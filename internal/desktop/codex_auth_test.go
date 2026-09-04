package desktop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingCodexAuthRunner struct {
	invocations []CodexAuthInvocation
	results     []CodexAuthRunResult
	err         error
}

func (runner *recordingCodexAuthRunner) Run(_ context.Context, invocation CodexAuthInvocation) (CodexAuthRunResult, error) {
	copy := invocation
	copy.Arguments = append([]string(nil), invocation.Arguments...)
	copy.Environment = append([]string(nil), invocation.Environment...)
	copy.Stdin = append([]byte(nil), invocation.Stdin...)
	runner.invocations = append(runner.invocations, copy)
	if runner.err != nil {
		return CodexAuthRunResult{}, runner.err
	}
	if len(runner.results) == 0 {
		return CodexAuthRunResult{}, nil
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	return result, nil
}

func TestCodexAuthBootstrapUsesIsolatedHomeAndCredentialStdin(t *testing.T) {
	root := t.TempDir()
	executable, home := filepath.Join(root, "codex.exe"), filepath.Join(root, "codex-home")
	if err := os.WriteFile(executable, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := []byte("provider-secret-value")
	store := &memorySecretStore{secret: append([]byte(nil), secret...)}
	runner := &recordingCodexAuthRunner{results: []CodexAuthRunResult{{ExitCode: 0}, {ExitCode: 0}}}
	manager := CodexAuthManager{
		Executable: executable, CodexHome: home, ProviderID: "codex", Runner: runner,
		Credentials: ProviderCredentials{ScopeRoot: root, Open: func(_, _ string) (CredentialStore, error) { return store, nil }},
	}
	status, err := manager.Bootstrap(context.Background())
	if err != nil || !status.Configured || status.Storage != "isolated_codex_home" || len(runner.invocations) != 2 {
		t.Fatalf("status=%+v invocations=%d err=%v", status, len(runner.invocations), err)
	}
	login, verify := runner.invocations[0], runner.invocations[1]
	if strings.Join(login.Arguments, " ") != "login --with-api-key" || !bytes.Equal(login.Stdin, secret) || strings.Join(verify.Arguments, " ") != "login status" || len(verify.Stdin) != 0 {
		t.Fatalf("login=%+v verify=%+v", login, verify)
	}
	joined := strings.Join(login.Environment, "\n")
	if !strings.Contains(joined, "CODEX_HOME="+home) || strings.Contains(joined, "OPENAI_API_KEY=") || strings.Contains(joined, "CODEX_API_KEY=") || strings.Contains(strings.Join(login.Arguments, " "), string(secret)) {
		t.Fatalf("unsafe authentication invocation: args=%v env=%v", login.Arguments, login.Environment)
	}
}

func TestCodexAuthStatusReportsLoggedOutWithoutLeakingRunnerOutput(t *testing.T) {
	root := t.TempDir()
	executable, home := filepath.Join(root, "codex.exe"), filepath.Join(root, "codex-home")
	_ = os.WriteFile(executable, []byte("runtime"), 0o600)
	_ = os.Mkdir(home, 0o700)
	runner := &recordingCodexAuthRunner{results: []CodexAuthRunResult{{ExitCode: 1}}}
	manager := CodexAuthManager{Executable: executable, CodexHome: home, ProviderID: "codex", Runner: runner, Credentials: ProviderCredentials{ScopeRoot: root}}
	status, err := manager.Status(context.Background())
	if err != nil || status.Configured || status.Storage != "isolated_codex_home" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	runner.err = errors.New("could not start")
	if _, err := manager.Status(context.Background()); err == nil {
		t.Fatal("runner startup failure was hidden")
	}
}
