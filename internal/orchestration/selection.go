package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"syncgate/internal/computenode"
	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/registry"
	"syncgate/internal/storage"
)

const MaxSelectionCandidates = 256

var ErrInvalidSelection = errors.New("invalid dispatch selection request")

type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskModerate RiskLevel = "moderate"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// DispatchBudget is a bounded, pre-contract policy input. Slice 3 binds the
// final effective limits into an immutable execution contract.
type DispatchBudget struct {
	MaxTokens           int64
	MaxCostMicros       int64
	MaxWallClock        time.Duration
	MaxConcurrentWorker int64
}

// GateEvidence is preflight evidence that a named deterministic gate is
// configured and understood. It is not a result-time gate satisfaction claim.
type GateEvidence struct {
	GateID         string
	Version        int64
	Digest         string
	EvidenceID     string
	EvidenceDigest string
}

type SelectionPolicy struct {
	AllowedCapabilities     []executioncontract.Capability
	MaximumRisk             RiskLevel
	MaximumBudget           DispatchBudget
	ActiveAssignments       int64
	GateEvidence            []GateEvidence
	PreferredWorkerNodePair []WorkerNodePreference
}

type WorkerNodePreference struct {
	WorkerID string
	NodeID   string
}

type OperatorOverride struct {
	WorkerID   string
	NodeID     string
	ActorID    string
	ReasonCode string
}

type Candidate struct {
	Worker              storage.WorkerProfile
	Trade               storage.TradeDefinition
	Runtime             executioncontract.BindingReference
	RuntimeAvailable    bool
	RuntimeCapabilities []executioncontract.Capability
	Node                computenode.Definition
	Observation         computenode.Observation
}

type SelectionRequest struct {
	Task              storage.ProjectTaskProjection
	Node              storage.ProjectTaskNodeProjection
	WorkPackage       project.WorkPackageDefinition
	WorkPackageDigest string
	Adaptation        *storage.ProjectTradeAdaptation
	Risk              RiskLevel
	RequestedBudget   DispatchBudget
	RequiredRuntime   []executioncontract.Capability
	RequiredGates     []executioncontract.GateRequirement
	Policy            SelectionPolicy
	Candidates        []Candidate
	Override          *OperatorOverride
	Now               time.Time
}

type CandidateIdentity struct {
	WorkerID      string
	WorkerVersion int64
	NodeID        string
	NodeVersion   int64
	RuntimeID     string
	RuntimeVer    int64
	Provider      string
	Model         string
	ModelVersion  string
}

type CandidateExplanation struct {
	Candidate CandidateIdentity
	Accepted  bool
	Reasons   []string
}

type SelectionResult struct {
	Selected              *CandidateIdentity
	Explanations          []CandidateExplanation
	Blocked               bool
	BlockReasons          []string
	Override              *OperatorOverride
	AssignmentAuditReason string
}

// ApplySelectionToPlan binds a planned authority-local assignment to the
// selected worker/node pair and preserves an explicit override audit reason.
// Slice 3 supplies the immutable contract fields before Plan is persisted.
func ApplySelectionToPlan(result SelectionResult, plan *PlanRequest) error {
	if plan == nil || result.Blocked || result.Selected == nil || !validID(result.Selected.WorkerID) || !validID(result.Selected.NodeID) {
		return ErrInvalidSelection
	}
	if plan.WorkerID != result.Selected.WorkerID || plan.NodeID != result.Selected.NodeID {
		return fmt.Errorf("%w: selected worker/node does not match assignment", ErrInvalidSelection)
	}
	if result.Override != nil {
		if plan.ActorID != "" && plan.ActorID != result.Override.ActorID {
			return fmt.Errorf("%w: override actor does not match assignment audit", ErrInvalidSelection)
		}
		plan.ActorID = result.Override.ActorID
	}
	plan.AssignmentReason = result.AssignmentAuditReason
	return nil
}

// Select evaluates candidates in the Phase 1 policy order. It does not score
// telemetry, create an assignment, or invoke runtime/node providers.
func Select(ctx context.Context, request SelectionRequest) (SelectionResult, error) {
	if ctx == nil {
		return SelectionResult{}, ErrInvalidSelection
	}
	if err := ctx.Err(); err != nil {
		return SelectionResult{}, err
	}
	if err := validateSelectionRequest(request); err != nil {
		return SelectionResult{}, err
	}

	globalReasons := authorityReasons(request)
	result := SelectionResult{AssignmentAuditReason: "deterministic_selection"}
	for _, candidate := range normalizedCandidates(request.Candidates) {
		if err := ctx.Err(); err != nil {
			return SelectionResult{}, err
		}
		identity := candidateIdentity(candidate)
		reasons := append([]string(nil), globalReasons...)
		if len(reasons) == 0 {
			reasons = candidateReasons(candidate, request)
		}
		result.Explanations = append(result.Explanations, CandidateExplanation{Candidate: identity, Accepted: len(reasons) == 0, Reasons: reasons})
	}
	sort.Slice(result.Explanations, func(i, j int) bool {
		return identityLess(result.Explanations[i].Candidate, result.Explanations[j].Candidate)
	})

	accepted := acceptedCandidates(result.Explanations)
	if len(accepted) == 0 {
		result.Blocked = true
		result.BlockReasons = selectionBlockReasons(globalReasons, result.Explanations)
		return result, nil
	}
	sort.Slice(accepted, func(i, j int) bool {
		return preferredLess(accepted[i].Candidate, accepted[j].Candidate, request.Policy.PreferredWorkerNodePair)
	})
	selected := accepted[0].Candidate
	if request.Override != nil {
		override, found := findOverride(accepted, *request.Override)
		if !found {
			result.Blocked = true
			result.BlockReasons = []string{"operator_override_not_eligible"}
			return result, nil
		}
		selected = override.Candidate
		copy := *request.Override
		result.Override = &copy
		result.AssignmentAuditReason = "operator_override"
	}
	result.Selected = &selected
	return result, nil
}

func validateSelectionRequest(request SelectionRequest) error {
	if request.Now.IsZero() || !validRisk(request.Risk) || !validRisk(request.Policy.MaximumRisk) ||
		!validDispatchBudget(request.RequestedBudget) || !validDispatchBudget(request.Policy.MaximumBudget) ||
		request.Policy.MaximumBudget.MaxConcurrentWorker > 2 || request.Policy.ActiveAssignments < 0 ||
		len(request.Candidates) > MaxSelectionCandidates || !validCapabilities(request.RequiredRuntime) || !validCapabilities(request.Policy.AllowedCapabilities) || !validGateRequirements(request.RequiredGates) {
		return ErrInvalidSelection
	}
	if request.WorkPackage.TradeReference == nil || !validRegistryReference(*request.WorkPackage.TradeReference) || !validDigest(request.WorkPackageDigest) {
		return ErrInvalidSelection
	}
	if request.Adaptation != nil && (!validID(request.Adaptation.ProjectID) || !validID(request.Adaptation.AdaptationID) || request.Adaptation.Version < 1 || !validRegistryLifecycle(request.Adaptation.Lifecycle) || !validID(request.Adaptation.TradeID) || request.Adaptation.TradeVersion < 1 || !validTags(request.Adaptation.RequiredCapabilities)) {
		return ErrInvalidSelection
	}
	if err := validateGateEvidence(request.Policy.GateEvidence); err != nil {
		return err
	}
	seenPreferences := map[string]bool{}
	for _, preference := range request.Policy.PreferredWorkerNodePair {
		if !validID(preference.WorkerID) || !validID(preference.NodeID) {
			return ErrInvalidSelection
		}
		key := preference.WorkerID + "\x00" + preference.NodeID
		if seenPreferences[key] {
			return ErrInvalidSelection
		}
		seenPreferences[key] = true
	}
	if request.Override != nil && (!validID(request.Override.WorkerID) || !validID(request.Override.NodeID) || !validID(request.Override.ActorID) || !validID(request.Override.ReasonCode)) {
		return ErrInvalidSelection
	}
	seenCandidates := map[string]bool{}
	for _, candidate := range request.Candidates {
		identity := candidateIdentity(candidate)
		key := identity.WorkerID + "\x00" + fmt.Sprint(identity.WorkerVersion) + "\x00" + identity.NodeID + "\x00" + fmt.Sprint(identity.NodeVersion) + "\x00" + identity.RuntimeID + "\x00" + fmt.Sprint(identity.RuntimeVer)
		if seenCandidates[key] {
			return ErrInvalidSelection
		}
		seenCandidates[key] = true
	}
	return nil
}

func authorityReasons(request SelectionRequest) []string {
	work := request.WorkPackage
	task, node := request.Task, request.Node
	if task.ProjectID != work.ProjectID || node.ProjectID != work.ProjectID || task.TaskID != work.TaskID || node.TaskID != work.TaskID ||
		task.TaskRevision != work.TaskRevision || node.TaskRevision != work.TaskRevision || task.GraphRevision != work.GraphRevision || node.GraphRevision != work.GraphRevision ||
		node.WorkPackageID != work.WorkPackageID || node.DefinitionRecordID != work.RecordID || node.DefinitionHash != request.WorkPackageDigest {
		return []string{"work_package_authority_mismatch"}
	}
	if node.Readiness != string(ReadinessReady) {
		return []string{"work_package_not_ready"}
	}
	if task.State != string(ReadinessReady) {
		return []string{"task_not_dispatchable"}
	}
	if request.Adaptation != nil && (request.Adaptation.ProjectID != work.ProjectID || request.Adaptation.TradeID != work.TradeReference.ID || request.Adaptation.TradeVersion != work.TradeReference.Version || request.Adaptation.Lifecycle != storage.RegistryActive) {
		return []string{"project_trade_authority_mismatch"}
	}
	return nil
}

func candidateReasons(candidate Candidate, request SelectionRequest) []string {
	if reasons := tradeAndCapabilityReasons(candidate, request); len(reasons) > 0 {
		return reasons
	}
	if reasons := runtimeAndNodeReasons(candidate, request); len(reasons) > 0 {
		return reasons
	}
	if reasons := permissionReasons(candidate, request); len(reasons) > 0 {
		return reasons
	}
	if reasons := riskAndGateReasons(request); len(reasons) > 0 {
		return reasons
	}
	return budgetReasons(request)
}

func tradeAndCapabilityReasons(candidate Candidate, request SelectionRequest) []string {
	reference := *request.WorkPackage.TradeReference
	if candidate.Trade.TradeID != reference.ID || candidate.Trade.Version != reference.Version || candidate.Trade.ContentHash != reference.Digest {
		return []string{"trade_authority_mismatch"}
	}
	if candidate.Trade.Lifecycle != storage.RegistryActive {
		return []string{"trade_unavailable"}
	}
	if candidate.Worker.Lifecycle != storage.RegistryActive {
		return []string{"worker_unavailable"}
	}
	if candidate.Worker.TradeID != reference.ID || candidate.Worker.TradeVersion != reference.Version {
		return []string{"worker_trade_mismatch"}
	}
	required := append([]string(nil), candidate.Trade.RequiredCapabilities...)
	if request.Adaptation != nil {
		required = append(required, request.Adaptation.RequiredCapabilities...)
	}
	match := registry.MatchCapabilities(candidate.Worker.CapabilityTags, required, nil)
	if len(match.MissingRequired) == 0 {
		return nil
	}
	reasons := make([]string, 0, len(match.MissingRequired))
	for _, capability := range match.MissingRequired {
		reasons = append(reasons, "missing_capability:"+capability)
	}
	return reasons
}

func runtimeAndNodeReasons(candidate Candidate, request SelectionRequest) []string {
	if candidate.Worker.RuntimeID != candidate.Runtime.ID || candidate.Worker.RuntimeVersion != candidate.Runtime.Version || !validBinding(candidate.Runtime, "runtime:") {
		return []string{"runtime_worker_mismatch"}
	}
	if !candidate.RuntimeAvailable {
		return []string{"runtime_unavailable"}
	}
	access := computenode.RepositoryRead
	if containsCapability(request.RequiredRuntime, executioncontract.CapabilityWrite) || containsCapability(request.RequiredRuntime, executioncontract.CapabilityBranch) {
		access = computenode.RepositoryWrite
	}
	eligibility := computenode.Evaluate(candidate.Node, candidate.Observation, computenode.Requirements{RepositoryAccess: access, Runtime: candidate.Runtime}, request.Now)
	return append([]string(nil), eligibility.Reasons...)
}

func permissionReasons(candidate Candidate, request SelectionRequest) []string {
	reasons := []string{}
	for _, capability := range sortedCapabilities(request.RequiredRuntime) {
		if !containsCapability(request.Policy.AllowedCapabilities, capability) {
			reasons = append(reasons, "capability_denied_by_policy:"+string(capability))
		}
		if !containsCapability(candidate.RuntimeCapabilities, capability) {
			reasons = append(reasons, "runtime_capability_unavailable:"+string(capability))
		}
	}
	return reasons
}

func riskAndGateReasons(request SelectionRequest) []string {
	if riskRank(request.Risk) > riskRank(request.Policy.MaximumRisk) {
		return []string{"risk_limit_exceeded"}
	}
	for _, gate := range request.RequiredGates {
		if !gateEvidenceAvailable(gate, request.Policy.GateEvidence) {
			return []string{"missing_gate_evidence:" + gate.GateID}
		}
	}
	return nil
}

func budgetReasons(request SelectionRequest) []string {
	requested, maximum := request.RequestedBudget, request.Policy.MaximumBudget
	switch {
	case requested.MaxTokens > maximum.MaxTokens:
		return []string{"budget_tokens_exceeded"}
	case requested.MaxCostMicros > maximum.MaxCostMicros:
		return []string{"budget_cost_exceeded"}
	case requested.MaxWallClock > maximum.MaxWallClock:
		return []string{"budget_timeout_exceeded"}
	case requested.MaxConcurrentWorker > maximum.MaxConcurrentWorker:
		return []string{"budget_concurrency_exceeded"}
	case request.Policy.ActiveAssignments >= maximum.MaxConcurrentWorker:
		return []string{"concurrency_limit_reached"}
	default:
		return nil
	}
}

func acceptedCandidates(explanations []CandidateExplanation) []CandidateExplanation {
	accepted := make([]CandidateExplanation, 0, len(explanations))
	for _, explanation := range explanations {
		if explanation.Accepted {
			accepted = append(accepted, explanation)
		}
	}
	return accepted
}

func findOverride(candidates []CandidateExplanation, override OperatorOverride) (CandidateExplanation, bool) {
	for _, candidate := range candidates {
		if candidate.Candidate.WorkerID == override.WorkerID && candidate.Candidate.NodeID == override.NodeID {
			return candidate, true
		}
	}
	return CandidateExplanation{}, false
}

func selectionBlockReasons(global []string, explanations []CandidateExplanation) []string {
	if len(global) > 0 {
		return append([]string(nil), global...)
	}
	reasons := []string{}
	for _, explanation := range explanations {
		reasons = append(reasons, explanation.Reasons...)
	}
	if len(reasons) == 0 {
		return []string{"no_registered_candidates"}
	}
	sort.Strings(reasons)
	return uniqueStrings(reasons)
}

func candidateIdentity(candidate Candidate) CandidateIdentity {
	return CandidateIdentity{WorkerID: candidate.Worker.WorkerID, WorkerVersion: candidate.Worker.Version, NodeID: candidate.Node.NodeID, NodeVersion: candidate.Node.Version, RuntimeID: candidate.Runtime.ID, RuntimeVer: candidate.Runtime.Version, Provider: candidate.Worker.Provider, Model: candidate.Worker.Model, ModelVersion: candidate.Worker.ModelVersion}
}

func normalizedCandidates(values []Candidate) []Candidate {
	result := append([]Candidate(nil), values...)
	sort.Slice(result, func(i, j int) bool { return identityLess(candidateIdentity(result[i]), candidateIdentity(result[j])) })
	return result
}

func preferredLess(left, right CandidateIdentity, preferences []WorkerNodePreference) bool {
	leftRank, rightRank := preferenceRank(left, preferences), preferenceRank(right, preferences)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	return identityLess(left, right)
}

func preferenceRank(identity CandidateIdentity, preferences []WorkerNodePreference) int {
	for index, preference := range preferences {
		if preference.WorkerID == identity.WorkerID && preference.NodeID == identity.NodeID {
			return index
		}
	}
	return len(preferences)
}

func identityLess(left, right CandidateIdentity) bool {
	if left.WorkerID != right.WorkerID {
		return left.WorkerID < right.WorkerID
	}
	if left.WorkerVersion != right.WorkerVersion {
		return left.WorkerVersion < right.WorkerVersion
	}
	if left.NodeID != right.NodeID {
		return left.NodeID < right.NodeID
	}
	if left.NodeVersion != right.NodeVersion {
		return left.NodeVersion < right.NodeVersion
	}
	if left.RuntimeID != right.RuntimeID {
		return left.RuntimeID < right.RuntimeID
	}
	return left.RuntimeVer < right.RuntimeVer
}

func validateGateEvidence(values []GateEvidence) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !validID(value.GateID) || value.Version < 1 || !validDigest(value.Digest) || !validID(value.EvidenceID) || !validDigest(value.EvidenceDigest) {
			return ErrInvalidSelection
		}
		key := value.GateID + "\x00" + fmt.Sprint(value.Version) + "\x00" + value.Digest
		if seen[key] {
			return ErrInvalidSelection
		}
		seen[key] = true
	}
	return nil
}

func gateEvidenceAvailable(requirement executioncontract.GateRequirement, evidence []GateEvidence) bool {
	for _, value := range evidence {
		if value.GateID == requirement.GateID && value.Version == requirement.Version && value.Digest == requirement.Digest {
			return true
		}
	}
	return false
}

func validGateRequirements(values []executioncontract.GateRequirement) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if !validID(value.GateID) || value.Version < 1 || !validDigest(value.Digest) {
			return false
		}
		key := value.GateID + "\x00" + fmt.Sprint(value.Version) + "\x00" + value.Digest
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func validDispatchBudget(value DispatchBudget) bool {
	return value.MaxTokens > 0 && value.MaxCostMicros > 0 && value.MaxWallClock > 0 && value.MaxConcurrentWorker > 0
}

func validRisk(value RiskLevel) bool {
	return value == RiskLow || value == RiskModerate || value == RiskHigh || value == RiskCritical
}

func riskRank(value RiskLevel) int {
	switch value {
	case RiskLow:
		return 0
	case RiskModerate:
		return 1
	case RiskHigh:
		return 2
	case RiskCritical:
		return 3
	default:
		return -1
	}
}

func validRegistryReference(value project.RegistryReference) bool {
	return validID(value.ID) && value.Version > 0 && validDigest(value.Digest)
}

func validBinding(value executioncontract.BindingReference, prefix string) bool {
	return validID(value.ID) && strings.HasPrefix(value.ID, prefix) && value.Version > 0 && validDigest(value.Digest)
}

func validRegistryLifecycle(value storage.RegistryLifecycle) bool {
	return value == storage.RegistryActive || value == storage.RegistryDeprecated || value == storage.RegistryDisabled
}

func validTags(values []string) bool {
	for _, value := range values {
		if !validID(value) {
			return false
		}
	}
	return true
}

func validCapabilities(values []executioncontract.Capability) bool {
	for _, value := range values {
		if !knownSelectionCapability(value) {
			return false
		}
	}
	return true
}

func knownSelectionCapability(value executioncontract.Capability) bool {
	switch value {
	case executioncontract.CapabilityInspect, executioncontract.CapabilityWrite, executioncontract.CapabilityShell, executioncontract.CapabilityTest, executioncontract.CapabilityDependencyInstall, executioncontract.CapabilityNetwork, executioncontract.CapabilitySecret, executioncontract.CapabilityDatabase, executioncontract.CapabilityBranch, executioncontract.CapabilityMerge, executioncontract.CapabilityDeployment, executioncontract.CapabilityInfrastructure:
		return true
	default:
		return false
	}
}

func sortedCapabilities(values []executioncontract.Capability) []executioncontract.Capability {
	result := append([]executioncontract.Capability(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func containsCapability(values []executioncontract.Capability, wanted executioncontract.Capability) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
