package dispatchbinding

import (
	"strings"
	"testing"
	"time"

	"syncgate/internal/contextcompiler"
	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/storage"
)

func TestAttemptBindingCapturesImmutableAuthorityAndRejectsTampering(t *testing.T) {
	when := time.Date(2026, time.August, 22, 15, 0, 0, 0, time.UTC)
	digest := func(seed string) string { return strings.Repeat(seed, 64) }
	trade := project.RegistryReference{ID: "trade:go", Version: 1, Digest: digest("a")}
	worker := project.RegistryReference{ID: "worker:go", Version: 1, Digest: digest("b")}
	taskHeader := project.NewRecordHeader(project.RecordTaskRevision, "record-task-binding", "project-binding")
	taskHeader.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: digest("c")}
	task := project.TaskRevision{RecordHeader: taskHeader, TaskID: "task:binding", Revision: 1, GraphRevision: 1}
	workHeader := project.NewRecordHeader(project.RecordWorkPackage, "record-work-binding", "project-binding")
	workHeader.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: digest("d")}
	work := project.WorkPackageDefinition{RecordHeader: workHeader, WorkPackageID: "wp-binding", TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1, TradeReference: &trade, Scope: project.WorkScope{Allowed: []string{"src"}, Inspect: []string{"docs"}}, Deliverables: []string{"binary"}, AcceptanceCriteria: []string{"tests pass"}}
	graphHeader := project.NewRecordHeader(project.RecordDependencyGraph, "record-graph-binding", "project-binding")
	graphHeader.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: digest("e")}
	graph := project.DependencyGraphRevision{RecordHeader: graphHeader, TaskID: task.TaskID, TaskRevision: 1, Revision: 1, Members: []project.DependencyGraphMember{{WorkPackageID: work.WorkPackageID, DefinitionRecordID: work.RecordID, DefinitionDigest: work.Integrity.Digest}}}
	budget := executioncontract.BudgetLimits{MaxTokens: 100, MaxCostMicros: 1_000, MaxWallClockSeconds: 60, MaxRetries: 2, MaxToolCalls: 10, MaxConcurrentWorkers: 1}
	layers := make([]executioncontract.PolicyLayer, 0, 7)
	for _, name := range []string{"project", "runtime", "task", "trade", "user", "work_package", "worker"} {
		layers = append(layers, executioncontract.PolicyLayer{Name: name, AllowedCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite, executioncontract.CapabilityTest}, InspectPaths: []string{"docs", "src"}, WritePaths: []string{"src"}, BudgetCeiling: budget})
	}
	ref := func(id, seed string) executioncontract.BindingReference {
		return executioncontract.BindingReference{ID: id, Version: 1, Digest: digest(seed)}
	}
	contract, _, err := executioncontract.Build(executioncontract.BuildRequest{ContractID: "contract:binding", Version: 1, Task: task, TaskDigest: task.Integrity.Digest, Graph: graph, GraphDigest: graph.Integrity.Digest, WorkPackage: work, WorkPackageDigest: work.Integrity.Digest, ExecutionID: "execution:binding", ProjectRevision: digest("f"), Trade: trade, Worker: worker, WorkerProfile: storage.WorkerProfile{WorkerID: worker.ID, Version: 1, Lifecycle: storage.RegistryActive, TradeID: trade.ID, TradeVersion: 1, ContentHash: worker.Digest}, Instruction: ref("instruction:go", "1"), ContextDigest: digest("2"), Runtime: ref("runtime:local", "3"), Provider: ref("provider:local", "4"), Model: ref("model:test", "5"), Node: ref("node:local", "6"), RequestedCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite, executioncontract.CapabilityTest}, PolicyLayers: layers, RequestedBudget: budget, CreatedAt: when, NotBefore: when.Add(time.Second), Deadline: when.Add(31 * time.Second), CreatedBy: "actor:scheduler"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := buildAttemptBinding("attempt-binding", contract, contextcompiler.Manifest{ContextDigest: contract.ContextDigest, Sources: []contextcompiler.SourceIndexEntry{{Digest: digest("7")}}, Omissions: []contextcompiler.Notice{{Code: "secret_content"}}}, when)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAttemptBinding(binding)
	if err != nil || decoded.Contract.Digest != contract.Digest || decoded.ContextDigest != contract.ContextDigest || decoded.OutputSchema != "syncgate.result-envelope.v1" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if strings.Contains(string(binding.BindingJSON), "secret-value") {
		t.Fatal("binding stored source content")
	}
	binding.BindingJSON[0] ^= 1
	if _, err := DecodeAttemptBinding(binding); err == nil {
		t.Fatal("tampered binding was accepted")
	}
}
