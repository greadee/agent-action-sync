package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/integrationgate"
	"syncgate/internal/project"
	"syncgate/internal/workspace"
)

const maxGateOutputBytes = 1 << 20

type LocalTestRunner struct {
	Control    orchestrationSnapshotReader
	Workspaces *workspace.GitWorktreeManager
	RunGit     func(context.Context, string, ...string) (int64, int64, string, error)
}

func (runner LocalTestRunner) RunAuthorized(ctx context.Context, contract executioncontract.Contract, plan integrationgate.TestCommand) (integrationgate.TestResult, error) {
	expected := localWorkspaceCheckPlan()
	if ctx == nil || runner.Control == nil || runner.Workspaces == nil || plan != expected || !contractRequiresGate(contract, plan) {
		return integrationgate.TestResult{}, errors.New("test command is not authorized")
	}
	assignmentID := identitiesFromContract(contract.ContractID).assignmentID
	snapshot, err := runner.Control.GetAssignment(ctx, assignmentID)
	if err != nil || snapshot.Assignment.ContractID != contract.ContractID || snapshot.Assignment.ContractVersion != contract.Version || snapshot.Assignment.ContractDigest != contract.Digest || snapshot.Resources == nil {
		return integrationgate.TestResult{}, errors.New("test workspace authority is stale")
	}
	directory, err := runner.Workspaces.ResolveLocalPath(ctx, snapshot.Resources.WorkspaceID)
	if err != nil {
		return integrationgate.TestResult{}, err
	}
	runCtx := ctx
	cancel := func() {}
	if !contract.Deadline.IsZero() {
		runCtx, cancel = context.WithDeadline(ctx, contract.Deadline)
	}
	defer cancel()
	run := runner.RunGit
	if run == nil {
		run = runWorkspaceCheck
	}
	exit, duration, outputDigest, runErr := run(runCtx, directory,
		"-c", "core.hooksPath=NUL", "-c", "diff.external=", "diff", "--no-ext-diff", "--no-textconv", "--check", "HEAD", "--",
	)
	evidenceDigest := localHash("gate-evidence", plan.CommandDigest, fmt.Sprint(exit), fmt.Sprint(duration), outputDigest)
	result := integrationgate.TestResult{
		Outcome: project.TestPassed, ExitCode: exit, DurationMilliseconds: duration,
		EvidenceID: "test-evidence:" + evidenceDigest[:24], EvidenceDigest: evidenceDigest,
	}
	if runErr != nil || exit != 0 {
		result.Outcome = project.TestFailed
	}
	return result, runErr
}

func localWorkspaceCheckPlan() integrationgate.TestCommand {
	gateDigest := builtInGateDigest("gate:tests")
	return integrationgate.TestCommand{
		GateID: "gate:tests", GateVersion: 1, GateDigest: gateDigest,
		CommandID: "command:git-diff-check", CommandDigest: localHash("command:git-diff-check", "v1", "git diff --check HEAD --"),
	}
}

func localTestPlans() map[string]integrationgate.TestCommand {
	plan := localWorkspaceCheckPlan()
	return map[string]integrationgate.TestCommand{plan.GateID: plan}
}

func builtInGateDigest(id string) string {
	digest := sha256.Sum256([]byte(id + ":v1"))
	return hex.EncodeToString(digest[:])
}

func contractRequiresGate(contract executioncontract.Contract, plan integrationgate.TestCommand) bool {
	for _, gate := range contract.RequiredGates {
		if gate.GateID == plan.GateID && gate.Version == plan.GateVersion && gate.Digest == plan.GateDigest {
			return true
		}
	}
	return false
}

func runWorkspaceCheck(ctx context.Context, directory string, arguments ...string) (int64, int64, string, error) {
	executable, err := exec.LookPath("git")
	if err != nil {
		return -1, 0, "", errors.New("authorized Git executable is unavailable")
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return -1, 0, "", err
	}
	info, err := os.Lstat(executable)
	if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return -1, 0, "", errors.New("authorized Git executable is unsafe")
	}
	start := time.Now()
	command := exec.CommandContext(ctx, executable, append([]string{"-C", directory}, arguments...)...)
	command.Env = gateEnvironment()
	output := &boundedHashWriter{remaining: maxGateOutputBytes, hash: sha256.New()}
	command.Stdout, command.Stderr = output, output
	runErr := command.Run()
	duration := time.Since(start).Milliseconds()
	exit := int64(0)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exit = int64(exitErr.ExitCode())
			runErr = nil
		} else {
			exit = -1
		}
	}
	if output.exceeded {
		return -1, duration, hex.EncodeToString(output.hash.Sum(nil)), errors.New("authorized gate output exceeded its bound")
	}
	return exit, duration, hex.EncodeToString(output.hash.Sum(nil)), runErr
}

type boundedHashWriter struct {
	remaining int
	hash      hash.Hash
	exceeded  bool
}

func (writer *boundedHashWriter) Write(value []byte) (int, error) {
	if len(value) > writer.remaining {
		writer.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	written, err := writer.hash.Write(value)
	writer.remaining -= written
	return written, err
}

func gateEnvironment() []string {
	keys := []string{"COMSPEC", "PATH", "PATHEXT", "SYSTEMROOT", "TEMP", "TMP", "WINDIR"}
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && !strings.ContainsRune(value, '\x00') {
			result = append(result, key+"="+value)
		}
	}
	return result
}

var _ integrationgate.TestRunner = LocalTestRunner{}
