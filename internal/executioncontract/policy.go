package executioncontract

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"syncgate/internal/project"
)

func evaluatePolicy(request BuildRequest) (EffectivePermissions, BudgetLimits, error) {
	if err := validateLayers(request.PolicyLayers); err != nil {
		return EffectivePermissions{}, BudgetLimits{}, err
	}
	for _, capability := range request.RequestedCapabilities {
		if !knownCapability(capability) {
			return EffectivePermissions{}, BudgetLimits{}, fmt.Errorf("%w: unknown capability %q", ErrPrivilegeEscalation, capability)
		}
		for _, layer := range request.PolicyLayers {
			if containsCapability(layer.DeniedCapabilities, capability) || !containsCapability(layer.AllowedCapabilities, capability) {
				return EffectivePermissions{}, BudgetLimits{}, fmt.Errorf("%w: %s denied by %s", ErrPrivilegeEscalation, capability, layer.Name)
			}
		}
	}
	permissions := EffectivePermissions{Capabilities: append([]Capability(nil), request.RequestedCapabilities...)}
	forbidden := append([]string(nil), request.WorkPackage.Scope.Forbidden...)
	for _, layer := range request.PolicyLayers {
		forbidden = append(forbidden, layer.ForbiddenPaths...)
	}
	permissions.Forbidden = canonicalPaths(forbidden)
	if containsCapability(permissions.Capabilities, CapabilityInspect) {
		paths := append(append([]string(nil), request.WorkPackage.Scope.Allowed...), request.WorkPackage.Scope.Inspect...)
		for _, layer := range request.PolicyLayers {
			paths = intersectPaths(paths, layer.InspectPaths)
		}
		permissions.InspectPaths = removeFullyForbidden(paths, permissions.Forbidden)
		if len(permissions.InspectPaths) == 0 {
			return EffectivePermissions{}, BudgetLimits{}, fmt.Errorf("%w: inspect path intersection is empty", ErrPrivilegeEscalation)
		}
	}
	if containsCapability(permissions.Capabilities, CapabilityWrite) {
		paths := append([]string(nil), request.WorkPackage.Scope.Allowed...)
		for _, layer := range request.PolicyLayers {
			paths = intersectPaths(paths, layer.WritePaths)
		}
		permissions.WritePaths = removeFullyForbidden(paths, permissions.Forbidden)
		if len(permissions.WritePaths) == 0 {
			return EffectivePermissions{}, BudgetLimits{}, fmt.Errorf("%w: write path intersection is empty", ErrPrivilegeEscalation)
		}
	}
	if len(request.RequestedSecretIDs) > 0 {
		if !containsCapability(permissions.Capabilities, CapabilitySecret) {
			return EffectivePermissions{}, BudgetLimits{}, fmt.Errorf("%w: secret IDs requested without secret capability", ErrPrivilegeEscalation)
		}
		for _, secretID := range request.RequestedSecretIDs {
			if !namespaced(secretID, "secret:") {
				return EffectivePermissions{}, BudgetLimits{}, ErrPrivilegeEscalation
			}
			for _, layer := range request.PolicyLayers {
				if !containsString(layer.AllowedSecretIDs, secretID) {
					return EffectivePermissions{}, BudgetLimits{}, fmt.Errorf("%w: secret %s denied by %s", ErrPrivilegeEscalation, secretID, layer.Name)
				}
			}
		}
		permissions.SecretIDs = append([]string(nil), request.RequestedSecretIDs...)
	}
	budget, err := effectiveBudget(request.RequestedBudget, request.PolicyLayers)
	if err != nil {
		return EffectivePermissions{}, BudgetLimits{}, err
	}
	return permissions, budget, nil
}

func AuthorizePath(contract Contract, capability Capability, path string) error {
	if !containsCapability(contract.Permissions.Capabilities, capability) {
		return ErrPrivilegeEscalation
	}
	if err := project.ValidateProjectRelativePath(path); err != nil {
		return ErrPrivilegeEscalation
	}
	for _, forbidden := range contract.Permissions.Forbidden {
		if within(path, forbidden) {
			return ErrPrivilegeEscalation
		}
	}
	paths := contract.Permissions.InspectPaths
	if capability == CapabilityWrite {
		paths = contract.Permissions.WritePaths
	} else if capability != CapabilityInspect {
		return ErrPrivilegeEscalation
	}
	for _, allowed := range paths {
		if within(path, allowed) {
			return nil
		}
	}
	return ErrPrivilegeEscalation
}

func effectiveBudget(requested BudgetLimits, layers []PolicyLayer) (BudgetLimits, error) {
	if err := validateBudget(requested); err != nil {
		return BudgetLimits{}, err
	}
	result := requested
	for _, layer := range layers {
		if err := validateBudget(layer.BudgetCeiling); err != nil {
			return BudgetLimits{}, fmt.Errorf("%w: %s budget", ErrInvalidContract, layer.Name)
		}
		result.MaxTokens = min(result.MaxTokens, layer.BudgetCeiling.MaxTokens)
		result.MaxCostMicros = min(result.MaxCostMicros, layer.BudgetCeiling.MaxCostMicros)
		result.MaxWallClockSeconds = min(result.MaxWallClockSeconds, layer.BudgetCeiling.MaxWallClockSeconds)
		result.MaxRetries = min(result.MaxRetries, layer.BudgetCeiling.MaxRetries)
		result.MaxToolCalls = min(result.MaxToolCalls, layer.BudgetCeiling.MaxToolCalls)
		result.MaxConcurrentWorkers = min(result.MaxConcurrentWorkers, layer.BudgetCeiling.MaxConcurrentWorkers)
	}
	return result, nil
}
func validateBudget(value BudgetLimits) error {
	const maxDurationSeconds = int64((1<<63 - 1) / int64(time.Second))
	if value.MaxTokens < 1 || value.MaxCostMicros < 1 || value.MaxWallClockSeconds < 1 || value.MaxWallClockSeconds > maxDurationSeconds || value.MaxRetries < 0 || value.MaxToolCalls < 1 || value.MaxConcurrentWorkers < 1 {
		return ErrInvalidContract
	}
	return nil
}
func validateLayers(layers []PolicyLayer) error {
	if len(layers) != len(policyLayerNames) {
		return fmt.Errorf("%w: every policy layer is required", ErrInvalidContract)
	}
	for index, name := range policyLayerNames {
		if layers[index].Name != name {
			return fmt.Errorf("%w: missing policy layer %s", ErrInvalidContract, name)
		}
		if err := validatePolicyLayer(layers[index]); err != nil {
			return err
		}
	}
	return nil
}
func validatePolicyLayer(layer PolicyLayer) error {
	if layer.Name == "" {
		return ErrInvalidContract
	}
	for _, capability := range append(append([]Capability(nil), layer.AllowedCapabilities...), layer.DeniedCapabilities...) {
		if !knownCapability(capability) {
			return fmt.Errorf("%w: unknown policy capability %q", ErrInvalidContract, capability)
		}
	}
	for _, paths := range [][]string{layer.InspectPaths, layer.WritePaths, layer.ForbiddenPaths} {
		for _, path := range paths {
			if err := project.ValidateProjectRelativePath(path); err != nil {
				return fmt.Errorf("%w: policy path", ErrInvalidContract)
			}
		}
	}
	return nil
}
func normalizePolicyLayer(layer *PolicyLayer) {
	layer.AllowedCapabilities = sortedCapabilities(layer.AllowedCapabilities)
	layer.DeniedCapabilities = sortedCapabilities(layer.DeniedCapabilities)
	layer.InspectPaths = canonicalPaths(layer.InspectPaths)
	layer.WritePaths = canonicalPaths(layer.WritePaths)
	layer.ForbiddenPaths = canonicalPaths(layer.ForbiddenPaths)
	layer.AllowedSecretIDs = sortedStrings(layer.AllowedSecretIDs)
}
func knownCapability(value Capability) bool {
	switch value {
	case CapabilityInspect, CapabilityWrite, CapabilityShell, CapabilityTest, CapabilityDependencyInstall, CapabilityNetwork, CapabilitySecret, CapabilityDatabase, CapabilityBranch, CapabilityMerge, CapabilityDeployment, CapabilityInfrastructure:
		return true
	}
	return false
}
func sortedCapabilities(values []Capability) []Capability {
	copy := append([]Capability(nil), values...)
	sort.Slice(copy, func(i, j int) bool { return copy[i] < copy[j] })
	return uniqueCapabilities(copy)
}
func uniqueCapabilities(values []Capability) []Capability {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
func sortedStrings(values []string) []string {
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	result := copy[:0]
	for _, value := range copy {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
func canonicalPaths(values []string) []string {
	values = sortedStrings(values)
	result := make([]string, 0, len(values))
	for _, value := range values {
		covered := false
		for _, existing := range result {
			if within(value, existing) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, value)
		}
	}
	return result
}
func intersectPaths(left, right []string) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	result := []string{}
	for _, a := range left {
		for _, b := range right {
			if within(a, b) {
				result = append(result, a)
			} else if within(b, a) {
				result = append(result, b)
			}
		}
	}
	return canonicalPaths(result)
}
func removeFullyForbidden(paths, forbidden []string) []string {
	result := []string{}
	for _, path := range paths {
		blocked := false
		for _, deny := range forbidden {
			if within(path, deny) {
				blocked = true
				break
			}
		}
		if !blocked {
			result = append(result, path)
		}
	}
	return canonicalPaths(result)
}
func within(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, strings.TrimSuffix(parent, "/")+"/")
}
func containsCapability(values []Capability, value Capability) bool {
	index := sort.Search(len(values), func(i int) bool { return values[i] >= value })
	return index < len(values) && values[index] == value
}
func containsString(values []string, value string) bool {
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}
func min(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

type GateEvidence struct {
	GateID    string
	Version   int64
	Digest    string
	Satisfied bool
}
type CompletionDecision struct {
	Accepted     bool
	Code         string
	MissingGates []string
}

func EvaluateCompletion(contract Contract, evidence []GateEvidence, workerClaimedSuccess bool) CompletionDecision {
	_ = workerClaimedSuccess
	satisfied := map[string]GateEvidence{}
	for _, item := range evidence {
		if item.Satisfied {
			satisfied[item.GateID] = item
		}
	}
	decision := CompletionDecision{Accepted: true, Code: "gates_satisfied"}
	for _, gate := range contract.RequiredGates {
		item, ok := satisfied[gate.GateID]
		if !ok || item.Version != gate.Version || item.Digest != gate.Digest {
			decision.Accepted = false
			decision.Code = "required_gates_missing"
			decision.MissingGates = append(decision.MissingGates, gate.GateID)
		}
	}
	sort.Strings(decision.MissingGates)
	return decision
}
func requiredGates(task project.TaskRevision, work project.WorkPackageDefinition, capabilities []Capability) ([]GateRequirement, error) {
	gates := map[string]GateRequirement{}
	for _, gate := range append(append([]project.QualityGateReference(nil), task.QualityGates...), work.QualityGates...) {
		if !gate.Required {
			continue
		}
		item := GateRequirement{GateID: gate.GateID, Version: gate.Version, Digest: gate.Digest, Rationale: "work_package"}
		if existing, ok := gates[item.GateID]; ok && (existing.Version != item.Version || existing.Digest != item.Digest) {
			return nil, fmt.Errorf("%w: conflicting work-package gate %s", ErrRequiredGates, item.GateID)
		}
		gates[item.GateID] = item
	}
	if work.ReviewRequired {
		if err := addBuiltinGate(gates, "gate:human-review", "work_package_review"); err != nil {
			return nil, err
		}
	}
	for _, risk := range append(append([]project.RiskDimension(nil), task.Risk...), work.Risk...) {
		if risk.Level != project.RiskHigh && risk.Level != project.RiskCritical {
			continue
		}
		if err := addBuiltinGate(gates, "gate:human-review", "high_risk"); err != nil {
			return nil, err
		}
		switch risk.Name {
		case "security", "secret":
			if err := addBuiltinGate(gates, "gate:security-review", "security_risk"); err != nil {
				return nil, err
			}
		case "data", "database":
			if err := addBuiltinGate(gates, "gate:data-review", "data_risk"); err != nil {
				return nil, err
			}
		case "deployment", "infrastructure":
			if err := addBuiltinGate(gates, "gate:operations-approval", "operations_risk"); err != nil {
				return nil, err
			}
		}
	}
	for _, capability := range capabilities {
		var err error
		switch capability {
		case CapabilitySecret:
			err = addBuiltinGate(gates, "gate:security-review", "secret_capability")
		case CapabilityMerge:
			err = addBuiltinGate(gates, "gate:human-review", "merge_capability")
		case CapabilityDeployment, CapabilityInfrastructure:
			err = addBuiltinGate(gates, "gate:operations-approval", "operations_capability")
		}
		if err != nil {
			return nil, err
		}
	}
	result := make([]GateRequirement, 0, len(gates))
	for _, gate := range gates {
		result = append(result, gate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GateID < result[j].GateID })
	return result, nil
}

func addBuiltinGate(gates map[string]GateRequirement, id, rationale string) error {
	builtinDigest := digest([]byte(id + ":v1"))
	if existing, ok := gates[id]; ok {
		if existing.Version != 1 || existing.Digest != builtinDigest {
			return fmt.Errorf("%w: gate %s does not match the required built-in version", ErrRequiredGates, id)
		}
		if rationale < existing.Rationale {
			existing.Rationale = rationale
			gates[id] = existing
		}
		return nil
	}
	gates[id] = GateRequirement{GateID: id, Version: 1, Digest: builtinDigest, Rationale: rationale}
	return nil
}

type Usage struct {
	Tokens            int64
	CostMicros        int64
	WallClockSeconds  int64
	Retries           int64
	ToolCalls         int64
	ConcurrentWorkers int64
	Canceled          bool
	Failure           bool
}
type RuntimeDecision struct {
	State       string
	Code        string
	Terminal    bool
	Recoverable bool
}

func EvaluateUsage(contract Contract, usage Usage) RuntimeDecision {
	if usage.Canceled {
		return RuntimeDecision{"canceled", "canceled_by_authority", true, false}
	}
	if usage.Tokens < 0 || usage.CostMicros < 0 || usage.WallClockSeconds < 0 || usage.Retries < 0 || usage.ToolCalls < 0 || usage.ConcurrentWorkers < 0 {
		return RuntimeDecision{"failed", "invalid_usage", true, false}
	}
	if usage.Tokens > contract.Budget.MaxTokens {
		return RuntimeDecision{"budget_exhausted", "token_budget_exhausted", true, false}
	}
	if usage.CostMicros > contract.Budget.MaxCostMicros {
		return RuntimeDecision{"budget_exhausted", "cost_budget_exhausted", true, false}
	}
	if usage.WallClockSeconds > contract.Budget.MaxWallClockSeconds {
		return RuntimeDecision{"budget_exhausted", "wall_clock_budget_exhausted", true, false}
	}
	if usage.ToolCalls > contract.Budget.MaxToolCalls {
		return RuntimeDecision{"budget_exhausted", "tool_call_budget_exhausted", true, false}
	}
	if usage.ConcurrentWorkers > contract.Budget.MaxConcurrentWorkers {
		return RuntimeDecision{"budget_exhausted", "concurrency_budget_exhausted", true, false}
	}
	if usage.Retries > contract.Budget.MaxRetries {
		return RuntimeDecision{"failed", "retry_budget_exhausted", true, false}
	}
	if usage.Failure {
		return RuntimeDecision{"retry_wait", "recoverable_failure", false, true}
	}
	return RuntimeDecision{"within_budget", "within_budget", false, true}
}
