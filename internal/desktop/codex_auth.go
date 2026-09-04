package desktop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

type CodexAuthStatus struct {
	Configured bool   `json:"configured"`
	Storage    string `json:"storage"`
}

type CodexAuthInvocation struct {
	Executable  string
	Arguments   []string
	Environment []string
	Stdin       []byte
}

type CodexAuthRunResult struct{ ExitCode int }

type CodexAuthRunner interface {
	Run(context.Context, CodexAuthInvocation) (CodexAuthRunResult, error)
}

type CodexAuthManager struct {
	Executable  string
	CodexHome   string
	ProviderID  string
	Credentials ProviderCredentials
	Runner      CodexAuthRunner
}

// Bootstrap imports the already-authorized OS-stored API key into the isolated
// Codex home through the supported `codex login --with-api-key` stdin flow.
func (manager CodexAuthManager) Bootstrap(ctx context.Context) (CodexAuthStatus, error) {
	if err := manager.validate(); err != nil || ctx == nil {
		return CodexAuthStatus{}, errors.New("codex authentication bootstrap is invalid")
	}
	runner := manager.runner()
	err := manager.Credentials.WithSecret(manager.ProviderID, func(secret []byte) error {
		input := append([]byte(nil), secret...)
		defer clearSecret(input)
		result, runErr := runner.Run(ctx, CodexAuthInvocation{
			Executable: manager.Executable, Arguments: []string{"login", "--with-api-key"},
			Environment: codexAuthEnvironment(manager.CodexHome), Stdin: input,
		})
		if runErr != nil || result.ExitCode != 0 {
			return errors.New("codex authentication bootstrap failed")
		}
		return nil
	})
	if err != nil {
		return CodexAuthStatus{}, err
	}
	status, err := manager.Status(ctx)
	if err != nil || !status.Configured {
		return CodexAuthStatus{}, errors.New("codex authentication could not be verified")
	}
	return status, nil
}

func (manager CodexAuthManager) Status(ctx context.Context) (CodexAuthStatus, error) {
	if err := manager.validate(); err != nil || ctx == nil {
		return CodexAuthStatus{}, errors.New("codex authentication status is invalid")
	}
	result, err := manager.runner().Run(ctx, CodexAuthInvocation{
		Executable: manager.Executable, Arguments: []string{"login", "status"}, Environment: codexAuthEnvironment(manager.CodexHome),
	})
	if err != nil {
		return CodexAuthStatus{}, err
	}
	return CodexAuthStatus{Configured: result.ExitCode == 0, Storage: "isolated_codex_home"}, nil
}

func (manager CodexAuthManager) validate() error {
	if !filepath.IsAbs(manager.Executable) || !filepath.IsAbs(manager.CodexHome) || !desktopIdentifier(manager.ProviderID) || !filepath.IsAbs(manager.Credentials.ScopeRoot) {
		return errors.New("codex authentication configuration is incomplete")
	}
	home, err := os.Lstat(manager.CodexHome)
	if err != nil || !home.IsDir() || home.Mode()&os.ModeSymlink != 0 {
		return errors.New("isolated codex home is unavailable")
	}
	executable, err := os.Lstat(manager.Executable)
	if err != nil || executable.IsDir() || executable.Mode()&os.ModeSymlink != 0 {
		return errors.New("codex executable is unavailable")
	}
	return nil
}

func (manager CodexAuthManager) runner() CodexAuthRunner {
	if manager.Runner != nil {
		return manager.Runner
	}
	return OSCodexAuthRunner{}
}

type OSCodexAuthRunner struct{}

func (OSCodexAuthRunner) Run(ctx context.Context, invocation CodexAuthInvocation) (CodexAuthRunResult, error) {
	command := exec.CommandContext(ctx, invocation.Executable, invocation.Arguments...)
	command.Env = append([]string(nil), invocation.Environment...)
	command.Stdin = bytes.NewReader(invocation.Stdin)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	err := command.Run()
	if err == nil {
		return CodexAuthRunResult{ExitCode: 0}, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return CodexAuthRunResult{ExitCode: exit.ExitCode()}, nil
	}
	return CodexAuthRunResult{}, err
}

func codexAuthEnvironment(codexHome string) []string {
	keys := []string{"COMSPEC", "PATH", "PATHEXT", "SYSTEMROOT", "TEMP", "TMP", "WINDIR"}
	result := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			result = append(result, key+"="+value)
		}
	}
	result = append(result, "CODEX_HOME="+codexHome)
	sort.Strings(result)
	return result
}

var _ CodexAuthRunner = OSCodexAuthRunner{}
