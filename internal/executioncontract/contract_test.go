package executioncontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"syncgate/internal/project"
	"syncgate/internal/storage"
)

func TestBuildIsReproducibleAndIntersectsPolicy(t *testing.T) {
	request := contractFixture()
	request.WorkPackage.ReviewRequired = true
	request.PolicyLayers[3].WritePaths = []string{"src/pkg", "src/pkg"}
	first, raw, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}

	reversed := request
	reversed.PolicyLayers = reverseLayers(request.PolicyLayers)
	reversed.RequestedCapabilities = []Capability{CapabilityTest, CapabilityWrite, CapabilityInspect}
	second, secondRaw, err := Build(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw, secondRaw) || first.Digest != second.Digest {
		t.Fatal("equal policy inputs did not produce byte-equivalent contracts")
	}
	if !reflect.DeepEqual(first.Permissions.WritePaths, []string{"src/pkg"}) {
		t.Fatalf("write intersection=%v", first.Permissions.WritePaths)
	}
	if !first.ReviewRequired {
		t.Fatal("review requirement was not bound into the immutable contract")
	}

	unsigned := first
	unsigned.Digest = ""
	unsignedJSON, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(unsignedJSON)
	if first.Digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("contract digest=%s", first.Digest)
	}
	if err := VerifyDigest(first); err != nil {
		t.Fatal(err)
	}
	tampered := first
	tampered.Budget.MaxTokens--
	if err := VerifyDigest(tampered); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("tampered contract error=%v", err)
	}
	if first.Budget.MaxTokens != 50 || first.Budget.MaxToolCalls != 10 {
		t.Fatalf("effective budget=%+v", first.Budget)
	}
}

func TestPolicyPathsFailClosedWithForbiddenPrecedenceAndScopeEscape(t *testing.T) {
	contract := mustBuild(t, contractFixture())
	for _, allowed := range []string{"src/main.go", "docs/design.md"} {
		if err := AuthorizePath(contract, CapabilityInspect, allowed); err != nil {
			t.Fatalf("inspect %s: %v", allowed, err)
		}
	}
	if err := AuthorizePath(contract, CapabilityWrite, "src/main.go"); err != nil {
		t.Fatalf("write allowed path: %v", err)
	}
	for _, denied := range []string{"src/blocked/key.txt", "docs/design.md", "../escape", "src2/file.go"} {
		if err := AuthorizePath(contract, CapabilityWrite, denied); !errors.Is(err, ErrPrivilegeEscalation) {
			t.Fatalf("write %s error=%v", denied, err)
		}
	}

	request := contractFixture()
	for index := range request.PolicyLayers {
		if request.PolicyLayers[index].Name == "user" {
			request.PolicyLayers[index].WritePaths = []string{"outside"}
		}
	}
	if _, _, err := Build(request); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("empty scope intersection error=%v", err)
	}
}

func TestPolicyDeniesSecretsUnknownCapabilitiesAndPrivilegeEscalation(t *testing.T) {
	missingLayerRequest := contractFixture()
	missingLayerRequest.PolicyLayers = missingLayerRequest.PolicyLayers[1:]
	if _, _, err := Build(missingLayerRequest); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("missing layer error=%v", err)
	}

	secretRequest := contractFixture()
	secretRequest.RequestedCapabilities = append(secretRequest.RequestedCapabilities, CapabilitySecret)
	secretRequest.RequestedSecretIDs = []string{"secret:deploy-key"}
	secretRequest.PolicyLayers[0].AllowedSecretIDs = nil
	if _, _, err := Build(secretRequest); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("secret denial error=%v", err)
	}

	unknownRequest := contractFixture()
	unknownRequest.RequestedCapabilities = append(unknownRequest.RequestedCapabilities, Capability("telepathy"))
	if _, _, err := Build(unknownRequest); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("unknown capability error=%v", err)
	}

	escalationRequest := contractFixture()
	escalationRequest.RequestedCapabilities = append(escalationRequest.RequestedCapabilities, CapabilityShell)
	for index := range escalationRequest.PolicyLayers {
		if escalationRequest.PolicyLayers[index].Name == "user" {
			escalationRequest.PolicyLayers[index].DeniedCapabilities = []Capability{CapabilityShell}
		}
	}
	if _, _, err := Build(escalationRequest); !errors.Is(err, ErrPrivilegeEscalation) {
		t.Fatalf("capability escalation error=%v", err)
	}
}

func TestHighRiskCompletionRequiresExactGatesEvenAfterWorkerSuccess(t *testing.T) {
	request := contractFixture()
	request.Task.Risk = request.WorkPackage.Risk
	request.WorkPackage.Risk = nil
	contract := mustBuild(t, request)
	decision := EvaluateCompletion(contract, nil, true)
	if decision.Accepted || decision.Code != "required_gates_missing" || len(decision.MissingGates) < 2 {
		t.Fatalf("ungated success decision=%+v", decision)
	}
	evidence := make([]GateEvidence, 0, len(contract.RequiredGates))
	for _, gate := range contract.RequiredGates {
		evidence = append(evidence, GateEvidence{GateID: gate.GateID, Version: gate.Version, Digest: gate.Digest, Satisfied: true})
	}
	decision = EvaluateCompletion(contract, evidence, true)
	if !decision.Accepted || decision.Code != "gates_satisfied" {
		t.Fatalf("gated decision=%+v", decision)
	}

	request = contractFixture()
	request.WorkPackage.QualityGates = append(request.WorkPackage.QualityGates, project.QualityGateReference{
		GateID: "gate:security-review", Version: 1, Digest: strings.Repeat("f", 64), Required: true,
	})
	if _, _, err := Build(request); !errors.Is(err, ErrRequiredGates) {
		t.Fatalf("built-in gate identity conflict error=%v", err)
	}
}

func TestRuntimeDecisionsAreDeterministic(t *testing.T) {
	contract := mustBuild(t, contractFixture())
	tests := []struct {
		name string
		use  Usage
		want RuntimeDecision
	}{
		{name: "canceled", use: Usage{Canceled: true}, want: RuntimeDecision{State: "canceled", Code: "canceled_by_authority", Terminal: true}},
		{name: "token budget", use: Usage{Tokens: contract.Budget.MaxTokens + 1}, want: RuntimeDecision{State: "budget_exhausted", Code: "token_budget_exhausted", Terminal: true}},
		{name: "retry budget", use: Usage{Retries: contract.Budget.MaxRetries + 1}, want: RuntimeDecision{State: "failed", Code: "retry_budget_exhausted", Terminal: true}},
		{name: "recoverable failure", use: Usage{Failure: true}, want: RuntimeDecision{State: "retry_wait", Code: "recoverable_failure", Recoverable: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EvaluateUsage(contract, test.use); got != test.want {
				t.Fatalf("decision=%+v want=%+v", got, test.want)
			}
		})
	}
}

func TestServicePersistsImmutableVersionsAndRejectsIdentityChanges(t *testing.T) {
	store := &memoryContractStore{records: map[string]storage.ExecutionContractRecord{}}
	service := Service{Store: store}
	request := contractFixture()
	first, result, err := service.Create(context.Background(), request)
	if err != nil || result.AlreadyPresent {
		t.Fatalf("create v1 result=%+v err=%v", result, err)
	}
	_, result, err = service.Create(context.Background(), request)
	if err != nil || !result.AlreadyPresent {
		t.Fatalf("replay v1 result=%+v err=%v", result, err)
	}
	mutation := contractFixture()
	mutation.RequestedBudget.MaxCostMicros = 900
	if _, _, err := service.Create(context.Background(), mutation); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("in-place mutation error=%v", err)
	}

	request.Version = 2
	request.Predecessor = &ContractReference{ContractID: first.ContractID, Version: first.Version, Digest: first.Digest}
	request.RequestedBudget.MaxTokens = 40
	second, _, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Version = 3
	request.Predecessor = &ContractReference{ContractID: second.ContractID, Version: second.Version, Digest: second.Digest}
	request.ProjectRevision = strings.Repeat("9", 64)
	if _, _, err := service.Create(context.Background(), request); !errors.Is(err, ErrAmendmentConflict) {
		t.Fatalf("identity amendment error=%v", err)
	}
}

func contractFixture() BuildRequest {
	when := time.Date(2026, time.August, 17, 18, 0, 0, 0, time.UTC)
	trade := project.RegistryReference{ID: "trade:go", Version: 2, Digest: strings.Repeat("a", 64)}
	worker := project.RegistryReference{ID: "worker:go-sol", Version: 3, Digest: strings.Repeat("b", 64)}
	task := project.TaskRevision{
		RecordHeader: project.NewRecordHeader(project.RecordTaskRevision, "record:task", "project:one"),
		TaskID:       "task:one", Revision: 4, Objective: "implement", Priority: project.TaskPriorityHigh, GraphRevision: 5,
	}
	work := project.WorkPackageDefinition{
		RecordHeader:  project.NewRecordHeader(project.RecordWorkPackage, "record:work", "project:one"),
		WorkPackageID: "work:one", TaskID: task.TaskID, TaskRevision: task.Revision, GraphRevision: task.GraphRevision,
		Objective: "implement policy", Trade: "go", TradeReference: &trade,
		Scope:        project.WorkScope{Allowed: []string{"src"}, Inspect: []string{"docs"}, Forbidden: []string{"src/blocked"}},
		Deliverables: []string{"code"}, AcceptanceCriteria: []string{"tests pass"},
		Risk:         []project.RiskDimension{{Name: "security", Level: project.RiskHigh}},
		QualityGates: []project.QualityGateReference{{GateID: "gate:tests", Version: 2, Digest: strings.Repeat("c", 64), Required: true}},
	}
	workDigest := strings.Repeat("d", 64)
	taskDigest := strings.Repeat("7", 64)
	graphDigest := strings.Repeat("6", 64)
	task.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: taskDigest}
	work.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: workDigest}
	graph := project.DependencyGraphRevision{
		RecordHeader: project.NewRecordHeader(project.RecordDependencyGraph, "record:graph", "project:one"),
		TaskID:       task.TaskID, TaskRevision: task.Revision, Revision: task.GraphRevision,
		Members: []project.DependencyGraphMember{{WorkPackageID: work.WorkPackageID, DefinitionRecordID: work.RecordID, DefinitionDigest: workDigest}},
	}
	graph.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: graphDigest}
	capabilities := []Capability{CapabilityInspect, CapabilityWrite, CapabilityTest, CapabilityShell, CapabilitySecret, CapabilityNetwork, CapabilityDatabase, CapabilityBranch, CapabilityMerge, CapabilityDependencyInstall, CapabilityDeployment, CapabilityInfrastructure}
	budget := BudgetLimits{MaxTokens: 100, MaxCostMicros: 1_000, MaxWallClockSeconds: 60, MaxRetries: 2, MaxToolCalls: 20, MaxConcurrentWorkers: 2}
	layers := make([]PolicyLayer, 0, len(policyLayerNames))
	for _, name := range policyLayerNames {
		ceiling := budget
		if name == "user" {
			ceiling.MaxTokens = 50
			ceiling.MaxToolCalls = 10
		}
		layers = append(layers, PolicyLayer{
			Name: name, AllowedCapabilities: capabilities, InspectPaths: []string{"docs", "src"}, WritePaths: []string{"src"},
			ForbiddenPaths: []string{"src/blocked"}, AllowedSecretIDs: []string{"secret:deploy-key"}, BudgetCeiling: ceiling,
		})
	}
	return BuildRequest{
		ContractID: "contract:one", Version: 1, Task: task, TaskDigest: taskDigest, Graph: graph, GraphDigest: graphDigest, WorkPackage: work, WorkPackageDigest: workDigest,
		ExecutionID: "execution:one", ProjectRevision: strings.Repeat("e", 64), Trade: trade, Worker: worker,
		WorkerProfile: storage.WorkerProfile{WorkerID: worker.ID, Version: worker.Version, Lifecycle: storage.RegistryActive, TradeID: trade.ID, TradeVersion: trade.Version, ContentHash: worker.Digest},
		Instruction:   binding("instruction:go", "1"), ContextDigest: strings.Repeat("f", 64), Runtime: binding("runtime:local", "2"),
		Provider: binding("provider:openai", "3"), Model: binding("model:sol", "4"), Node: binding("node:local", "5"),
		RequestedCapabilities: []Capability{CapabilityInspect, CapabilityWrite, CapabilityTest}, PolicyLayers: layers, RequestedBudget: budget,
		NotBefore: when.Add(time.Second), Deadline: when.Add(31 * time.Second), CreatedAt: when, CreatedBy: "actor:scheduler",
	}
}

func binding(id, seed string) BindingReference {
	return BindingReference{ID: id, Version: 1, Digest: strings.Repeat(seed, 64)}
}

func reverseLayers(values []PolicyLayer) []PolicyLayer {
	result := append([]PolicyLayer(nil), values...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func mustBuild(t *testing.T, request BuildRequest) Contract {
	t.Helper()
	contract, _, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

type memoryContractStore struct {
	records map[string]storage.ExecutionContractRecord
}

func (store *memoryContractStore) SaveExecutionContract(_ context.Context, record storage.ExecutionContractRecord) (storage.RegistryWriteResult, error) {
	key := record.ContractID + ":" + strconv.FormatInt(record.Version, 10)
	if existing, ok := store.records[key]; ok {
		if existing.Digest != record.Digest {
			return storage.RegistryWriteResult{}, storage.ErrConflict
		}
		return storage.RegistryWriteResult{AlreadyPresent: true}, nil
	}
	record.ContractJSON = append([]byte(nil), record.ContractJSON...)
	store.records[key] = record
	return storage.RegistryWriteResult{}, nil
}

func (store *memoryContractStore) GetExecutionContract(_ context.Context, contractID string, version int64) (storage.ExecutionContractRecord, error) {
	record, ok := store.records[contractID+":"+strconv.FormatInt(version, 10)]
	if !ok {
		return storage.ExecutionContractRecord{}, storage.ErrNotFound
	}
	record.ContractJSON = append([]byte(nil), record.ContractJSON...)
	return record, nil
}

func (store *memoryContractStore) ListExecutionContracts(context.Context, storage.ExecutionContractQuery) (storage.Page[storage.ExecutionContractRecord], error) {
	return storage.Page[storage.ExecutionContractRecord]{}, nil
}
