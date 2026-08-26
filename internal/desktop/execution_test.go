package desktop

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutionEnablementRequiresCredentialDisposablePreflightAndConfirmations(t *testing.T) {
	manager, credentialStore, projectRoot, runtimePath, now := executionFixture(t)
	if _, err := manager.MarkDisposable(context.Background(), projectRoot, "wrong"); err == nil {
		t.Fatal("expected disposable marker confirmation to fail")
	}
	marker, err := manager.MarkDisposable(context.Background(), projectRoot, MarkDisposableConfirmation)
	if err != nil || !marker.Disposable {
		t.Fatalf("mark disposable = %#v err=%v", marker, err)
	}
	if _, err := manager.Preflight(context.Background(), "codex", "gpt-5.6-sol", runtimePath, projectRoot, "wrong"); err == nil {
		t.Fatal("expected disposable preflight confirmation to fail")
	}
	preflight, err := manager.Preflight(context.Background(), "codex", "gpt-5.6-sol", runtimePath, projectRoot, DisposableConfirmation)
	if err != nil || !preflight.Ready || len(preflight.CheckCodes) < 7 {
		t.Fatalf("execution preflight = %#v err=%v", preflight, err)
	}
	if _, err := manager.Enable(context.Background(), preflight.Receipt, "wrong"); err == nil {
		t.Fatal("expected enablement confirmation to fail")
	}
	state, err := manager.Enable(context.Background(), preflight.Receipt, EnableExecutionConfirmation)
	if err != nil || !state.Enabled || state.ProviderID != "codex" || !state.RuntimeExecutableConfigured || !state.PreflightReceiptConfigured {
		t.Fatalf("enabled state = %#v err=%v", state, err)
	}
	cfg, roots, err := SettingsManager{Base: manager.Base}.Active(context.Background())
	if err != nil || !cfg.Node.Execution.Enabled || cfg.Node.Execution.PreflightReceipt != preflight.Receipt {
		t.Fatalf("active execution config = %#v err=%v", cfg.Node.Execution, err)
	}
	secret := []byte("provider-key-super-secret")
	for _, path := range []string{manager.Base.ConfigPath, filepath.Join(roots.DataDir, ExecutionPreflightFileName)} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil || bytes.Contains(raw, secret) {
			t.Fatalf("credential leaked to %s: err=%v", path, readErr)
		}
	}
	controlPath := filepath.Join(roots.DataDir, "preserved-control.db")
	if err := os.WriteFile(controlPath, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err = manager.Disable(context.Background(), DisableExecutionConfirmation)
	if err != nil || state.Enabled || !state.PreflightReceiptConfigured {
		t.Fatalf("disabled state = %#v err=%v", state, err)
	}
	if raw, err := os.ReadFile(controlPath); err != nil || string(raw) != "preserved" {
		t.Fatalf("disable changed control data: %q err=%v", raw, err)
	}
	if len(credentialStore.secret) == 0 {
		t.Fatal("disable deleted provider credential")
	}
	if _, err := os.Stat(projectRoot); err != nil {
		t.Fatalf("disable deleted disposable worktree: %v", err)
	}
	_ = now
}

func TestExecutionPreflightExpiresAndRuntimeChangesFailClosed(t *testing.T) {
	manager, _, projectRoot, runtimePath, now := executionFixture(t)
	if _, err := manager.MarkDisposable(context.Background(), projectRoot, MarkDisposableConfirmation); err != nil {
		t.Fatal(err)
	}
	preflight, err := manager.Preflight(context.Background(), "codex", "gpt-5.6-sol", runtimePath, projectRoot, DisposableConfirmation)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(ExecutionPreflightLifetime + time.Second)
	if _, err := manager.Enable(context.Background(), preflight.Receipt, EnableExecutionConfirmation); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired enablement error = %v", err)
	}
	*now = now.Add(-ExecutionPreflightLifetime - time.Second)
	if err := os.WriteFile(runtimePath, []byte("changed-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(context.Background(), preflight.Receipt, EnableExecutionConfirmation); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed runtime error = %v", err)
	}
}

func TestExecutionEnablementRechecksDisposableProject(t *testing.T) {
	manager, _, projectRoot, runtimePath, _ := executionFixture(t)
	if _, err := manager.MarkDisposable(context.Background(), projectRoot, MarkDisposableConfirmation); err != nil {
		t.Fatal(err)
	}
	preflight, err := manager.Preflight(context.Background(), "codex", "gpt-5.6-sol", runtimePath, projectRoot, DisposableConfirmation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "unreviewed.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(context.Background(), preflight.Receipt, EnableExecutionConfirmation); err == nil || !strings.Contains(err.Error(), "project changed") {
		t.Fatalf("changed project error = %v", err)
	}
}

func TestExecutionPreflightRequiresOSCredentialAndIsolatedProject(t *testing.T) {
	manager, credentialStore, _, runtimePath, _ := executionFixture(t)
	credentialStore.secret = nil
	outside := t.TempDir()
	gitTestCommand(t, outside, "init")
	if _, err := manager.Preflight(context.Background(), "codex", "gpt-5.6-sol", runtimePath, outside, DisposableConfirmation); err == nil || !strings.Contains(err.Error(), "isolated child") {
		t.Fatalf("outside project error = %v", err)
	}
}

func executionFixture(t *testing.T) (ExecutionManager, *memorySecretStore, string, string, *time.Time) {
	t.Helper()
	settings := initializedSettingsManager(t)
	_, roots, err := settings.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(roots.WorktreeRoot, "disposable-preflight")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, projectRoot, "init")
	gitTestCommand(t, projectRoot, "config", "user.email", "slice2@example.invalid")
	gitTestCommand(t, projectRoot, "config", "user.name", "Slice Two Test")
	if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("disposable"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, projectRoot, "add", "README.md")
	gitTestCommand(t, projectRoot, "commit", "-m", "add disposable fixture")
	runtimePath := filepath.Join(t.TempDir(), "runtime.exe")
	if err := os.WriteFile(runtimePath, []byte("bounded-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	credentialStore := &memorySecretStore{}
	credentials := ProviderCredentials{
		ScopeRoot: roots.ConfigDir,
		Open:      func(_, _ string) (CredentialStore, error) { return credentialStore, nil },
	}
	if _, err := credentials.Set("codex", []byte("provider-key-super-secret")); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	manager := ExecutionManager{
		Base: settings.Base, Credentials: credentials, Now: func() time.Time { return now },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 128)),
	}
	return manager, credentialStore, projectRoot, runtimePath, &now
}

func gitTestCommand(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
