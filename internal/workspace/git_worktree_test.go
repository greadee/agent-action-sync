package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"syncgate/internal/executioncontract"
)

func TestGitWorktreeProvisioningRestartManifestAndCleanup(t *testing.T) {
	fixture := newGitFixture(t)
	manager, err := NewGitWorktreeManager(fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	request := fixture.request(t, "attempt:one", "workspace:one", "assignment:one")
	primaryHead := gitTest(t, fixture.repository, "rev-parse", "HEAD")
	allocated, err := manager.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if allocated.BaseCommit != primaryHead || allocated.HeadCommit != primaryHead || allocated.BranchName != request.BranchName {
		t.Fatalf("workspace=%+v", allocated)
	}
	public, _ := json.Marshal(allocated)
	if strings.Contains(string(public), fixture.repository) || strings.Contains(string(public), fixture.config.WorktreeBase) {
		t.Fatalf("workspace leaked absolute path: %s", public)
	}
	if replay, err := manager.Allocate(context.Background(), request); err != nil || replay != allocated {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if status := gitTest(t, fixture.repository, "status", "--porcelain"); status != "" {
		t.Fatalf("primary worktree changed: %q", status)
	}

	restarted, _ := NewGitWorktreeManager(fixture.config)
	if observed, err := restarted.Inspect(context.Background(), request.WorkspaceID); err != nil || observed != allocated {
		t.Fatalf("restart=%+v err=%v", observed, err)
	}
	worktree := restarted.target(request.WorkspaceID)
	if localPath, err := restarted.ResolveLocalPath(context.Background(), request.WorkspaceID); err != nil || localPath != worktree {
		t.Fatalf("runtime path=%q err=%v", localPath, err)
	}
	writeGitFile(t, worktree, "src/main.go", "package main\n// changed\n")
	gitTest(t, worktree, "add", "src/main.go")
	gitTest(t, worktree, "commit", "-m", "update source")
	contract := executioncontract.Contract{Permissions: executioncontract.EffectivePermissions{Capabilities: []executioncontract.Capability{executioncontract.CapabilityWrite}, WritePaths: []string{"src"}}}
	manifest, err := restarted.InspectChanges(context.Background(), request.WorkspaceID, contract)
	if err != nil || len(manifest.Files) != 1 || manifest.Files[0].RelativePath != "src/main.go" || manifest.BaseCommit != primaryHead || manifest.HeadCommit == primaryHead || manifest.Digest == "" {
		t.Fatalf("manifest=%+v err=%v", manifest, err)
	}
	preview, err := restarted.PreviewIntegration(context.Background(), request.WorkspaceID, primaryHead)
	if err != nil || preview.StaleBase || preview.HasConflicts || preview.HeadCommit != manifest.HeadCommit || preview.CurrentCommit != primaryHead || preview.Digest == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if got := gitTest(t, fixture.repository, "rev-parse", "HEAD"); got != primaryHead {
		t.Fatalf("preview mutated primary head: %s", got)
	}
	if err := restarted.Release(context.Background(), CleanupClaim{WorkspaceID: request.WorkspaceID, OwnerID: request.OwnerID, Generation: allocated.Generation}); err != nil {
		t.Fatal(err)
	}
	if observed, err := restarted.Inspect(context.Background(), request.WorkspaceID); err != nil || observed.State != StateReleased || observed.Generation != 2 {
		t.Fatalf("released=%+v err=%v", observed, err)
	}
	if _, err := restarted.ResolveLocalPath(context.Background(), request.WorkspaceID); !errors.Is(err, ErrWorkspaceMissing) {
		t.Fatalf("released runtime path error=%v", err)
	}
	if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released worktree remains: %v", err)
	}

	collision := fixture.request(t, "attempt:one", "workspace:two", "assignment:two")
	if _, err := restarted.Allocate(context.Background(), collision); !errors.Is(err, ErrCollision) {
		t.Fatalf("branch collision error=%v", err)
	}
}

func TestGitWorktreeManifestMarksBinaryAndPreviewDetectsStaleBase(t *testing.T) {
	fixture := newGitFixture(t)
	manager, _ := NewGitWorktreeManager(fixture.config)
	request := fixture.request(t, "attempt:binary", "workspace:binary", "assignment:binary")
	if _, err := manager.Allocate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	worktree := manager.target(request.WorkspaceID)
	if err := os.WriteFile(filepath.Join(worktree, "src", "payload.bin"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	contract := executioncontract.Contract{Permissions: executioncontract.EffectivePermissions{Capabilities: []executioncontract.Capability{executioncontract.CapabilityWrite}, WritePaths: []string{"src"}}}
	manifest, err := manager.InspectChanges(context.Background(), request.WorkspaceID, contract)
	if err != nil || len(manifest.Files) != 1 || !manifest.Files[0].Binary {
		t.Fatalf("binary manifest=%+v err=%v", manifest, err)
	}
	writeGitFile(t, fixture.repository, "src/primary.go", "package main\n")
	gitTest(t, fixture.repository, "add", "src/primary.go")
	gitTest(t, fixture.repository, "commit", "-m", "advance primary")
	preview, err := manager.PreviewIntegration(context.Background(), request.WorkspaceID, request.BaseCommit)
	if err != nil || !preview.StaleBase {
		t.Fatalf("stale preview=%+v err=%v", preview, err)
	}
}

func TestGitWorktreeManifestRejectsSymlinkAndOversizedFile(t *testing.T) {
	fixture := newGitFixture(t)
	manager, _ := NewGitWorktreeManager(fixture.config)
	request := fixture.request(t, "attempt:unsafe-output", "workspace:unsafe-output", "assignment:unsafe-output")
	if _, err := manager.Allocate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	worktree := manager.target(request.WorkspaceID)
	contract := executioncontract.Contract{Permissions: executioncontract.EffectivePermissions{Capabilities: []executioncontract.Capability{executioncontract.CapabilityWrite}, WritePaths: []string{"src"}}}
	target := filepath.Join(worktree, "src", "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktree, "src", "link.txt")
	if err := os.Symlink(target, link); err == nil {
		if _, err := manager.InspectChanges(context.Background(), request.WorkspaceID, contract); !errors.Is(err, executioncontract.ErrPrivilegeEscalation) {
			t.Fatalf("symlink output error=%v", err)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Logf("symlink creation unavailable: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	largePath := filepath.Join(worktree, "src", "large.bin")
	large, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(MaxChangedFileBytes + 1); err != nil {
		_ = large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InspectChanges(context.Background(), request.WorkspaceID, contract); !errors.Is(err, ErrChangeLimit) {
		t.Fatalf("oversized output error=%v", err)
	}
}

func TestGitWorktreeRequiresAllowlistedBaseAndRejectsNestedRepository(t *testing.T) {
	fixture := newGitFixture(t)
	bad := fixture.config
	bad.AllowedBaseCommits = []string{strings.Repeat("f", 40)}
	manager, err := NewGitWorktreeManager(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Allocate(context.Background(), fixture.request(t, "attempt:base", "workspace:base", "assignment:base")); !errors.Is(err, ErrBaseCommit) {
		t.Fatalf("base error=%v", err)
	}

	gitTest(t, fixture.projectRoot, "init")
	request := fixture.request(t, "attempt:nested", "workspace:nested", "assignment:nested")
	manager, _ = NewGitWorktreeManager(fixture.config)
	if _, err := manager.Allocate(context.Background(), request); !errors.Is(err, ErrUnsafeRoot) {
		t.Fatalf("nested repository error=%v", err)
	}
}

func TestGitWorktreeRejectsDirtyPrimaryAndQuarantinesDirtyOrStaleState(t *testing.T) {
	fixture := newGitFixture(t)
	manager, _ := NewGitWorktreeManager(fixture.config)
	request := fixture.request(t, "attempt:dirty", "workspace:dirty", "assignment:dirty")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Allocate(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	writeGitFile(t, fixture.repository, "untracked.txt", "dirty")
	if _, err := manager.Allocate(context.Background(), request); !errors.Is(err, ErrDirtyRepository) {
		t.Fatalf("dirty primary error=%v", err)
	}
	if err := os.Remove(filepath.Join(fixture.repository, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	allocated, err := manager.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeGitFile(t, manager.target(request.WorkspaceID), "src/dirty.go", "package dirty")
	if err := manager.Release(context.Background(), CleanupClaim{WorkspaceID: request.WorkspaceID, OwnerID: request.OwnerID, Generation: allocated.Generation}); !errors.Is(err, ErrCleanupRequired) {
		t.Fatalf("dirty cleanup error=%v", err)
	}
	if observed, err := manager.Inspect(context.Background(), request.WorkspaceID); err != nil || observed.State != StateQuarantined {
		t.Fatalf("quarantine=%+v err=%v", observed, err)
	}

	stale := fixture.request(t, "attempt:stale", "workspace:stale", "assignment:stale")
	staleAllocated, err := manager.Allocate(context.Background(), stale)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.repository, "worktree", "remove", manager.target(stale.WorkspaceID))
	restarted, _ := NewGitWorktreeManager(fixture.config)
	observed, err := restarted.Inspect(context.Background(), stale.WorkspaceID)
	if err != nil || observed.State != StateQuarantined || observed.Generation != staleAllocated.Generation+1 {
		t.Fatalf("stale=%+v err=%v", observed, err)
	}
}

func TestGitWorktreeManifestRejectsOutOfScopeAndGitMetadata(t *testing.T) {
	fixture := newGitFixture(t)
	manager, _ := NewGitWorktreeManager(fixture.config)
	request := fixture.request(t, "attempt:scope", "workspace:scope", "assignment:scope")
	if _, err := manager.Allocate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	worktree := manager.target(request.WorkspaceID)
	writeGitFile(t, worktree, "docs/out.txt", "outside")
	contract := executioncontract.Contract{Permissions: executioncontract.EffectivePermissions{Capabilities: []executioncontract.Capability{executioncontract.CapabilityWrite}, WritePaths: []string{"src"}}}
	if _, err := manager.InspectChanges(context.Background(), request.WorkspaceID, contract); !errors.Is(err, executioncontract.ErrPrivilegeEscalation) {
		t.Fatalf("scope error=%v", err)
	}
}

type gitFixture struct {
	projectRoot, repository string
	config                  GitManagerConfig
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	projectRoot := t.TempDir()
	repository := filepath.Join(projectRoot, "repo")
	worktreeBase := t.TempDir()
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "init")
	gitTest(t, repository, "config", "user.email", "syncgate@example.invalid")
	gitTest(t, repository, "config", "user.name", "SyncGate Test")
	writeGitFile(t, repository, "src/main.go", "package main\n")
	gitTest(t, repository, "add", "src/main.go")
	gitTest(t, repository, "commit", "-m", "initial")
	base := gitTest(t, repository, "rev-parse", "HEAD")
	return gitFixture{projectRoot: projectRoot, repository: repository, config: GitManagerConfig{ProjectSyncRoot: projectRoot, RepositoryRoot: repository, WorktreeBase: worktreeBase, AllowedBaseCommits: []string{base}}}
}
func (fixture gitFixture) request(t *testing.T, attemptID, workspaceID, ownerID string) PreflightRequest {
	t.Helper()
	branch, err := AttemptBranchName(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	return PreflightRequest{WorkspaceID: workspaceID, OwnerID: ownerID, AttemptID: attemptID, ProjectSyncRoot: fixture.projectRoot, RepositoryRoot: fixture.repository, WorktreeBase: fixture.config.WorktreeBase, BranchName: branch, BaseCommit: gitTest(t, fixture.repository, "rev-parse", "HEAD"), RequiredDiskBytes: 1, AvailableDiskBytes: 1 << 20, IdempotencyKeyDigest: strings.Repeat("a", 64)}
}
func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
func writeGitFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
