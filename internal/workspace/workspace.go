// Package workspace defines safe, opaque workspace allocation contracts.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
)

var (
	ErrInvalidRequest   = errors.New("invalid workspace request")
	ErrUnsafeRoot       = errors.New("unsafe workspace root")
	ErrDirtyRepository  = errors.New("repository is dirty")
	ErrBranchName       = errors.New("unsafe branch name")
	ErrCollision        = errors.New("workspace collision")
	ErrDiskLimit        = errors.New("workspace disk limit unavailable")
	ErrOwnership        = errors.New("workspace cleanup ownership mismatch")
	ErrWorkspaceMissing = errors.New("workspace not found")
	ErrBaseCommit       = errors.New("workspace base commit is not allowed")
	ErrCleanupRequired  = errors.New("workspace requires operator cleanup")
	ErrChangeLimit      = errors.New("workspace change manifest exceeds limits")
)

type State string

const (
	StateAllocated   State = "allocated"
	StateQuarantined State = "quarantined"
	StateReleased    State = "released"
)

type PreflightRequest struct {
	WorkspaceID          string
	OwnerID              string
	AttemptID            string
	ProjectSyncRoot      string
	RepositoryRoot       string
	WorktreeBase         string
	BranchName           string
	BaseCommit           string
	BranchExists         bool
	RepositoryDirty      bool
	RequiredDiskBytes    int64
	AvailableDiskBytes   int64
	IdempotencyKeyDigest string
}

type PreflightReport struct {
	WorkspaceID string
	BranchName  string
	Ready       bool
	Checks      []string
}

type Workspace struct {
	WorkspaceID string `json:"workspace_id"`
	OwnerID     string `json:"owner_id"`
	BranchName  string `json:"branch_name"`
	Generation  int64  `json:"generation"`
	State       State  `json:"state"`
	BaseCommit  string `json:"base_commit,omitempty"`
	HeadCommit  string `json:"head_commit,omitempty"`
}

type CleanupClaim struct {
	WorkspaceID string
	OwnerID     string
	Generation  int64
}

type Manager interface {
	Allocate(context.Context, PreflightRequest) (Workspace, error)
	Inspect(context.Context, string) (Workspace, error)
	Release(context.Context, CleanupClaim) error
}

func Preflight(request PreflightRequest) (PreflightReport, error) {
	if !namespaced(request.WorkspaceID, "workspace:") || !namespaced(request.OwnerID, "assignment:") ||
		!validDigest(request.IdempotencyKeyDigest) || request.RequiredDiskBytes < 1 || request.AvailableDiskBytes < 0 {
		return PreflightReport{}, ErrInvalidRequest
	}
	if request.RepositoryDirty {
		return PreflightReport{}, ErrDirtyRepository
	}
	if err := ValidateBranchName(request.BranchName); err != nil {
		return PreflightReport{}, err
	}
	if request.BranchExists {
		return PreflightReport{}, ErrCollision
	}
	projectRoot, err := inspectRealDirectory(request.ProjectSyncRoot)
	if err != nil {
		return PreflightReport{}, err
	}
	repositoryRoot, err := inspectRealDirectory(request.RepositoryRoot)
	if err != nil {
		return PreflightReport{}, err
	}
	worktreeBase, err := inspectRealDirectory(request.WorktreeBase)
	if err != nil {
		return PreflightReport{}, err
	}
	if !inside(repositoryRoot, projectRoot) || inside(worktreeBase, projectRoot) || inside(projectRoot, worktreeBase) || inside(worktreeBase, repositoryRoot) || inside(repositoryRoot, worktreeBase) {
		return PreflightReport{}, ErrUnsafeRoot
	}
	for current := filepath.Dir(repositoryRoot); inside(current, projectRoot); current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return PreflightReport{}, ErrUnsafeRoot
		}
		if current == projectRoot || filepath.Dir(current) == current {
			break
		}
	}
	gitMetadata := filepath.Join(repositoryRoot, ".git")
	gitInfo, err := os.Lstat(gitMetadata)
	if err != nil || gitInfo.Mode()&fs.ModeSymlink != 0 || !gitInfo.IsDir() {
		return PreflightReport{}, ErrUnsafeRoot
	}
	if request.AvailableDiskBytes < request.RequiredDiskBytes {
		return PreflightReport{}, ErrDiskLimit
	}
	target := filepath.Join(worktreeBase, workspaceDirectoryKey(request.WorkspaceID))
	if _, err := os.Lstat(target); err == nil {
		return PreflightReport{}, ErrCollision
	} else if !errors.Is(err, os.ErrNotExist) {
		return PreflightReport{}, ErrUnsafeRoot
	}
	return PreflightReport{
		WorkspaceID: request.WorkspaceID, BranchName: request.BranchName, Ready: true,
		Checks: []string{"branch_safe", "cleanup_owner_bound", "disk_available", "git_metadata_local", "repository_clean", "roots_separated", "symlinks_rejected", "target_available"},
	}, nil
}

func ValidateBranchName(name string) error {
	if len(name) < 1 || len(name) > 240 || name == "HEAD" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.Contains(name, "..") ||
		strings.Contains(name, "@{") || strings.Contains(name, "//") || strings.ContainsAny(name, " ~^:?*[\\") {
		return ErrBranchName
	}
	for _, value := range name {
		if value < 0x20 || value == 0x7f {
			return ErrBranchName
		}
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return ErrBranchName
		}
	}
	return nil
}

func ValidateCollectedPath(contract executioncontract.Contract, relativePath string) error {
	if err := project.ValidateProjectRelativePath(relativePath); err != nil || relativePath == ".git" || strings.HasPrefix(relativePath, ".git/") || strings.Contains(relativePath, "/.git/") || strings.HasSuffix(relativePath, "/.git") {
		return executioncontract.ErrPrivilegeEscalation
	}
	return executioncontract.AuthorizePath(contract, executioncontract.CapabilityWrite, relativePath)
}

type DeterministicFake struct {
	mu         sync.Mutex
	preflight  func(PreflightRequest) (PreflightReport, error)
	workspaces map[string]Workspace
	replays    map[string]allocationReplay
}

type allocationReplay struct {
	fingerprint string
	workspace   Workspace
}

func NewDeterministicFake(preflight func(PreflightRequest) (PreflightReport, error)) *DeterministicFake {
	if preflight == nil {
		preflight = Preflight
	}
	return &DeterministicFake{preflight: preflight, workspaces: map[string]Workspace{}, replays: map[string]allocationReplay{}}
}

func (fake *DeterministicFake) Allocate(ctx context.Context, request PreflightRequest) (Workspace, error) {
	if err := contextError(ctx); err != nil {
		return Workspace{}, err
	}
	report, err := fake.preflight(request)
	if err != nil || !report.Ready || report.WorkspaceID != request.WorkspaceID {
		if err == nil {
			err = ErrInvalidRequest
		}
		return Workspace{}, err
	}
	fingerprint := allocationFingerprint(request)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if replay, ok := fake.replays[request.IdempotencyKeyDigest]; ok {
		if replay.fingerprint != fingerprint {
			return Workspace{}, ErrCollision
		}
		return replay.workspace, nil
	}
	if existing, ok := fake.workspaces[request.WorkspaceID]; ok && existing.State != StateReleased {
		return Workspace{}, ErrCollision
	}
	workspace := Workspace{WorkspaceID: request.WorkspaceID, OwnerID: request.OwnerID, BranchName: request.BranchName, Generation: 1, State: StateAllocated, BaseCommit: request.BaseCommit, HeadCommit: request.BaseCommit}
	fake.workspaces[workspace.WorkspaceID] = workspace
	fake.replays[request.IdempotencyKeyDigest] = allocationReplay{fingerprint: fingerprint, workspace: workspace}
	return workspace, nil
}

func (fake *DeterministicFake) Inspect(ctx context.Context, workspaceID string) (Workspace, error) {
	if err := contextError(ctx); err != nil {
		return Workspace{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	workspace, ok := fake.workspaces[workspaceID]
	if !ok {
		return Workspace{}, ErrWorkspaceMissing
	}
	return workspace, nil
}

func (fake *DeterministicFake) Release(ctx context.Context, claim CleanupClaim) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	workspace, ok := fake.workspaces[claim.WorkspaceID]
	if !ok {
		return ErrWorkspaceMissing
	}
	if workspace.OwnerID != claim.OwnerID || workspace.Generation != claim.Generation || workspace.State != StateAllocated {
		return ErrOwnership
	}
	workspace.State = StateReleased
	workspace.Generation++
	fake.workspaces[claim.WorkspaceID] = workspace
	return nil
}

func inspectRealDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", ErrUnsafeRoot
	}
	clean := filepath.Clean(path)
	if err := rejectSymlinkComponents(clean); err != nil {
		return "", err
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return "", ErrUnsafeRoot
	}
	return clean, nil
}

func rejectSymlinkComponents(path string) error {
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	parts := strings.FieldsFunc(remainder, func(value rune) bool { return value == '/' || value == '\\' })
	current := volume + string(os.PathSeparator)
	if volume == "" {
		current = string(os.PathSeparator)
	}
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return ErrUnsafeRoot
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return ErrUnsafeRoot
		}
	}
	return nil
}

func inside(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		relative = strings.ToLower(relative)
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}

func workspaceDirectoryKey(workspaceID string) string {
	return hash([]byte(workspaceID))[:32]
}

func allocationFingerprint(request PreflightRequest) string {
	values := []string{request.WorkspaceID, request.OwnerID, request.AttemptID, filepath.Clean(request.ProjectSyncRoot), filepath.Clean(request.RepositoryRoot), filepath.Clean(request.WorktreeBase), request.BranchName, request.BaseCommit, fmtInt(request.RequiredDiskBytes), fmtInt(request.AvailableDiskBytes)}
	return hash([]byte(strings.Join(values, "\x00")))
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	return ctx.Err()
}

func namespaced(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && len(value) <= 128
}

func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func fmtInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
