// Package executioncontract defines immutable, provider-neutral execution
// authority. It contains policy and persistence orchestration, not runtimes.
package executioncontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"syncgate/internal/project"
	"syncgate/internal/storage"
)

const Schema = "syncgate.execution-contract.v1"

var (
	ErrInvalidContract     = errors.New("invalid execution contract")
	ErrPrivilegeEscalation = errors.New("execution contract privilege escalation")
	ErrAmendmentConflict   = errors.New("execution contract amendment conflict")
	ErrRequiredGates       = errors.New("execution contract required gates are incomplete")
)

type Capability string

const (
	CapabilityInspect           Capability = "inspect"
	CapabilityWrite             Capability = "write"
	CapabilityShell             Capability = "shell"
	CapabilityTest              Capability = "test"
	CapabilityDependencyInstall Capability = "dependency_install"
	CapabilityNetwork           Capability = "network"
	CapabilitySecret            Capability = "secret"
	CapabilityDatabase          Capability = "database"
	CapabilityBranch            Capability = "branch"
	CapabilityMerge             Capability = "merge"
	CapabilityDeployment        Capability = "deployment"
	CapabilityInfrastructure    Capability = "infrastructure"
)

var policyLayerNames = []string{"project", "runtime", "task", "trade", "user", "work_package", "worker"}

type BindingReference struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
}

type ContractReference struct {
	ContractID string `json:"contract_id"`
	Version    int64  `json:"version"`
	Digest     string `json:"digest"`
}

type BudgetLimits struct {
	MaxTokens            int64 `json:"max_tokens"`
	MaxCostMicros        int64 `json:"max_cost_micros"`
	MaxWallClockSeconds  int64 `json:"max_wall_clock_seconds"`
	MaxRetries           int64 `json:"max_retries"`
	MaxToolCalls         int64 `json:"max_tool_calls"`
	MaxConcurrentWorkers int64 `json:"max_concurrent_workers"`
}

type PolicyLayer struct {
	Name                string       `json:"name"`
	AllowedCapabilities []Capability `json:"allowed_capabilities"`
	DeniedCapabilities  []Capability `json:"denied_capabilities,omitempty"`
	InspectPaths        []string     `json:"inspect_paths"`
	WritePaths          []string     `json:"write_paths"`
	ForbiddenPaths      []string     `json:"forbidden_paths,omitempty"`
	AllowedSecretIDs    []string     `json:"allowed_secret_ids,omitempty"`
	BudgetCeiling       BudgetLimits `json:"budget_ceiling"`
}

type EffectivePermissions struct {
	Capabilities []Capability `json:"capabilities"`
	InspectPaths []string     `json:"inspect_paths,omitempty"`
	WritePaths   []string     `json:"write_paths,omitempty"`
	Forbidden    []string     `json:"forbidden_paths,omitempty"`
	SecretIDs    []string     `json:"secret_ids,omitempty"`
}

type GateRequirement struct {
	GateID    string `json:"gate_id"`
	Version   int64  `json:"version"`
	Digest    string `json:"digest"`
	Rationale string `json:"rationale"`
}

type Contract struct {
	Schema              string                    `json:"schema"`
	ContractID          string                    `json:"contract_id"`
	Version             int64                     `json:"version"`
	Predecessor         *ContractReference        `json:"predecessor,omitempty"`
	ProjectID           string                    `json:"project_id"`
	ProjectRevision     string                    `json:"project_revision"`
	TaskID              string                    `json:"task_id"`
	TaskRecordID        string                    `json:"task_record_id"`
	TaskRevision        int64                     `json:"task_revision"`
	TaskDigest          string                    `json:"task_digest"`
	GraphRecordID       string                    `json:"graph_record_id"`
	GraphRevision       int64                     `json:"graph_revision"`
	GraphDigest         string                    `json:"graph_digest"`
	WorkPackageID       string                    `json:"work_package_id"`
	WorkPackageRecordID string                    `json:"work_package_record_id"`
	WorkPackageDigest   string                    `json:"work_package_digest"`
	ExecutionID         string                    `json:"execution_id"`
	Trade               project.RegistryReference `json:"trade"`
	Worker              project.RegistryReference `json:"worker"`
	Instruction         BindingReference          `json:"instruction"`
	ContextDigest       string                    `json:"context_digest"`
	Runtime             BindingReference          `json:"runtime"`
	Provider            BindingReference          `json:"provider"`
	Model               BindingReference          `json:"model"`
	Node                BindingReference          `json:"node"`
	Permissions         EffectivePermissions      `json:"permissions"`
	Budget              BudgetLimits              `json:"budget"`
	NotBefore           time.Time                 `json:"not_before"`
	Deadline            time.Time                 `json:"deadline"`
	Deliverables        []string                  `json:"deliverables"`
	AcceptanceCriteria  []string                  `json:"acceptance_criteria"`
	RequiredGates       []GateRequirement         `json:"required_gates"`
	CreatedAt           time.Time                 `json:"created_at"`
	CreatedBy           string                    `json:"created_by"`
	Digest              string                    `json:"digest"`
}

type BuildRequest struct {
	ContractID            string
	Version               int64
	Predecessor           *ContractReference
	Task                  project.TaskRevision
	TaskDigest            string
	Graph                 project.DependencyGraphRevision
	GraphDigest           string
	WorkPackage           project.WorkPackageDefinition
	WorkPackageDigest     string
	ExecutionID           string
	ProjectRevision       string
	Trade                 project.RegistryReference
	Worker                project.RegistryReference
	WorkerProfile         storage.WorkerProfile
	Instruction           BindingReference
	ContextDigest         string
	Runtime               BindingReference
	Provider              BindingReference
	Model                 BindingReference
	Node                  BindingReference
	RequestedCapabilities []Capability
	RequestedSecretIDs    []string
	PolicyLayers          []PolicyLayer
	RequestedBudget       BudgetLimits
	NotBefore             time.Time
	Deadline              time.Time
	CreatedAt             time.Time
	CreatedBy             string
}

type Service struct {
	Store storage.ExecutionContractStore
}

func Build(request BuildRequest) (Contract, []byte, error) {
	normalizeBuildRequest(&request)
	if err := validateBindings(request); err != nil {
		return Contract{}, nil, err
	}
	permissions, budget, err := evaluatePolicy(request)
	if err != nil {
		return Contract{}, nil, err
	}
	if request.Deadline.Before(request.NotBefore) || request.Deadline.Sub(request.NotBefore) > time.Duration(budget.MaxWallClockSeconds)*time.Second {
		return Contract{}, nil, fmt.Errorf("%w: deadline exceeds wall-clock ceiling", ErrInvalidContract)
	}
	gates, err := requiredGates(request.Task, request.WorkPackage, permissions.Capabilities)
	if err != nil {
		return Contract{}, nil, err
	}
	contract := Contract{
		Schema: Schema, ContractID: request.ContractID, Version: request.Version, Predecessor: cloneContractReference(request.Predecessor),
		ProjectID: request.Task.ProjectID, ProjectRevision: request.ProjectRevision, TaskID: request.Task.TaskID, TaskRecordID: request.Task.RecordID,
		TaskRevision: request.Task.Revision, TaskDigest: request.TaskDigest, GraphRecordID: request.Graph.RecordID, GraphRevision: request.Graph.Revision, GraphDigest: request.GraphDigest,
		WorkPackageID: request.WorkPackage.WorkPackageID, WorkPackageRecordID: request.WorkPackage.RecordID,
		WorkPackageDigest: request.WorkPackageDigest, ExecutionID: request.ExecutionID, Trade: request.Trade, Worker: request.Worker,
		Instruction: request.Instruction, ContextDigest: request.ContextDigest, Runtime: request.Runtime, Provider: request.Provider,
		Model: request.Model, Node: request.Node, Permissions: permissions, Budget: budget, NotBefore: request.NotBefore,
		Deadline: request.Deadline, Deliverables: append([]string(nil), request.WorkPackage.Deliverables...),
		AcceptanceCriteria: append([]string(nil), request.WorkPackage.AcceptanceCriteria...), RequiredGates: gates,
		CreatedAt: request.CreatedAt, CreatedBy: request.CreatedBy,
	}
	unsigned, err := json.Marshal(contract)
	if err != nil {
		return Contract{}, nil, err
	}
	contract.Digest = digest(unsigned)
	raw, err := json.Marshal(contract)
	if err != nil {
		return Contract{}, nil, err
	}
	return contract, raw, nil
}

// VerifyDigest proves that a decoded contract still matches the digest over
// its canonical JSON representation with the digest field empty.
func VerifyDigest(contract Contract) error {
	expected := contract.Digest
	if contract.Schema != Schema || !validDigest(expected) {
		return ErrInvalidContract
	}
	contract.Digest = ""
	raw, err := json.Marshal(contract)
	if err != nil || digest(raw) != expected {
		return fmt.Errorf("%w: execution contract digest mismatch", ErrInvalidContract)
	}
	return nil
}

func (service Service) Create(ctx context.Context, request BuildRequest) (Contract, storage.RegistryWriteResult, error) {
	if service.Store == nil || ctx == nil {
		return Contract{}, storage.RegistryWriteResult{}, ErrInvalidContract
	}
	if err := ctx.Err(); err != nil {
		return Contract{}, storage.RegistryWriteResult{}, err
	}
	contract, raw, err := Build(request)
	if err != nil {
		return Contract{}, storage.RegistryWriteResult{}, err
	}
	if contract.Version > 1 {
		prior, err := service.Store.GetExecutionContract(ctx, contract.ContractID, contract.Version-1)
		if err != nil || contract.Predecessor == nil || prior.Digest != contract.Predecessor.Digest {
			return Contract{}, storage.RegistryWriteResult{}, ErrAmendmentConflict
		}
		var previous Contract
		if json.Unmarshal(prior.ContractJSON, &previous) != nil || previous.Digest != prior.Digest || VerifyDigest(previous) != nil || !sameExecutionIdentity(previous, contract) {
			return Contract{}, storage.RegistryWriteResult{}, ErrAmendmentConflict
		}
	}
	result, err := service.Store.SaveExecutionContract(ctx, storage.ExecutionContractRecord{
		ContractID: contract.ContractID, Version: contract.Version, ProjectID: contract.ProjectID, TaskID: contract.TaskID,
		TaskRevision: contract.TaskRevision, GraphRevision: contract.GraphRevision, WorkPackageID: contract.WorkPackageID,
		ExecutionID: contract.ExecutionID, Digest: contract.Digest, PredecessorDigest: predecessorDigest(contract.Predecessor),
		ContractJSON: raw, CreatedAt: contract.CreatedAt,
	})
	if err != nil {
		return Contract{}, storage.RegistryWriteResult{}, err
	}
	return contract, result, nil
}

func validateBindings(request BuildRequest) error {
	if !namespaced(request.ContractID, "contract:") || request.Version < 1 || !namespaced(request.ExecutionID, "execution:") || !namespaced(request.CreatedBy, "actor:") {
		return ErrInvalidContract
	}
	if err := validateVersion(request.Version, request.Predecessor, request.ContractID); err != nil {
		return err
	}
	if request.Task.ProjectID == "" || request.Task.ProjectID != request.Graph.ProjectID || request.Task.ProjectID != request.WorkPackage.ProjectID ||
		request.Task.TaskID != request.Graph.TaskID || request.Task.Revision != request.Graph.TaskRevision || request.Task.GraphRevision != request.Graph.Revision ||
		request.WorkPackage.TaskID != request.Task.TaskID || request.WorkPackage.TaskRevision != request.Task.Revision || request.WorkPackage.GraphRevision != request.Graph.Revision {
		return fmt.Errorf("%w: task graph and work package identity mismatch", ErrInvalidContract)
	}
	memberFound := false
	for _, member := range request.Graph.Members {
		if member.WorkPackageID == request.WorkPackage.WorkPackageID && member.DefinitionRecordID == request.WorkPackage.RecordID && member.DefinitionDigest == request.WorkPackageDigest {
			memberFound = true
		}
	}
	if !memberFound || !validDigest(request.TaskDigest) || !validDigest(request.GraphDigest) || !validDigest(request.WorkPackageDigest) || !validDigest(request.ProjectRevision) || !validDigest(request.ContextDigest) ||
		request.Task.Integrity.Algorithm != project.HashAlgorithmSHA256 || request.Task.Integrity.Digest != request.TaskDigest ||
		request.Graph.Integrity.Algorithm != project.HashAlgorithmSHA256 || request.Graph.Integrity.Digest != request.GraphDigest ||
		request.WorkPackage.Integrity.Algorithm != project.HashAlgorithmSHA256 || request.WorkPackage.Integrity.Digest != request.WorkPackageDigest {
		return fmt.Errorf("%w: immutable project binding mismatch", ErrInvalidContract)
	}
	if request.WorkPackage.TradeReference == nil || *request.WorkPackage.TradeReference != request.Trade {
		return fmt.Errorf("%w: work package trade binding mismatch", ErrInvalidContract)
	}
	if err := validateProjectReference(request.Trade, "trade:"); err != nil {
		return err
	}
	if err := validateProjectReference(request.Worker, "worker:"); err != nil {
		return err
	}
	if request.WorkerProfile.WorkerID != request.Worker.ID || request.WorkerProfile.Version != request.Worker.Version || request.WorkerProfile.ContentHash != request.Worker.Digest ||
		request.WorkerProfile.TradeID != request.Trade.ID || request.WorkerProfile.TradeVersion != request.Trade.Version || request.WorkerProfile.Lifecycle != storage.RegistryActive {
		return fmt.Errorf("%w: worker profile binding mismatch", ErrInvalidContract)
	}
	for _, item := range []struct {
		value  BindingReference
		prefix string
	}{{request.Instruction, "instruction:"}, {request.Runtime, "runtime:"}, {request.Provider, "provider:"}, {request.Model, "model:"}, {request.Node, "node:"}} {
		if err := validateBindingReference(item.value, item.prefix); err != nil {
			return err
		}
	}
	if request.CreatedAt.IsZero() || request.NotBefore.IsZero() || request.Deadline.IsZero() || !utc(request.CreatedAt) || !utc(request.NotBefore) || !utc(request.Deadline) || request.NotBefore.Before(request.CreatedAt) {
		return fmt.Errorf("%w: contract times must be ordered UTC values", ErrInvalidContract)
	}
	for _, gate := range append(append([]project.QualityGateReference(nil), request.Task.QualityGates...), request.WorkPackage.QualityGates...) {
		if !namespaced(gate.GateID, "gate:") || gate.Version < 1 || !validDigest(gate.Digest) {
			return fmt.Errorf("%w: invalid work-package gate", ErrInvalidContract)
		}
	}
	for _, risk := range append(append([]project.RiskDimension(nil), request.Task.Risk...), request.WorkPackage.Risk...) {
		if strings.TrimSpace(risk.Name) == "" || !knownRiskLevel(risk.Level) {
			return fmt.Errorf("%w: invalid risk dimension", ErrInvalidContract)
		}
	}
	return nil
}

func normalizeBuildRequest(request *BuildRequest) {
	request.RequestedCapabilities = sortedCapabilities(request.RequestedCapabilities)
	request.RequestedSecretIDs = sortedStrings(request.RequestedSecretIDs)
	request.PolicyLayers = append([]PolicyLayer(nil), request.PolicyLayers...)
	for index := range request.PolicyLayers {
		normalizePolicyLayer(&request.PolicyLayers[index])
	}
	sort.Slice(request.PolicyLayers, func(i, j int) bool { return request.PolicyLayers[i].Name < request.PolicyLayers[j].Name })
}

func validateVersion(version int64, predecessor *ContractReference, contractID string) error {
	if version == 1 {
		if predecessor != nil {
			return ErrAmendmentConflict
		}
		return nil
	}
	if predecessor == nil || predecessor.ContractID != contractID || predecessor.Version != version-1 || !validDigest(predecessor.Digest) {
		return ErrAmendmentConflict
	}
	return nil
}
func validateProjectReference(value project.RegistryReference, prefix string) error {
	return validateBindingReference(BindingReference{ID: value.ID, Version: value.Version, Digest: value.Digest}, prefix)
}
func validateBindingReference(value BindingReference, prefix string) error {
	if !namespaced(value.ID, prefix) || value.Version < 1 || !validDigest(value.Digest) {
		return fmt.Errorf("%w: invalid %s binding", ErrInvalidContract, prefix)
	}
	return nil
}
func namespaced(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && len(value) <= 128
}
func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
func knownRiskLevel(value project.RiskLevel) bool {
	return value == project.RiskLow || value == project.RiskMedium || value == project.RiskHigh || value == project.RiskCritical
}
func utc(value time.Time) bool { _, offset := value.Zone(); return offset == 0 }
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func cloneContractReference(value *ContractReference) *ContractReference {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func predecessorDigest(value *ContractReference) string {
	if value == nil {
		return ""
	}
	return value.Digest
}
func sameExecutionIdentity(left, right Contract) bool {
	return left.ContractID == right.ContractID && left.ProjectID == right.ProjectID && left.ProjectRevision == right.ProjectRevision && left.TaskID == right.TaskID && left.TaskRecordID == right.TaskRecordID && left.TaskRevision == right.TaskRevision && left.TaskDigest == right.TaskDigest && left.GraphRecordID == right.GraphRecordID && left.GraphRevision == right.GraphRevision && left.GraphDigest == right.GraphDigest && left.WorkPackageID == right.WorkPackageID && left.WorkPackageRecordID == right.WorkPackageRecordID && left.WorkPackageDigest == right.WorkPackageDigest && left.ExecutionID == right.ExecutionID
}
