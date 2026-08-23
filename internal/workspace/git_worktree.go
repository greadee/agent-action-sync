package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
)

const gitRegistrySchema = "syncgate.git-worktree.v1"

const (
	MaxChangedFiles     = 4096
	MaxChangedFileBytes = 64 << 20
)

type GitManagerConfig struct {
	ProjectSyncRoot    string
	RepositoryRoot     string
	WorktreeBase       string
	GitExecutable      string
	AllowedBaseCommits []string
}

type GitWorktreeManager struct {
	mu     sync.Mutex
	config GitManagerConfig
}

type ChangedFile struct {
	RelativePath string `json:"relative_path"`
	Status       string `json:"status"`
	Digest       string `json:"digest,omitempty"`
	Size         int64  `json:"size"`
}

type ChangeManifest struct {
	WorkspaceID string        `json:"workspace_id"`
	BaseCommit  string        `json:"base_commit"`
	HeadCommit  string        `json:"head_commit"`
	Files       []ChangedFile `json:"files"`
	Digest      string        `json:"digest"`
}

type gitRegistryRecord struct {
	Schema        string    `json:"schema"`
	Workspace     Workspace `json:"workspace"`
	RequestDigest string    `json:"request_digest"`
	WorktreePath  string    `json:"worktree_path"`
}

func NewGitWorktreeManager(config GitManagerConfig) (*GitWorktreeManager, error) {
	if config.GitExecutable == "" {
		config.GitExecutable = "git"
	}
	for _, value := range []string{config.ProjectSyncRoot, config.RepositoryRoot, config.WorktreeBase} {
		if value == "" || !filepath.IsAbs(value) {
			return nil, ErrUnsafeRoot
		}
	}
	if len(config.AllowedBaseCommits) == 0 {
		return nil, ErrBaseCommit
	}
	for _, value := range config.AllowedBaseCommits {
		if !validCommit(value) {
			return nil, ErrBaseCommit
		}
	}
	return &GitWorktreeManager{config: config}, nil
}

func AttemptBranchName(attemptID string) (string, error) {
	if !namespaced(attemptID, "attempt:") {
		return "", ErrInvalidRequest
	}
	return "syncgate/attempt-" + hash([]byte(attemptID))[:20], nil
}

func (manager *GitWorktreeManager) Allocate(ctx context.Context, request PreflightRequest) (Workspace, error) {
	if err := contextError(ctx); err != nil {
		return Workspace{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if filepath.Clean(request.ProjectSyncRoot) != filepath.Clean(manager.config.ProjectSyncRoot) || filepath.Clean(request.RepositoryRoot) != filepath.Clean(manager.config.RepositoryRoot) || filepath.Clean(request.WorktreeBase) != filepath.Clean(manager.config.WorktreeBase) {
		return Workspace{}, ErrUnsafeRoot
	}
	wantBranch, err := AttemptBranchName(request.AttemptID)
	if err != nil || request.BranchName != wantBranch {
		return Workspace{}, ErrBranchName
	}
	if !validCommit(request.BaseCommit) {
		return Workspace{}, ErrBaseCommit
	}
	allowedBase := false
	for _, value := range manager.config.AllowedBaseCommits {
		if value == request.BaseCommit {
			allowedBase = true
			break
		}
	}
	if !allowedBase {
		return Workspace{}, ErrBaseCommit
	}
	record, readErr := manager.readRecord(request.WorkspaceID)
	if readErr == nil {
		if record.RequestDigest != allocationFingerprint(request) || record.Workspace.OwnerID != request.OwnerID {
			return Workspace{}, ErrCollision
		}
		return manager.reconcileRecord(ctx, record)
	}
	if !errors.Is(readErr, ErrWorkspaceMissing) {
		return Workspace{}, readErr
	}
	request.RepositoryDirty, err = manager.repositoryDirty(ctx, manager.config.RepositoryRoot)
	if err != nil {
		return Workspace{}, err
	}
	request.BranchExists, err = manager.branchExists(ctx, request.BranchName)
	if err != nil {
		return Workspace{}, err
	}
	if _, err = Preflight(request); err != nil {
		return Workspace{}, err
	}
	resolved, err := manager.git(ctx, manager.config.RepositoryRoot, "rev-parse", "--verify", request.BaseCommit+"^{commit}")
	if err != nil || strings.TrimSpace(resolved) != request.BaseCommit {
		return Workspace{}, ErrBaseCommit
	}
	target := manager.target(request.WorkspaceID)
	if output, err := manager.git(ctx, manager.config.RepositoryRoot, "worktree", "add", "-b", request.BranchName, target, request.BaseCommit); err != nil {
		return Workspace{}, fmt.Errorf("provision git worktree: %w: %s", err, sanitizeGitOutput(output))
	}
	head, err := manager.git(ctx, target, "rev-parse", "HEAD")
	if err != nil {
		return Workspace{}, err
	}
	workspace := Workspace{WorkspaceID: request.WorkspaceID, OwnerID: request.OwnerID, BranchName: request.BranchName, Generation: 1, State: StateAllocated, BaseCommit: request.BaseCommit, HeadCommit: strings.TrimSpace(head)}
	if err := manager.writeRecord(gitRegistryRecord{Schema: gitRegistrySchema, Workspace: workspace, RequestDigest: allocationFingerprint(request), WorktreePath: target}); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

func (manager *GitWorktreeManager) Inspect(ctx context.Context, workspaceID string) (Workspace, error) {
	if err := contextError(ctx); err != nil {
		return Workspace{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.readRecord(workspaceID)
	if err != nil {
		return Workspace{}, err
	}
	return manager.reconcileRecord(ctx, record)
}

func (manager *GitWorktreeManager) Release(ctx context.Context, claim CleanupClaim) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.readRecord(claim.WorkspaceID)
	if err != nil {
		return err
	}
	if record.Workspace.OwnerID != claim.OwnerID || record.Workspace.Generation != claim.Generation || record.Workspace.State != StateAllocated {
		return ErrOwnership
	}
	dirty, err := manager.repositoryDirty(ctx, record.WorktreePath)
	if err != nil || dirty {
		record.Workspace.State = StateQuarantined
		record.Workspace.Generation++
		if writeErr := manager.writeRecord(record); writeErr != nil {
			return writeErr
		}
		return ErrCleanupRequired
	}
	if output, err := manager.git(ctx, manager.config.RepositoryRoot, "worktree", "remove", record.WorktreePath); err != nil {
		return fmt.Errorf("release git worktree: %w: %s", err, sanitizeGitOutput(output))
	}
	record.Workspace.State = StateReleased
	record.Workspace.Generation++
	return manager.writeRecord(record)
}

func (manager *GitWorktreeManager) InspectChanges(ctx context.Context, workspaceID string, contract executioncontract.Contract) (ChangeManifest, error) {
	if err := contextError(ctx); err != nil {
		return ChangeManifest{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.readRecord(workspaceID)
	if err != nil {
		return ChangeManifest{}, err
	}
	if record.Workspace.State != StateAllocated {
		return ChangeManifest{}, ErrOwnership
	}
	head, err := manager.git(ctx, record.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return ChangeManifest{}, err
	}
	output, err := manager.gitBytes(ctx, record.WorktreePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return ChangeManifest{}, err
	}
	entries, err := parsePorcelain(output)
	if err != nil {
		return ChangeManifest{}, err
	}
	committed, err := manager.gitBytes(ctx, record.WorktreePath, "diff", "--name-status", "-z", record.Workspace.BaseCommit, strings.TrimSpace(head))
	if err != nil {
		return ChangeManifest{}, err
	}
	committedEntries, err := parseNameStatus(committed)
	if err != nil {
		return ChangeManifest{}, err
	}
	byPath := make(map[string]porcelainEntry, len(committedEntries)+len(entries))
	for _, entry := range committedEntries {
		byPath[entry.path] = entry
	}
	for _, entry := range entries {
		byPath[entry.path] = entry
	}
	entries = entries[:0]
	for _, entry := range byPath {
		entries = append(entries, entry)
	}
	if len(entries) > MaxChangedFiles {
		return ChangeManifest{}, ErrChangeLimit
	}
	files := make([]ChangedFile, 0, len(entries))
	for _, entry := range entries {
		if err := ValidateCollectedPath(contract, entry.path); err != nil {
			return ChangeManifest{}, err
		}
		file := ChangedFile{RelativePath: entry.path, Status: entry.status}
		full := filepath.Join(record.WorktreePath, filepath.FromSlash(entry.path))
		info, statErr := os.Lstat(full)
		if statErr == nil {
			if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return ChangeManifest{}, executioncontract.ErrPrivilegeEscalation
			}
			if info.Size() > MaxChangedFileBytes {
				return ChangeManifest{}, ErrChangeLimit
			}
			raw, readErr := os.ReadFile(full)
			if readErr != nil {
				return ChangeManifest{}, readErr
			}
			file.Digest, file.Size = hash(raw), int64(len(raw))
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return ChangeManifest{}, statErr
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	manifest := ChangeManifest{WorkspaceID: workspaceID, BaseCommit: record.Workspace.BaseCommit, HeadCommit: strings.TrimSpace(head), Files: files}
	raw, _ := json.Marshal(manifest)
	manifest.Digest = hash(raw)
	return manifest, nil
}

type porcelainEntry struct{ status, path string }

func parsePorcelain(raw []byte) ([]porcelainEntry, error) {
	parts := bytes.Split(raw, []byte{0})
	result := make([]porcelainEntry, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		if len(parts[i]) == 0 {
			continue
		}
		line := string(parts[i])
		if len(line) < 4 {
			return nil, ErrInvalidRequest
		}
		rawStatus := strings.TrimSpace(line[:2])
		status, statusErr := normalizeGitStatus(rawStatus)
		if statusErr != nil {
			return nil, statusErr
		}
		path := filepath.ToSlash(line[3:])
		if strings.Contains(rawStatus, "R") || strings.Contains(rawStatus, "C") {
			i++
			if i >= len(parts) || len(parts[i]) == 0 {
				return nil, ErrInvalidRequest
			}
			path = filepath.ToSlash(string(parts[i]))
		}
		if err := project.ValidateProjectRelativePath(path); err != nil {
			return nil, ErrInvalidRequest
		}
		result = append(result, porcelainEntry{status: status, path: path})
	}
	return result, nil
}

func parseNameStatus(raw []byte) ([]porcelainEntry, error) {
	parts := bytes.Split(raw, []byte{0})
	result := make([]porcelainEntry, 0, len(parts)/2)
	for index := 0; index < len(parts); {
		if len(parts[index]) == 0 {
			index++
			continue
		}
		rawStatus := string(parts[index])
		status, statusErr := normalizeGitStatus(rawStatus)
		if statusErr != nil {
			return nil, statusErr
		}
		index++
		if index >= len(parts) || len(parts[index]) == 0 {
			return nil, ErrInvalidRequest
		}
		path := filepath.ToSlash(string(parts[index]))
		index++
		if strings.HasPrefix(rawStatus, "R") || strings.HasPrefix(rawStatus, "C") {
			if index >= len(parts) || len(parts[index]) == 0 {
				return nil, ErrInvalidRequest
			}
			path = filepath.ToSlash(string(parts[index]))
			index++
		}
		if err := project.ValidateProjectRelativePath(path); err != nil {
			return nil, ErrInvalidRequest
		}
		result = append(result, porcelainEntry{status: status, path: path})
	}
	return result, nil
}

func normalizeGitStatus(value string) (string, error) {
	if strings.Contains(value, "U") || value == "AA" || value == "DD" {
		return "", ErrCollision
	}
	switch {
	case value == "??":
		return "untracked", nil
	case strings.Contains(value, "R"):
		return "renamed", nil
	case strings.Contains(value, "C"):
		return "copied", nil
	case strings.Contains(value, "D"):
		return "deleted", nil
	case strings.Contains(value, "A"):
		return "added", nil
	case strings.ContainsAny(value, "MT"):
		return "modified", nil
	default:
		return "", ErrInvalidRequest
	}
}

func (manager *GitWorktreeManager) repositoryDirty(ctx context.Context, root string) (bool, error) {
	output, err := manager.gitBytes(ctx, root, "status", "--porcelain=v1", "-z")
	return len(output) > 0, err
}
func (manager *GitWorktreeManager) branchExists(ctx context.Context, branch string) (bool, error) {
	_, err := manager.git(ctx, manager.config.RepositoryRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
func (manager *GitWorktreeManager) reconcileRecord(ctx context.Context, record gitRegistryRecord) (Workspace, error) {
	if record.Schema != gitRegistrySchema || !namespaced(record.Workspace.WorkspaceID, "workspace:") || !namespaced(record.Workspace.OwnerID, "assignment:") || !validCommit(record.Workspace.BaseCommit) || !validCommit(record.Workspace.HeadCommit) || record.Workspace.Generation < 1 || (record.Workspace.State != StateAllocated && record.Workspace.State != StateQuarantined && record.Workspace.State != StateReleased) || record.WorktreePath != manager.target(record.Workspace.WorkspaceID) {
		return Workspace{}, ErrUnsafeRoot
	}
	if record.Workspace.State == StateAllocated {
		if info, err := os.Lstat(record.WorktreePath); err != nil || !info.IsDir() {
			record.Workspace.State = StateQuarantined
			record.Workspace.Generation++
			if err := manager.writeRecord(record); err != nil {
				return Workspace{}, err
			}
		}
	}
	return record.Workspace, nil
}
func (manager *GitWorktreeManager) target(id string) string {
	return filepath.Join(manager.config.WorktreeBase, workspaceDirectoryKey(id))
}
func (manager *GitWorktreeManager) registryPath(id string) string {
	return filepath.Join(manager.config.WorktreeBase, ".syncgate-workspaces", workspaceDirectoryKey(id)+".json")
}
func (manager *GitWorktreeManager) readRecord(id string) (gitRegistryRecord, error) {
	raw, err := os.ReadFile(manager.registryPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return gitRegistryRecord{}, ErrWorkspaceMissing
	}
	if err != nil {
		return gitRegistryRecord{}, err
	}
	var record gitRegistryRecord
	if json.Unmarshal(raw, &record) != nil {
		return gitRegistryRecord{}, ErrUnsafeRoot
	}
	return record, nil
}
func (manager *GitWorktreeManager) writeRecord(record gitRegistryRecord) error {
	dir := filepath.Dir(manager.registryPath(record.Workspace.WorkspaceID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temporary := manager.registryPath(record.Workspace.WorkspaceID) + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, manager.registryPath(record.Workspace.WorkspaceID))
}
func (manager *GitWorktreeManager) git(ctx context.Context, root string, args ...string) (string, error) {
	raw, err := manager.gitBytes(ctx, root, args...)
	return string(raw), err
}
func (manager *GitWorktreeManager) gitBytes(ctx context.Context, root string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(ctx, manager.config.GitExecutable, commandArgs...)
	return command.CombinedOutput()
}
func validCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}
func sanitizeGitOutput(value string) string {
	if strings.TrimSpace(value) == "" {
		return "git command failed"
	}
	sum := sha256.Sum256([]byte(value))
	return "git-output:" + hex.EncodeToString(sum[:8])
}
