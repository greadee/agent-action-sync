package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
	"time"

	"syncgate/internal/computenode"
	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/storage"
)

func TestSelectUsesStableFactsAndConfiguredPreference(t *testing.T) {
	request := selectionFixture(t)
	result, err := Select(context.Background(), request)
	if err != nil || result.Blocked || result.Selected == nil {
		t.Fatalf("Select = %#v, err=%v", result, err)
	}
	if result.Selected.WorkerID != "worker:beta" || result.Selected.Provider != "provider-b" || result.Selected.Model != "model-b" {
		t.Fatalf("selected candidate = %#v", result.Selected)
	}
	if result.AssignmentAuditReason != "deterministic_selection" || len(result.Explanations) != 2 {
		t.Fatalf("selection explanation = %#v", result)
	}
	for _, explanation := range result.Explanations {
		if !explanation.Accepted || len(explanation.Reasons) != 0 {
			t.Fatalf("eligible candidate explanation = %#v", explanation)
		}
	}

	reversed := request
	reversed.Candidates = []Candidate{request.Candidates[1], request.Candidates[0]}
	again, err := Select(context.Background(), reversed)
	if err != nil || !reflect.DeepEqual(result, again) {
		t.Fatalf("selection must be deterministic: first=%#v second=%#v err=%v", result, again, err)
	}
}

func TestSelectFailsClosedAtEachPolicyLayer(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SelectionRequest)
		want   string
	}{
		{
			name: "work package authority",
			mutate: func(request *SelectionRequest) {
				request.Node.DefinitionHash = selectionDigest("other-work-package")
			},
			want: "work_package_authority_mismatch",
		},
		{
			name: "required trade capability",
			mutate: func(request *SelectionRequest) {
				request.Candidates[0].Worker.CapabilityTags = []string{"cap:go"}
				request.Candidates = request.Candidates[:1]
			},
			want: "missing_capability:cap:test",
		},
		{
			name: "runtime unavailable",
			mutate: func(request *SelectionRequest) {
				request.Candidates[0].RuntimeAvailable = false
				request.Candidates = request.Candidates[:1]
			},
			want: "runtime_unavailable",
		},
		{
			name: "offline node",
			mutate: func(request *SelectionRequest) {
				request.Candidates[0].Observation.Health = computenode.HealthUnhealthy
				request.Candidates = request.Candidates[:1]
			},
			want: "node_health_unavailable",
		},
		{
			name: "permission denied",
			mutate: func(request *SelectionRequest) {
				request.Policy.AllowedCapabilities = []executioncontract.Capability{executioncontract.CapabilityInspect}
				request.Candidates = request.Candidates[:1]
			},
			want: "capability_denied_by_policy:write",
		},
		{
			name: "missing gate evidence",
			mutate: func(request *SelectionRequest) {
				request.Policy.GateEvidence = nil
				request.Candidates = request.Candidates[:1]
			},
			want: "missing_gate_evidence:gate:tests",
		},
		{
			name: "risk limit",
			mutate: func(request *SelectionRequest) {
				request.Risk = RiskCritical
				request.Candidates = request.Candidates[:1]
			},
			want: "risk_limit_exceeded",
		},
		{
			name: "budget limit",
			mutate: func(request *SelectionRequest) {
				request.Policy.MaximumBudget.MaxTokens = request.RequestedBudget.MaxTokens - 1
				request.Candidates = request.Candidates[:1]
			},
			want: "budget_tokens_exceeded",
		},
		{
			name: "concurrency limit",
			mutate: func(request *SelectionRequest) {
				request.Policy.ActiveAssignments = request.Policy.MaximumBudget.MaxConcurrentWorker
				request.Candidates = request.Candidates[:1]
			},
			want: "concurrency_limit_reached",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := selectionFixture(t)
			test.mutate(&request)
			result, err := Select(context.Background(), request)
			if err != nil || !result.Blocked || !selectionContains(result.BlockReasons, test.want) {
				t.Fatalf("Select = %#v, err=%v, want %q", result, err, test.want)
			}
		})
	}
}

func TestSelectOperatorOverrideRequiresAnEligibleExplicitCandidate(t *testing.T) {
	request := selectionFixture(t)
	request.Override = &OperatorOverride{WorkerID: "worker:alpha", NodeID: "node:one", ActorID: "operator:one", ReasonCode: "operator_preference"}
	result, err := Select(context.Background(), request)
	if err != nil || result.Blocked || result.Selected == nil || result.Selected.WorkerID != "worker:alpha" || result.Override == nil || result.AssignmentAuditReason != "operator_override" {
		t.Fatalf("operator override = %#v, err=%v", result, err)
	}
	plan := &PlanRequest{WorkerID: result.Selected.WorkerID, NodeID: result.Selected.NodeID}
	if err := ApplySelectionToPlan(result, plan); err != nil || plan.AssignmentReason != "operator_override" || plan.ActorID != request.Override.ActorID {
		t.Fatalf("ApplySelectionToPlan = %#v, err=%v", plan, err)
	}
	plan.ActorID = "operator:other"
	if err := ApplySelectionToPlan(result, plan); err == nil {
		t.Fatal("mismatched override actor must not be planned")
	}
	plan.ActorID = request.Override.ActorID
	plan.NodeID = "node:other"
	if err := ApplySelectionToPlan(result, plan); err == nil {
		t.Fatal("mismatched selected node must not be planned")
	}
	request.Override.WorkerID = "worker:unknown"
	result, err = Select(context.Background(), request)
	if err != nil || !result.Blocked || !selectionContains(result.BlockReasons, "operator_override_not_eligible") {
		t.Fatalf("ineligible override = %#v, err=%v", result, err)
	}
}

func TestSelectRejectsInvalidOrUnboundedPolicyInput(t *testing.T) {
	request := selectionFixture(t)
	request.Policy.MaximumBudget.MaxConcurrentWorker = 3
	if _, err := Select(context.Background(), request); err != ErrInvalidSelection {
		t.Fatalf("unbounded concurrency error = %v", err)
	}
	request = selectionFixture(t)
	request.RequiredRuntime = []executioncontract.Capability{"unknown"}
	if _, err := Select(context.Background(), request); err != ErrInvalidSelection {
		t.Fatalf("unknown capability error = %v", err)
	}
}

func selectionFixture(t *testing.T) SelectionRequest {
	t.Helper()
	now := time.Date(2026, time.August, 22, 9, 0, 0, 0, time.UTC)
	tradeReference := project.RegistryReference{ID: "trade:go", Version: 1, Digest: selectionDigest("trade-go")}
	runtime := executioncontract.BindingReference{ID: "runtime:local", Version: 1, Digest: selectionDigest("runtime-local")}
	node, err := computenode.BuildDefinition(computenode.Definition{
		NodeID: "node:one", Version: 1, Lifecycle: computenode.LifecycleActive, OS: "windows", Architecture: "amd64",
		Capacity: computenode.Capacity{CPUMillis: 4000, MemoryBytes: 8 << 30, DiskBytes: 100 << 30}, RepositoryAccess: computenode.RepositoryWrite,
		Runtimes: []executioncontract.BindingReference{runtime}, MaxActiveLeases: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation := computenode.Observation{
		Node: executioncontract.BindingReference{ID: node.NodeID, Version: node.Version, Digest: node.DefinitionDigest}, Health: computenode.HealthHealthy,
		Available: node.Capacity, AvailableRuntimes: []executioncontract.BindingReference{runtime}, ActiveLeases: 0,
		ObservedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
	}
	trade := storage.TradeDefinition{TradeID: tradeReference.ID, Version: tradeReference.Version, Lifecycle: storage.RegistryActive, ContentHash: tradeReference.Digest, RequiredCapabilities: []string{"cap:go", "cap:test"}}
	worker := func(id, provider, model string) storage.WorkerProfile {
		return storage.WorkerProfile{WorkerID: id, Version: 1, Lifecycle: storage.RegistryActive, TradeID: trade.TradeID, TradeVersion: trade.Version,
			RuntimeID: runtime.ID, RuntimeVersion: runtime.Version, Provider: provider, Model: model, ModelVersion: "2026-08", CapabilityTags: []string{"cap:go", "cap:test"}}
	}
	workPackageDigest := selectionDigest("work-package")
	workPackage := project.WorkPackageDefinition{RecordHeader: project.RecordHeader{RecordID: "record:work-one", ProjectID: "project:one"}, WorkPackageID: "work:one", TaskID: "task:one", TaskRevision: 1, GraphRevision: 1, TradeReference: &tradeReference}
	return SelectionRequest{
		Task:        storage.ProjectTaskProjection{ProjectID: workPackage.ProjectID, TaskID: workPackage.TaskID, TaskRevision: workPackage.TaskRevision, GraphRevision: workPackage.GraphRevision, State: string(ReadinessReady)},
		Node:        storage.ProjectTaskNodeProjection{ProjectID: workPackage.ProjectID, TaskID: workPackage.TaskID, TaskRevision: workPackage.TaskRevision, GraphRevision: workPackage.GraphRevision, WorkPackageID: workPackage.WorkPackageID, DefinitionRecordID: workPackage.RecordID, DefinitionHash: workPackageDigest, Readiness: string(ReadinessReady)},
		WorkPackage: workPackage, WorkPackageDigest: workPackageDigest, Risk: RiskModerate,
		RequestedBudget: DispatchBudget{MaxTokens: 1000, MaxCostMicros: 2000, MaxWallClock: 10 * time.Minute, MaxConcurrentWorker: 1},
		RequiredRuntime: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite},
		RequiredGates:   []executioncontract.GateRequirement{{GateID: "gate:tests", Version: 1, Digest: selectionDigest("gate-tests")}},
		Policy: SelectionPolicy{AllowedCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite}, MaximumRisk: RiskHigh,
			MaximumBudget:           DispatchBudget{MaxTokens: 2000, MaxCostMicros: 4000, MaxWallClock: 20 * time.Minute, MaxConcurrentWorker: 2},
			GateEvidence:            []GateEvidence{{GateID: "gate:tests", Version: 1, Digest: selectionDigest("gate-tests"), EvidenceID: "evidence:tests", EvidenceDigest: selectionDigest("evidence-tests")}},
			PreferredWorkerNodePair: []WorkerNodePreference{{WorkerID: "worker:beta", NodeID: node.NodeID}}},
		Candidates: []Candidate{
			{Worker: worker("worker:alpha", "provider-a", "model-a"), Trade: trade, Runtime: runtime, RuntimeAvailable: true, RuntimeCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite}, Node: node, Observation: observation},
			{Worker: worker("worker:beta", "provider-b", "model-b"), Trade: trade, Runtime: runtime, RuntimeAvailable: true, RuntimeCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite}, Node: node, Observation: observation},
		},
		Now: now,
	}
}

func selectionDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func selectionContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
