// Package dispatchbinding binds compiled context and immutable execution
// contracts before handing a planned assignment to orchestration.
package dispatchbinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"

	"syncgate/internal/contextcompiler"
	"syncgate/internal/executioncontract"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/storage"
)

const attemptBindingSchema = "syncgate.attempt-binding.v1"

var ErrInvalidAttemptBinding = errors.New("invalid orchestration attempt binding")

type ContractBindingService struct {
	Contracts executioncontract.Service
	Control   orchestration.ControlService
	Now       func() time.Time
}

type BindAndPlanRequest struct {
	Layout    project.Layout
	Selection orchestration.SelectionResult
	Context   contextcompiler.Request
	Contract  executioncontract.BuildRequest
	Plan      orchestration.PlanRequest
}

type BindAndPlanResult struct {
	Context  contextcompiler.Result
	Contract executioncontract.Contract
	Planned  storage.OrchestrationWriteResult
}

// BindAndPlan compiles the bounded context, creates the immutable execution
// contract, records a digest-only attempt binding, and creates the planned
// assignment. It does not claim a lease or invoke a runtime.
func (service ContractBindingService) BindAndPlan(ctx context.Context, request BindAndPlanRequest) (BindAndPlanResult, error) {
	if ctx == nil || service.Contracts.Store == nil || service.Control.Store == nil {
		return BindAndPlanResult{}, ErrInvalidAttemptBinding
	}
	if err := ctx.Err(); err != nil {
		return BindAndPlanResult{}, err
	}
	if request.Selection.Blocked || request.Selection.Selected == nil || request.Layout.Root() == "" {
		return BindAndPlanResult{}, ErrInvalidAttemptBinding
	}
	if err := orchestration.ApplySelectionToPlan(request.Selection, &request.Plan); err != nil {
		return BindAndPlanResult{}, err
	}
	if err := validateBindingIdentity(request); err != nil {
		return BindAndPlanResult{}, err
	}
	compiled, err := contextcompiler.Compile(ctx, request.Layout, request.Context)
	if err != nil {
		return BindAndPlanResult{}, err
	}
	if compiled.Bundle.Manifest.ProjectID != request.Contract.Task.ProjectID || compiled.Bundle.Manifest.WorkPackageID != request.Contract.WorkPackage.WorkPackageID || compiled.Bundle.Manifest.TradeReference != request.Contract.Trade {
		return BindAndPlanResult{}, ErrInvalidAttemptBinding
	}
	request.Contract.ContextDigest = compiled.Bundle.Manifest.ContextDigest
	contract, _, err := service.Contracts.Create(ctx, request.Contract)
	if err != nil {
		return BindAndPlanResult{}, err
	}
	if err := bindPlanContract(&request.Plan, contract); err != nil {
		return BindAndPlanResult{}, err
	}
	createdAt := service.now(contract.CreatedAt)
	binding, err := buildAttemptBinding(request.Plan.AttemptID, contract, compiled.Bundle.Manifest, createdAt)
	if err != nil {
		return BindAndPlanResult{}, err
	}
	request.Plan.Binding = binding
	planned, err := service.Control.Plan(ctx, request.Plan)
	if err != nil {
		return BindAndPlanResult{}, err
	}
	return BindAndPlanResult{Context: compiled, Contract: contract, Planned: planned}, nil
}

func validateBindingIdentity(request BindAndPlanRequest) error {
	selected := request.Selection.Selected
	if request.Context.ProjectID != request.Contract.Task.ProjectID || request.Context.WorkPackageID != request.Contract.WorkPackage.WorkPackageID || request.Context.TradeReference != request.Contract.Trade ||
		request.Plan.ProjectID != request.Contract.Task.ProjectID || request.Plan.TaskID != request.Contract.Task.TaskID || request.Plan.TaskRevision != request.Contract.Task.Revision || request.Plan.GraphRevision != request.Contract.Graph.Revision ||
		request.Plan.WorkPackageID != request.Contract.WorkPackage.WorkPackageID || request.Plan.ExecutionID != request.Contract.ExecutionID ||
		request.Plan.WorkerID != selected.WorkerID || request.Plan.NodeID != selected.NodeID || request.Contract.Worker.ID != selected.WorkerID || request.Contract.Node.ID != selected.NodeID {
		return ErrInvalidAttemptBinding
	}
	return nil
}

func bindPlanContract(plan *orchestration.PlanRequest, contract executioncontract.Contract) error {
	if plan.ContractID != "" && plan.ContractID != contract.ContractID || plan.ContractVersion != 0 && plan.ContractVersion != contract.Version || plan.ContractDigest != "" && plan.ContractDigest != contract.Digest {
		return ErrInvalidAttemptBinding
	}
	plan.ContractID, plan.ContractVersion, plan.ContractDigest = contract.ContractID, contract.Version, contract.Digest
	return nil
}

type attemptBindingEnvelope struct {
	Schema             string                                 `json:"schema"`
	Contract           executioncontract.ContractReference    `json:"contract"`
	ContextDigest      string                                 `json:"context_digest"`
	ContextCompiler    string                                 `json:"context_compiler"`
	ProjectRevision    string                                 `json:"project_revision"`
	TaskID             string                                 `json:"task_id"`
	TaskRevision       int64                                  `json:"task_revision"`
	TaskDigest         string                                 `json:"task_digest"`
	GraphRevision      int64                                  `json:"graph_revision"`
	GraphDigest        string                                 `json:"graph_digest"`
	WorkPackageID      string                                 `json:"work_package_id"`
	WorkPackageDigest  string                                 `json:"work_package_digest"`
	Trade              project.RegistryReference              `json:"trade"`
	Worker             project.RegistryReference              `json:"worker"`
	Instruction        executioncontract.BindingReference     `json:"instruction"`
	Runtime            executioncontract.BindingReference     `json:"runtime"`
	Provider           executioncontract.BindingReference     `json:"provider"`
	Model              executioncontract.BindingReference     `json:"model"`
	Node               executioncontract.BindingReference     `json:"node"`
	Permissions        executioncontract.EffectivePermissions `json:"permissions"`
	Budget             executioncontract.BudgetLimits         `json:"budget"`
	Deadline           time.Time                              `json:"deadline"`
	RetryLimit         int64                                  `json:"retry_limit"`
	Deliverables       []string                               `json:"deliverables"`
	AcceptanceCriteria []string                               `json:"acceptance_criteria"`
	RequiredGates      []executioncontract.GateRequirement    `json:"required_gates"`
	OutputSchema       string                                 `json:"output_schema"`
	SourceDigests      []string                               `json:"source_digests"`
	Omissions          []contextcompiler.Notice               `json:"omissions"`
	Warnings           []contextcompiler.Notice               `json:"warnings"`
}

func buildAttemptBinding(attemptID string, contract executioncontract.Contract, manifest contextcompiler.Manifest, createdAt time.Time) (storage.OrchestrationAttemptBinding, error) {
	if !validBindingID(attemptID) || executioncontract.VerifyDigest(contract) != nil || manifest.ContextDigest != contract.ContextDigest || createdAt.IsZero() {
		return storage.OrchestrationAttemptBinding{}, ErrInvalidAttemptBinding
	}
	sources := make([]string, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		sources = append(sources, source.Digest)
	}
	sort.Strings(sources)
	envelope := attemptBindingEnvelope{
		Schema: attemptBindingSchema, Contract: executioncontract.ContractReference{ContractID: contract.ContractID, Version: contract.Version, Digest: contract.Digest},
		ContextDigest: contract.ContextDigest, ContextCompiler: contextcompiler.CompilerVersion, ProjectRevision: contract.ProjectRevision,
		TaskID: contract.TaskID, TaskRevision: contract.TaskRevision, TaskDigest: contract.TaskDigest, GraphRevision: contract.GraphRevision, GraphDigest: contract.GraphDigest,
		WorkPackageID: contract.WorkPackageID, WorkPackageDigest: contract.WorkPackageDigest, Trade: contract.Trade, Worker: contract.Worker,
		Instruction: contract.Instruction, Runtime: contract.Runtime, Provider: contract.Provider, Model: contract.Model, Node: contract.Node,
		Permissions: contract.Permissions, Budget: contract.Budget, Deadline: contract.Deadline, RetryLimit: contract.Budget.MaxRetries,
		Deliverables: append([]string(nil), contract.Deliverables...), AcceptanceCriteria: append([]string(nil), contract.AcceptanceCriteria...), RequiredGates: append([]executioncontract.GateRequirement(nil), contract.RequiredGates...),
		OutputSchema: "syncgate.result-envelope.v1", SourceDigests: sources, Omissions: append([]contextcompiler.Notice(nil), manifest.Omissions...), Warnings: append([]contextcompiler.Notice(nil), manifest.Warnings...),
	}
	raw, err := json.Marshal(envelope)
	if err != nil || len(raw) == 0 || len(raw) > storage.MaxProjectProjectionPayloadBytes {
		return storage.OrchestrationAttemptBinding{}, ErrInvalidAttemptBinding
	}
	sum := sha256.Sum256(raw)
	return storage.OrchestrationAttemptBinding{AttemptID: attemptID, ContractID: contract.ContractID, ContractVersion: contract.Version, ContractDigest: contract.Digest, ContextDigest: contract.ContextDigest, ContextCompilerVersion: contextcompiler.CompilerVersion, BindingDigest: hex.EncodeToString(sum[:]), BindingJSON: raw, CreatedAt: createdAt.UTC()}, nil
}

func (service ContractBindingService) now(fallback time.Time) time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return fallback.UTC()
}

func DecodeAttemptBinding(binding storage.OrchestrationAttemptBinding) (attemptBindingEnvelope, error) {
	var value attemptBindingEnvelope
	if binding.AttemptID == "" || len(binding.BindingJSON) == 0 || len(binding.BindingJSON) > storage.MaxProjectProjectionPayloadBytes || !validBindingDigest(binding.BindingDigest) {
		return value, ErrInvalidAttemptBinding
	}
	sum := sha256.Sum256(binding.BindingJSON)
	if hex.EncodeToString(sum[:]) != binding.BindingDigest || json.Unmarshal(binding.BindingJSON, &value) != nil || value.Schema != attemptBindingSchema || value.ContextDigest != binding.ContextDigest || value.Contract.ContractID != binding.ContractID || value.Contract.Version != binding.ContractVersion || value.Contract.Digest != binding.ContractDigest {
		return attemptBindingEnvelope{}, ErrInvalidAttemptBinding
	}
	return value, nil
}

var bindingIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var bindingDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validBindingID(value string) bool { return bindingIdentifier.MatchString(value) }

func validBindingDigest(value string) bool { return bindingDigest.MatchString(value) }
