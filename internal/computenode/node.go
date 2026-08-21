// Package computenode defines durable node inventory, volatile observations,
// eligibility, and bounded lease reservations without provider credentials.
package computenode

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

	"syncgate/internal/executioncontract"
)

const DefinitionSchema = "syncgate.compute-node.v1"

var (
	ErrInvalidNode     = errors.New("invalid compute node")
	ErrNodeUnavailable = errors.New("compute node unavailable")
	ErrLeaseConflict   = errors.New("compute node lease conflict")
	ErrLeaseNotFound   = errors.New("compute node lease not found")
)

type Lifecycle string

const (
	LifecycleActive   Lifecycle = "active"
	LifecycleDisabled Lifecycle = "disabled"
)

type RepositoryAccess string

const (
	RepositoryNone  RepositoryAccess = "none"
	RepositoryRead  RepositoryAccess = "read"
	RepositoryWrite RepositoryAccess = "write"
)

type Health string

const (
	HealthUnknown   Health = "unknown"
	HealthHealthy   Health = "healthy"
	HealthDegraded  Health = "degraded"
	HealthUnhealthy Health = "unhealthy"
)

type Capacity struct {
	CPUMillis   int64 `json:"cpu_millis"`
	MemoryBytes int64 `json:"memory_bytes"`
	DiskBytes   int64 `json:"disk_bytes"`
}

type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type Definition struct {
	Schema           string                               `json:"schema"`
	NodeID           string                               `json:"node_id"`
	Version          int64                                `json:"version"`
	Lifecycle        Lifecycle                            `json:"lifecycle"`
	OS               string                               `json:"os"`
	Architecture     string                               `json:"architecture"`
	Capacity         Capacity                             `json:"capacity"`
	Tools            []Tool                               `json:"tools"`
	RepositoryAccess RepositoryAccess                     `json:"repository_access"`
	Runtimes         []executioncontract.BindingReference `json:"runtimes"`
	MaxActiveLeases  int64                                `json:"max_active_leases"`
	DefinitionDigest string                               `json:"definition_digest"`
}

type Observation struct {
	Node              executioncontract.BindingReference
	Health            Health
	Available         Capacity
	AvailableTools    []Tool
	AvailableRuntimes []executioncontract.BindingReference
	ActiveLeases      int64
	ObservedAt        time.Time
	ExpiresAt         time.Time
}

type Requirements struct {
	OS               string
	Architecture     string
	Minimum          Capacity
	Tools            []Tool
	RepositoryAccess RepositoryAccess
	Runtime          executioncontract.BindingReference
}

type Eligibility struct {
	Eligible bool
	Reasons  []string
}

type LeaseRequest struct {
	Node                 executioncontract.BindingReference
	OwnerID              string
	Duration             time.Duration
	IdempotencyKeyDigest string
}

type RenewRequest struct {
	LeaseID              string
	OwnerID              string
	Generation           int64
	Duration             time.Duration
	IdempotencyKeyDigest string
}

type Lease struct {
	LeaseID    string
	Node       executioncontract.BindingReference
	OwnerID    string
	Generation int64
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

type Provider interface {
	Definition(context.Context) (Definition, error)
	Observe(context.Context) (Observation, error)
	AcquireLease(context.Context, LeaseRequest) (Lease, error)
	RenewLease(context.Context, RenewRequest) (Lease, error)
	ReleaseLease(context.Context, string, string, int64) error
}

func BuildDefinition(definition Definition) (Definition, error) {
	definition.Schema = DefinitionSchema
	definition.Tools = normalizeTools(definition.Tools)
	definition.Runtimes = normalizeBindings(definition.Runtimes)
	definition.DefinitionDigest = ""
	if err := validateDefinition(definition); err != nil {
		return Definition{}, err
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		return Definition{}, err
	}
	definition.DefinitionDigest = hash(raw)
	return definition, nil
}

func VerifyDefinition(definition Definition) error {
	expected := definition.DefinitionDigest
	if !validDigest(expected) {
		return ErrInvalidNode
	}
	definition.DefinitionDigest = ""
	normalized, err := BuildDefinition(definition)
	if err != nil || normalized.DefinitionDigest != expected {
		return fmt.Errorf("%w: definition digest mismatch", ErrInvalidNode)
	}
	return nil
}

func Evaluate(definition Definition, observation Observation, requirements Requirements, now time.Time) Eligibility {
	reasons := []string{}
	if VerifyDefinition(definition) != nil || definition.Lifecycle != LifecycleActive {
		reasons = append(reasons, "node_definition_unavailable")
	}
	expectedNode := executioncontract.BindingReference{ID: definition.NodeID, Version: definition.Version, Digest: definition.DefinitionDigest}
	if observation.Node != expectedNode {
		reasons = append(reasons, "node_observation_identity_mismatch")
	}
	if !validCapacity(observation.Available) || observation.Available.CPUMillis > definition.Capacity.CPUMillis || observation.Available.MemoryBytes > definition.Capacity.MemoryBytes || observation.Available.DiskBytes > definition.Capacity.DiskBytes {
		reasons = append(reasons, "node_observation_capacity_invalid")
	}
	for _, tool := range observation.AvailableTools {
		if !containsTool(definition.Tools, tool) {
			reasons = append(reasons, "node_observation_tool_drift")
		}
	}
	for _, runtime := range observation.AvailableRuntimes {
		if !containsBinding(definition.Runtimes, runtime) {
			reasons = append(reasons, "node_observation_runtime_drift")
		}
	}
	if observation.Health != HealthHealthy {
		reasons = append(reasons, "node_health_unavailable")
	}
	if now.IsZero() || observation.ObservedAt.IsZero() || observation.ExpiresAt.IsZero() || now.Before(observation.ObservedAt) || !now.Before(observation.ExpiresAt) {
		reasons = append(reasons, "node_observation_stale")
	}
	if requirements.OS != "" && definition.OS != requirements.OS {
		reasons = append(reasons, "os_mismatch")
	}
	if requirements.Architecture != "" && definition.Architecture != requirements.Architecture {
		reasons = append(reasons, "architecture_mismatch")
	}
	if observation.Available.CPUMillis < requirements.Minimum.CPUMillis || observation.Available.MemoryBytes < requirements.Minimum.MemoryBytes || observation.Available.DiskBytes < requirements.Minimum.DiskBytes {
		reasons = append(reasons, "capacity_unavailable")
	}
	for _, tool := range normalizeTools(requirements.Tools) {
		if !containsTool(observation.AvailableTools, tool) {
			reasons = append(reasons, "tool_unavailable:"+tool.Name)
		}
	}
	if accessRank(definition.RepositoryAccess) < accessRank(requirements.RepositoryAccess) {
		reasons = append(reasons, "repository_access_unavailable")
	}
	if requirements.Runtime.ID != "" && !containsBinding(observation.AvailableRuntimes, requirements.Runtime) {
		reasons = append(reasons, "runtime_unavailable")
	}
	if observation.ActiveLeases >= definition.MaxActiveLeases {
		reasons = append(reasons, "lease_limit_reached")
	}
	reasons = uniqueStrings(reasons)
	return Eligibility{Eligible: len(reasons) == 0, Reasons: reasons}
}

func validateDefinition(definition Definition) error {
	if definition.Schema != DefinitionSchema || !namespaced(definition.NodeID, "node:") || definition.Version < 1 ||
		(definition.Lifecycle != LifecycleActive && definition.Lifecycle != LifecycleDisabled) || strings.TrimSpace(definition.OS) == "" ||
		strings.TrimSpace(definition.Architecture) == "" || !validCapacity(definition.Capacity) || definition.MaxActiveLeases < 1 ||
		accessRank(definition.RepositoryAccess) < 0 || len(definition.Tools) > 256 || len(definition.Runtimes) == 0 || len(definition.Runtimes) > 256 {
		return ErrInvalidNode
	}
	for _, tool := range definition.Tools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Version) == "" || !validDigest(tool.Digest) {
			return ErrInvalidNode
		}
	}
	for index := 1; index < len(definition.Tools); index++ {
		if definition.Tools[index-1].Name == definition.Tools[index].Name {
			return ErrInvalidNode
		}
	}
	for _, runtime := range definition.Runtimes {
		if !validBinding(runtime, "runtime:") {
			return ErrInvalidNode
		}
	}
	return nil
}

func validCapacity(capacity Capacity) bool {
	return capacity.CPUMillis >= 0 && capacity.MemoryBytes >= 0 && capacity.DiskBytes >= 0
}

func normalizeTools(values []Tool) []Tool {
	result := append([]Tool(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		if result[i].Version != result[j].Version {
			return result[i].Version < result[j].Version
		}
		return result[i].Digest < result[j].Digest
	})
	unique := result[:0]
	for _, value := range result {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func normalizeBindings(values []executioncontract.BindingReference) []executioncontract.BindingReference {
	result := append([]executioncontract.BindingReference(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		if result[i].Version != result[j].Version {
			return result[i].Version < result[j].Version
		}
		return result[i].Digest < result[j].Digest
	})
	unique := result[:0]
	for _, value := range result {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func containsTool(values []Tool, target Tool) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsBinding(values []executioncontract.BindingReference, target executioncontract.BindingReference) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func accessRank(access RepositoryAccess) int {
	switch access {
	case RepositoryNone:
		return 0
	case RepositoryRead:
		return 1
	case RepositoryWrite:
		return 2
	default:
		return -1
	}
}

func validBinding(value executioncontract.BindingReference, prefix string) bool {
	return namespaced(value.ID, prefix) && value.Version > 0 && validDigest(value.Digest)
}

func namespaced(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && len(value) <= 128
}

func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func uniqueStrings(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
