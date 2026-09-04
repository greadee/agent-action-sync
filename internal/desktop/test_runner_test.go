package desktop

import (
	"context"
	"strings"
	"testing"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/integrationgate"
	"syncgate/internal/project"
	"syncgate/internal/storage"
	"syncgate/internal/workspace"
)

type testRunnerControl struct{ snapshot storage.OrchestrationSnapshot }

func (control testRunnerControl) GetAssignment(_ context.Context, assignmentID string) (storage.OrchestrationSnapshot, error) {
	if assignmentID != control.snapshot.Assignment.AssignmentID {
		return storage.OrchestrationSnapshot{}, storage.ErrNotFound
	}
	return control.snapshot, nil
}

func TestLocalTestRunnerExecutesOnlyExactAuthorityPlan(t *testing.T) {
	_, _, projectRoot, _, _ := executionFixture(t)
	head := gitTestCommand(t, projectRoot, "rev-parse", "HEAD")
	worktreeBase := t.TempDir()
	worktrees, err := workspace.NewGitWorktreeManager(workspace.GitManagerConfig{
		ProjectSyncRoot: projectRoot, RepositoryRoot: projectRoot, WorktreeBase: worktreeBase, AllowedBaseCommits: []string{head},
	})
	if err != nil {
		t.Fatal(err)
	}
	branchName, err := workspace.AttemptBranchName("attempt:test-runner")
	if err != nil {
		t.Fatal(err)
	}
	allocated, err := worktrees.Allocate(context.Background(), workspace.PreflightRequest{
		WorkspaceID: "workspace:test-runner", OwnerID: "assignment:test-runner", AttemptID: "attempt:test-runner",
		ProjectSyncRoot: projectRoot, RepositoryRoot: projectRoot, WorktreeBase: worktreeBase, BranchName: branchName,
		BaseCommit: head, RequiredDiskBytes: 1, AvailableDiskBytes: 2, IdempotencyKeyDigest: strings.Repeat("1", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := localWorkspaceCheckPlan()
	contract := executioncontract.Contract{ContractID: "contract:test-runner", Version: 1, Digest: strings.Repeat("2", 64), Deadline: time.Now().Add(time.Minute), RequiredGates: []executioncontract.GateRequirement{{GateID: plan.GateID, Version: plan.GateVersion, Digest: plan.GateDigest}}}
	assignmentID := identitiesFromContract(contract.ContractID).assignmentID
	control := testRunnerControl{snapshot: storage.OrchestrationSnapshot{
		Assignment: storage.OrchestrationAssignment{AssignmentID: assignmentID, ContractID: contract.ContractID, ContractVersion: contract.Version, ContractDigest: contract.Digest},
		Resources:  &storage.OrchestrationResourceBinding{WorkspaceID: allocated.WorkspaceID},
	}}
	called := 0
	runner := LocalTestRunner{Control: control, Workspaces: worktrees, RunGit: func(_ context.Context, directory string, arguments ...string) (int64, int64, string, error) {
		called++
		if directory == "" || strings.Join(arguments, " ") != "-c core.hooksPath=NUL -c diff.external= diff --no-ext-diff --no-textconv --check HEAD --" {
			t.Fatalf("directory=%q arguments=%v", directory, arguments)
		}
		return 0, 17, strings.Repeat("3", 64), nil
	}}
	result, err := runner.RunAuthorized(context.Background(), contract, plan)
	if err != nil || result.Outcome != project.TestPassed || result.ExitCode != 0 || result.DurationMilliseconds != 17 || result.EvidenceID == "" || result.EvidenceDigest == "" || called != 1 {
		t.Fatalf("result=%+v called=%d err=%v", result, called, err)
	}
	changed := plan
	changed.CommandDigest = strings.Repeat("4", 64)
	if _, err := runner.RunAuthorized(context.Background(), contract, changed); err == nil || called != 1 {
		t.Fatalf("changed plan executed: called=%d err=%v", called, err)
	}
}

func TestConfiguredGateRequiresExactBuiltInDigest(t *testing.T) {
	for _, gateID := range []string{"gate:tests", "gate:human-review", "gate:security-review", "gate:data-review", "gate:operations-approval"} {
		gate := executioncontract.GateRequirement{GateID: gateID, Version: 1, Digest: builtInGateDigest(gateID)}
		if !configuredGate(gate) {
			t.Fatalf("built-in gate %s was unavailable", gateID)
		}
		gate.Digest = strings.Repeat("f", 64)
		if configuredGate(gate) {
			t.Fatalf("forged gate %s was configured", gateID)
		}
	}
	if configuredGate(executioncontract.GateRequirement{GateID: "gate:custom-review", Version: 1, Digest: strings.Repeat("a", 64)}) {
		t.Fatal("unknown review gate was configured")
	}
}

var _ integrationgate.TestRunner = LocalTestRunner{}
