package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"syncgate/internal/executioncontract"
)

func TestPreflightRejectsDirtyNestedCollisionDiskAndUnsafeBranches(t *testing.T) {
	request := workspaceFixture(t)
	report, err := Preflight(request)
	if err != nil || !report.Ready || report.WorkspaceID != request.WorkspaceID {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	raw, _ := json.Marshal(report)
	for _, path := range []string{request.ProjectSyncRoot, request.RepositoryRoot, request.WorktreeBase} {
		if strings.Contains(string(raw), path) {
			t.Fatalf("opaque report leaked root %q: %s", path, raw)
		}
	}

	dirty := request
	dirty.RepositoryDirty = true
	if _, err := Preflight(dirty); !errors.Is(err, ErrDirtyRepository) {
		t.Fatalf("dirty error=%v", err)
	}
	nested := request
	nested.WorktreeBase = request.ProjectSyncRoot
	if _, err := Preflight(nested); !errors.Is(err, ErrUnsafeRoot) {
		t.Fatalf("nested worktree error=%v", err)
	}
	lowDisk := request
	lowDisk.AvailableDiskBytes = lowDisk.RequiredDiskBytes - 1
	if _, err := Preflight(lowDisk); !errors.Is(err, ErrDiskLimit) {
		t.Fatalf("disk error=%v", err)
	}
	badBranch := request
	badBranch.BranchName = "codex/../escape"
	if _, err := Preflight(badBranch); !errors.Is(err, ErrBranchName) {
		t.Fatalf("branch error=%v", err)
	}
	existingBranch := request
	existingBranch.BranchExists = true
	if _, err := Preflight(existingBranch); !errors.Is(err, ErrCollision) {
		t.Fatalf("existing branch error=%v", err)
	}
	if err := os.Mkdir(filepath.Join(request.WorktreeBase, workspaceDirectoryKey(request.WorkspaceID)), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Preflight(request); !errors.Is(err, ErrCollision) {
		t.Fatalf("collision error=%v", err)
	}
}

func TestPreflightRejectsSymlinkRoots(t *testing.T) {
	request := workspaceFixture(t)
	link := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(request.RepositoryRoot, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	request.RepositoryRoot = link
	if _, err := Preflight(request); !errors.Is(err, ErrUnsafeRoot) {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestDeterministicFakeBindsCleanupOwnership(t *testing.T) {
	request := workspaceFixture(t)
	fake := NewDeterministicFake(nil)
	allocated, err := fake.Allocate(context.Background(), request)
	if err != nil || allocated.WorkspaceID != request.WorkspaceID || allocated.Generation != 1 {
		t.Fatalf("allocated=%+v err=%v", allocated, err)
	}
	if replay, err := fake.Allocate(context.Background(), request); err != nil || replay != allocated {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if err := fake.Release(context.Background(), CleanupClaim{WorkspaceID: allocated.WorkspaceID, OwnerID: "assignment:other", Generation: allocated.Generation}); !errors.Is(err, ErrOwnership) {
		t.Fatalf("ownership error=%v", err)
	}
	if err := fake.Release(context.Background(), CleanupClaim{WorkspaceID: allocated.WorkspaceID, OwnerID: allocated.OwnerID, Generation: allocated.Generation}); err != nil {
		t.Fatal(err)
	}
	if observed, err := fake.Inspect(context.Background(), allocated.WorkspaceID); err != nil || observed.State != StateReleased || observed.Generation != 2 {
		t.Fatalf("released=%+v err=%v", observed, err)
	}
}

func TestCollectedPathsExcludeGitAndContractForbiddenScopes(t *testing.T) {
	contract := executioncontract.Contract{Permissions: executioncontract.EffectivePermissions{
		Capabilities: []executioncontract.Capability{executioncontract.CapabilityWrite}, WritePaths: []string{"src"}, Forbidden: []string{"src/private"},
	}}
	if err := ValidateCollectedPath(contract, "src/main.go"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".git/config", "src/.git/hooks/post-commit", "src/private/key", "../escape"} {
		if err := ValidateCollectedPath(contract, path); !errors.Is(err, executioncontract.ErrPrivilegeEscalation) {
			t.Fatalf("path %s error=%v", path, err)
		}
	}
}

func workspaceFixture(t *testing.T) PreflightRequest {
	t.Helper()
	projectRoot := t.TempDir()
	repositoryRoot := filepath.Join(projectRoot, "workspace", "repo")
	if err := os.MkdirAll(filepath.Join(repositoryRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	worktreeBase := t.TempDir()
	return PreflightRequest{
		WorkspaceID: "workspace:one", OwnerID: "assignment:one", ProjectSyncRoot: projectRoot, RepositoryRoot: repositoryRoot,
		WorktreeBase: worktreeBase, BranchName: "codex/slice-6", RequiredDiskBytes: 1024, AvailableDiskBytes: 2048, IdempotencyKeyDigest: strings.Repeat("a", 64),
	}
}
