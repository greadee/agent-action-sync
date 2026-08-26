package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"syncgate/internal/config"
)

const (
	DisposableMarkerDirectory    = "disposable-projects"
	ExecutionPreflightFileName   = "execution-preflight.json"
	MarkDisposableConfirmation   = "MARK PROJECT DISPOSABLE"
	DisposableConfirmation       = "I CONFIRM THIS PROJECT IS DISPOSABLE"
	EnableExecutionConfirmation  = "ENABLE LOCAL EXECUTION"
	DisableExecutionConfirmation = "DISABLE LOCAL EXECUTION"
	ExecutionPreflightLifetime   = 15 * time.Minute
	maxGitOutputBytes            = 256 << 10
	maxRuntimeExecutableBytes    = 512 << 20
)

type DisposableMarker struct {
	SchemaVersion int       `json:"schema_version"`
	Disposable    bool      `json:"disposable"`
	OpaqueID      string    `json:"opaque_id"`
	MarkedAt      time.Time `json:"marked_at"`
}

type ExecutionPreflightRecord struct {
	SchemaVersion     int       `json:"schema_version"`
	Receipt           string    `json:"receipt"`
	ProviderID        string    `json:"provider_id"`
	ModelID           string    `json:"model_id"`
	RuntimeExecutable string    `json:"runtime_executable"`
	RuntimeDigest     string    `json:"runtime_digest"`
	ProjectRoot       string    `json:"project_root"`
	ProjectDigest     string    `json:"project_digest"`
	HeadDigest        string    `json:"head_digest"`
	PassedAt          time.Time `json:"passed_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	CheckCodes        []string  `json:"check_codes"`
}

type ExecutionPreflightResult struct {
	Ready      bool      `json:"ready"`
	Receipt    string    `json:"receipt"`
	ExpiresAt  time.Time `json:"expires_at"`
	CheckCodes []string  `json:"check_codes"`
}

type ExecutionState struct {
	Enabled                     bool   `json:"enabled"`
	ProviderID                  string `json:"provider_id,omitempty"`
	ModelID                     string `json:"model_id,omitempty"`
	MaxConcurrent               int    `json:"max_concurrent"`
	RuntimeExecutableConfigured bool   `json:"runtime_executable_configured"`
	PreflightReceiptConfigured  bool   `json:"preflight_receipt_configured"`
}

type ExecutionManager struct {
	Base        Roots
	Credentials ProviderCredentials
	Now         func() time.Time
	Random      io.Reader
	RunGit      func(context.Context, string, ...string) (string, error)
}

func (manager ExecutionManager) MarkDisposable(ctx context.Context, projectRoot, confirmation string) (DisposableMarker, error) {
	if confirmation != MarkDisposableConfirmation {
		return DisposableMarker{}, errors.New("disposable marker confirmation did not match")
	}
	_, roots, err := SettingsManager{Base: manager.Base}.Active(ctx)
	if err != nil {
		return DisposableMarker{}, err
	}
	project, err := manager.validateProjectRoot(roots, projectRoot)
	if err != nil {
		return DisposableMarker{}, err
	}
	if _, err := manager.gitDirectory(ctx, project); err != nil {
		return DisposableMarker{}, err
	}
	status, err := manager.git(ctx, project, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return DisposableMarker{}, err
	}
	if strings.TrimSpace(status) != "" {
		return DisposableMarker{}, errors.New("disposable project must be clean before marking")
	}
	randomBytes := make([]byte, 24)
	source := manager.Random
	if source == nil {
		source = rand.Reader
	}
	if _, err := io.ReadFull(source, randomBytes); err != nil {
		return DisposableMarker{}, fmt.Errorf("generate disposable project identity: %w", err)
	}
	digest := sha256.Sum256(randomBytes)
	clearSecret(randomBytes)
	marker := DisposableMarker{SchemaVersion: 1, Disposable: true, OpaqueID: "disposable:" + hex.EncodeToString(digest[:16]), MarkedAt: manager.now()}
	markerPath := disposableMarkerPath(roots.DataDir, project)
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		return DisposableMarker{}, fmt.Errorf("create disposable marker directory: %w", err)
	}
	if err := writeJSONAtomic(markerPath, marker, 0o600); err != nil {
		return DisposableMarker{}, fmt.Errorf("write disposable project marker: %w", err)
	}
	return marker, nil
}

func (manager ExecutionManager) Preflight(ctx context.Context, providerID, modelID, runtimeExecutable, projectRoot, confirmation string) (ExecutionPreflightResult, error) {
	if confirmation != DisposableConfirmation {
		return ExecutionPreflightResult{}, errors.New("disposable project confirmation did not match")
	}
	providerID = strings.TrimSpace(providerID)
	if !desktopIdentifier(providerID) {
		return ExecutionPreflightResult{}, errors.New("provider ID must be a lowercase identifier")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = "gpt-5.6-sol"
	}
	if !desktopModelIdentifier(modelID) {
		return ExecutionPreflightResult{}, errors.New("model ID must be a lowercase identifier")
	}
	active, roots, err := SettingsManager{Base: manager.Base}.Active(ctx)
	if err != nil {
		return ExecutionPreflightResult{}, err
	}
	if active.Node.Execution.Enabled {
		return ExecutionPreflightResult{}, errors.New("execution must be disabled before a new enablement preflight")
	}
	project, err := manager.validateProjectRoot(roots, projectRoot)
	if err != nil {
		return ExecutionPreflightResult{}, err
	}
	runtimeExecutable, runtimeDigest, err := inspectRuntimeExecutable(runtimeExecutable)
	if err != nil {
		return ExecutionPreflightResult{}, err
	}
	manager.Credentials.ScopeRoot = roots.ConfigDir
	credential, err := manager.Credentials.Status(providerID)
	if err != nil {
		return ExecutionPreflightResult{}, err
	}
	if !credential.Configured {
		return ExecutionPreflightResult{}, errors.New("provider credential is not configured in the OS credential store")
	}
	if _, err := manager.gitDirectory(ctx, project); err != nil {
		return ExecutionPreflightResult{}, err
	}
	marker, err := readDisposableMarker(disposableMarkerPath(roots.DataDir, project))
	if err != nil {
		return ExecutionPreflightResult{}, err
	}
	status, err := manager.git(ctx, project, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return ExecutionPreflightResult{}, err
	}
	if strings.TrimSpace(status) != "" {
		return ExecutionPreflightResult{}, errors.New("disposable project must be clean")
	}
	head, err := manager.git(ctx, project, "rev-parse", "HEAD")
	if err != nil {
		return ExecutionPreflightResult{}, err
	}
	head = strings.TrimSpace(head)
	if len(head) < 40 || len(head) > 64 || strings.Trim(head, "0123456789abcdef") != "" {
		return ExecutionPreflightResult{}, errors.New("disposable project HEAD is invalid")
	}
	now := manager.now()
	nonce := make([]byte, 32)
	source := manager.Random
	if source == nil {
		source = rand.Reader
	}
	if _, err := io.ReadFull(source, nonce); err != nil {
		return ExecutionPreflightResult{}, fmt.Errorf("generate execution preflight receipt: %w", err)
	}
	projectDigest := sha256.Sum256([]byte(strings.ToLower(project) + "\x00" + marker.OpaqueID))
	receiptDigest := sha256.Sum256([]byte(hex.EncodeToString(nonce) + "\x00" + providerID + "\x00" + modelID + "\x00" + runtimeDigest + "\x00" + hex.EncodeToString(projectDigest[:]) + "\x00" + head + "\x00" + now.Format(time.RFC3339Nano)))
	clearSecret(nonce)
	checks := []string{"config_valid", "credential_os_backed", "disposable_marker_valid", "git_clean", "git_head_valid", "project_isolated", "runtime_executable_valid"}
	record := ExecutionPreflightRecord{
		SchemaVersion: 1, Receipt: hex.EncodeToString(receiptDigest[:]), ProviderID: providerID, ModelID: modelID,
		RuntimeExecutable: runtimeExecutable, RuntimeDigest: runtimeDigest,
		ProjectRoot: project, ProjectDigest: hex.EncodeToString(projectDigest[:]), HeadDigest: head,
		PassedAt: now, ExpiresAt: now.Add(ExecutionPreflightLifetime), CheckCodes: checks,
	}
	if err := writeJSONAtomic(filepath.Join(roots.DataDir, ExecutionPreflightFileName), record, 0o600); err != nil {
		return ExecutionPreflightResult{}, fmt.Errorf("write execution preflight: %w", err)
	}
	return ExecutionPreflightResult{Ready: true, Receipt: record.Receipt, ExpiresAt: record.ExpiresAt, CheckCodes: append([]string(nil), checks...)}, nil
}

func (manager ExecutionManager) Enable(ctx context.Context, receipt, confirmation string) (ExecutionState, error) {
	if confirmation != EnableExecutionConfirmation {
		return ExecutionState{}, errors.New("execution enablement confirmation did not match")
	}
	settings := SettingsManager{Base: manager.Base}
	active, roots, err := settings.Active(ctx)
	if err != nil {
		return ExecutionState{}, err
	}
	if err := ensureNoPendingSettings(manager.Base); err != nil {
		return ExecutionState{}, err
	}
	if err := ensureNodeStopped(active); err != nil {
		return ExecutionState{}, err
	}
	record, err := readExecutionPreflight(filepath.Join(roots.DataDir, ExecutionPreflightFileName))
	if err != nil {
		return ExecutionState{}, err
	}
	if receipt == "" || receipt != record.Receipt {
		return ExecutionState{}, errors.New("execution preflight receipt did not match")
	}
	if !manager.now().Before(record.ExpiresAt) {
		return ExecutionState{}, errors.New("execution preflight receipt expired")
	}
	manager.Credentials.ScopeRoot = roots.ConfigDir
	credential, err := manager.Credentials.Status(record.ProviderID)
	if err != nil || !credential.Configured {
		if err != nil {
			return ExecutionState{}, err
		}
		return ExecutionState{}, errors.New("provider credential is no longer configured")
	}
	_, digest, err := inspectRuntimeExecutable(record.RuntimeExecutable)
	if err != nil || digest != record.RuntimeDigest {
		return ExecutionState{}, errors.New("runtime executable changed after preflight")
	}
	project, err := manager.validateProjectRoot(roots, record.ProjectRoot)
	if err != nil {
		return ExecutionState{}, errors.New("disposable project changed after preflight")
	}
	if _, err := manager.gitDirectory(ctx, project); err != nil {
		return ExecutionState{}, errors.New("disposable project changed after preflight")
	}
	marker, err := readDisposableMarker(disposableMarkerPath(roots.DataDir, project))
	if err != nil {
		return ExecutionState{}, errors.New("disposable project changed after preflight")
	}
	projectDigest := sha256.Sum256([]byte(strings.ToLower(project) + "\x00" + marker.OpaqueID))
	status, statusErr := manager.git(ctx, project, "status", "--porcelain=v1", "--untracked-files=all")
	head, headErr := manager.git(ctx, project, "rev-parse", "HEAD")
	if statusErr != nil || headErr != nil || strings.TrimSpace(status) != "" || strings.TrimSpace(head) != record.HeadDigest || hex.EncodeToString(projectDigest[:]) != record.ProjectDigest {
		return ExecutionState{}, errors.New("disposable project changed after preflight")
	}
	active.Node.Execution = config.ExecutionConfig{
		Enabled: true, ProviderID: record.ProviderID, ModelID: record.ModelID, MaxConcurrent: 1,
		RuntimeExecutable: record.RuntimeExecutable, PreflightReceipt: record.Receipt,
	}
	if err := active.ApplyDefaultsAndValidate(); err != nil {
		return ExecutionState{}, err
	}
	if err := activateDirect(manager.Base, active); err != nil {
		return ExecutionState{}, err
	}
	return executionState(active), nil
}

func (manager ExecutionManager) Disable(ctx context.Context, confirmation string) (ExecutionState, error) {
	if confirmation != DisableExecutionConfirmation {
		return ExecutionState{}, errors.New("execution disable confirmation did not match")
	}
	active, _, err := SettingsManager{Base: manager.Base}.Active(ctx)
	if err != nil {
		return ExecutionState{}, err
	}
	if err := ensureNoPendingSettings(manager.Base); err != nil {
		return ExecutionState{}, err
	}
	if err := ensureNodeStopped(active); err != nil {
		return ExecutionState{}, err
	}
	active.Node.Execution.Enabled = false
	if err := active.ApplyDefaultsAndValidate(); err != nil {
		return ExecutionState{}, err
	}
	if err := activateDirect(manager.Base, active); err != nil {
		return ExecutionState{}, err
	}
	return executionState(active), nil
}

func (manager ExecutionManager) State(ctx context.Context) (ExecutionState, error) {
	active, _, err := SettingsManager{Base: manager.Base}.Active(ctx)
	if err != nil {
		return ExecutionState{}, err
	}
	return executionState(active), nil
}

// ValidateEnabledAuthorization rechecks the durable Slice 2 authorization at
// node startup without extending or replaying it. An enabled switch remains
// valid after its activation receipt expires, but every mutable preflight fact
// is checked again before runtime components are constructed.
func (manager ExecutionManager) ValidateEnabledAuthorization(ctx context.Context, cfg config.Config, roots Roots) (ExecutionPreflightRecord, error) {
	if !cfg.Node.Execution.Enabled {
		return ExecutionPreflightRecord{}, errors.New("local execution is disabled")
	}
	record, err := readExecutionPreflight(filepath.Join(roots.DataDir, ExecutionPreflightFileName))
	if err != nil || record.Receipt != cfg.Node.Execution.PreflightReceipt || record.ProviderID != cfg.Node.Execution.ProviderID || record.ModelID != cfg.Node.Execution.ModelID || record.RuntimeExecutable != cfg.Node.Execution.RuntimeExecutable {
		return ExecutionPreflightRecord{}, errors.New("enabled execution authorization does not match preflight evidence")
	}
	manager.Credentials.ScopeRoot = roots.ConfigDir
	credential, err := manager.Credentials.Status(record.ProviderID)
	if err != nil || !credential.Configured {
		return ExecutionPreflightRecord{}, errors.New("enabled provider credential is unavailable")
	}
	_, digest, err := inspectRuntimeExecutable(record.RuntimeExecutable)
	if err != nil || digest != record.RuntimeDigest {
		return ExecutionPreflightRecord{}, errors.New("enabled runtime executable changed after preflight")
	}
	project, err := manager.validateProjectRoot(roots, record.ProjectRoot)
	if err != nil {
		return ExecutionPreflightRecord{}, errors.New("enabled disposable project changed after preflight")
	}
	if _, err := manager.gitDirectory(ctx, project); err != nil {
		return ExecutionPreflightRecord{}, errors.New("enabled disposable project changed after preflight")
	}
	marker, err := readDisposableMarker(disposableMarkerPath(roots.DataDir, project))
	if err != nil {
		return ExecutionPreflightRecord{}, errors.New("enabled disposable project changed after preflight")
	}
	projectDigest := sha256.Sum256([]byte(strings.ToLower(project) + "\x00" + marker.OpaqueID))
	status, statusErr := manager.git(ctx, project, "status", "--porcelain=v1", "--untracked-files=all")
	head, headErr := manager.git(ctx, project, "rev-parse", "HEAD")
	if statusErr != nil || headErr != nil || strings.TrimSpace(status) != "" || strings.TrimSpace(head) != record.HeadDigest || hex.EncodeToString(projectDigest[:]) != record.ProjectDigest {
		return ExecutionPreflightRecord{}, errors.New("enabled disposable project changed after preflight")
	}
	return record, nil
}

func (manager ExecutionManager) validateProjectRoot(roots Roots, projectRoot string) (string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if !filepath.IsAbs(projectRoot) {
		return "", errors.New("disposable project root must be absolute")
	}
	project, err := filepath.Abs(filepath.Clean(projectRoot))
	if err != nil {
		return "", fmt.Errorf("resolve disposable project root: %w", err)
	}
	worktrees, err := filepath.Abs(filepath.Clean(roots.WorktreeRoot))
	if err != nil {
		return "", fmt.Errorf("resolve worktree root: %w", err)
	}
	relative, err := filepath.Rel(worktrees, project)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("disposable project must be an isolated child of the configured worktree root")
	}
	current := worktrees
	for _, part := range strings.FieldsFunc(relative, func(value rune) bool { return value == '/' || value == '\\' }) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("disposable project path contains an unavailable or symbolic-link component")
		}
	}
	info, err := os.Lstat(project)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("disposable project root is unavailable")
	}
	return project, nil
}

func (manager ExecutionManager) gitDirectory(ctx context.Context, project string) (string, error) {
	value, err := manager.git(ctx, project, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", errors.New("disposable project is not a Git worktree")
	}
	gitDir := filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(gitDir) {
		return "", errors.New("Git metadata directory is not absolute")
	}
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return "", errors.New("Git metadata directory is unavailable")
	}
	return gitDir, nil
}

func (manager ExecutionManager) git(ctx context.Context, directory string, arguments ...string) (string, error) {
	if manager.RunGit != nil {
		return manager.RunGit(ctx, directory, arguments...)
	}
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	stdout := &boundedOutput{limit: maxGitOutputBytes}
	stderr := &boundedOutput{limit: maxGitOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("Git preflight failed: %w", err)
	}
	return stdout.String(), nil
}

func (manager ExecutionManager) now() time.Time {
	if manager.Now == nil {
		return time.Now().UTC()
	}
	return manager.Now().UTC()
}

func inspectRuntimeExecutable(path string) (string, string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return "", "", errors.New("runtime executable must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("runtime executable must be a regular non-symlink file")
	}
	if info.Size() < 1 || info.Size() > maxRuntimeExecutableBytes {
		return "", "", errors.New("runtime executable size is outside the preflight bound")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("open runtime executable: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxRuntimeExecutableBytes+1)); err != nil {
		return "", "", fmt.Errorf("hash runtime executable: %w", err)
	}
	return path, hex.EncodeToString(hash.Sum(nil)), nil
}

func readDisposableMarker(path string) (DisposableMarker, error) {
	var marker DisposableMarker
	if err := readStrictJSON(path, &marker); err != nil {
		return DisposableMarker{}, fmt.Errorf("read disposable project marker: %w", err)
	}
	if marker.SchemaVersion != 1 || !marker.Disposable || !strings.HasPrefix(marker.OpaqueID, "disposable:") || marker.MarkedAt.IsZero() {
		return DisposableMarker{}, errors.New("disposable project marker is invalid")
	}
	return marker, nil
}

func readExecutionPreflight(path string) (ExecutionPreflightRecord, error) {
	var record ExecutionPreflightRecord
	if err := readStrictJSON(path, &record); err != nil {
		return ExecutionPreflightRecord{}, fmt.Errorf("read execution preflight: %w", err)
	}
	if record.SchemaVersion != 1 || !lowerHexDigest(record.Receipt, 64) || !desktopIdentifier(record.ProviderID) || !desktopModelIdentifier(record.ModelID) || !filepath.IsAbs(record.ProjectRoot) ||
		!lowerHexDigest(record.RuntimeDigest, 64) || !lowerHexDigest(record.ProjectDigest, 64) || !lowerHexDigest(record.HeadDigest, len(record.HeadDigest)) || len(record.HeadDigest) < 40 || len(record.HeadDigest) > 64 ||
		record.PassedAt.IsZero() || record.ExpiresAt.IsZero() || len(record.CheckCodes) == 0 {
		return ExecutionPreflightRecord{}, errors.New("execution preflight record is invalid")
	}
	return record, nil
}

func readStrictJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxConfigBytes {
		return errors.New("JSON exceeds size bound")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func disposableMarkerPath(dataDir, project string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(project))))
	return filepath.Join(dataDir, DisposableMarkerDirectory, hex.EncodeToString(digest[:16])+".json")
}

func activateDirect(base Roots, cfg config.Config) error {
	active, err := config.LoadFile(context.Background(), base.ConfigPath)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(base.ConfigDir, PreviousConfigFileName), active, 0o600); err != nil {
		return err
	}
	if err := writeJSONAtomic(base.ConfigPath, cfg, 0o600); err != nil {
		return err
	}
	return nil
}

func ensureNoPendingSettings(base Roots) error {
	_, err := os.Stat(filepath.Join(base.ConfigDir, PendingConfigFileName))
	if err == nil {
		return errors.New("pending settings must be applied or discarded before changing execution state")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func executionState(cfg config.Config) ExecutionState {
	return ExecutionState{
		Enabled: cfg.Node.Execution.Enabled, ProviderID: cfg.Node.Execution.ProviderID, ModelID: cfg.Node.Execution.ModelID, MaxConcurrent: cfg.Node.Execution.MaxConcurrent,
		RuntimeExecutableConfigured: cfg.Node.Execution.RuntimeExecutable != "",
		PreflightReceiptConfigured:  cfg.Node.Execution.PreflightReceipt != "",
	}
}

func lowerHexDigest(value string, length int) bool {
	return len(value) == length && strings.Trim(value, "0123456789abcdef") == ""
}

func desktopModelIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] == '.' || value[len(value)-1] == '.' {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

type boundedOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	remaining := output.limit - output.buffer.Len()
	if remaining <= 0 || len(value) > remaining {
		if remaining > 0 {
			_, _ = output.buffer.Write(value[:remaining])
		}
		return remaining, errors.New("command output exceeded bound")
	}
	return output.buffer.Write(value)
}
func (output *boundedOutput) String() string { return output.buffer.String() }
