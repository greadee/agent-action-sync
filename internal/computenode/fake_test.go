package computenode

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"syncgate/internal/executioncontract"
)

func TestDefinitionEligibilityAndObservationExpiryAreDeterministic(t *testing.T) {
	runtimeRef := nodeBinding("runtime:fake", "a")
	tool := Tool{Name: "go", Version: "1.25", Digest: nodeDigest("b")}
	definition, err := BuildDefinition(Definition{
		NodeID: "node:one", Version: 1, Lifecycle: LifecycleActive, OS: "windows", Architecture: "amd64",
		Capacity: Capacity{CPUMillis: 4000, MemoryBytes: 8 << 30, DiskBytes: 100 << 30}, Tools: []Tool{tool},
		RepositoryAccess: RepositoryWrite, Runtimes: []executioncontract.BindingReference{runtimeRef}, MaxActiveLeases: 2,
	})
	if err != nil || VerifyDefinition(definition) != nil {
		t.Fatalf("definition=%+v err=%v", definition, err)
	}
	reversed, err := BuildDefinition(Definition{
		NodeID: definition.NodeID, Version: 1, Lifecycle: LifecycleActive, OS: "windows", Architecture: "amd64", Capacity: definition.Capacity,
		Tools: []Tool{tool}, RepositoryAccess: RepositoryWrite, Runtimes: []executioncontract.BindingReference{runtimeRef, runtimeRef}, MaxActiveLeases: 2,
	})
	if err != nil || reversed.DefinitionDigest != definition.DefinitionDigest || !reflect.DeepEqual(reversed.Tools, definition.Tools) {
		t.Fatalf("normalized definition=%+v err=%v", reversed, err)
	}
	when := time.Date(2026, time.August, 17, 19, 0, 0, 0, time.UTC)
	observation := Observation{
		Node:   executioncontract.BindingReference{ID: definition.NodeID, Version: definition.Version, Digest: definition.DefinitionDigest},
		Health: HealthHealthy, Available: Capacity{CPUMillis: 2000, MemoryBytes: 4 << 30, DiskBytes: 50 << 30},
		AvailableTools: []Tool{tool}, AvailableRuntimes: []executioncontract.BindingReference{runtimeRef}, ObservedAt: when, ExpiresAt: when.Add(time.Minute),
	}
	requirements := Requirements{OS: "windows", Architecture: "amd64", Minimum: Capacity{CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 1 << 30}, Tools: []Tool{tool}, RepositoryAccess: RepositoryWrite, Runtime: runtimeRef}
	if eligibility := Evaluate(definition, observation, requirements, when.Add(time.Second)); !eligibility.Eligible {
		t.Fatalf("eligibility=%+v", eligibility)
	}
	if eligibility := Evaluate(definition, observation, requirements, observation.ExpiresAt); eligibility.Eligible || !containsReason(eligibility.Reasons, "node_observation_stale") {
		t.Fatalf("expired eligibility=%+v", eligibility)
	}
}

func TestDeterministicFakeEnforcesLeaseLimitAndFencing(t *testing.T) {
	runtimeRef := nodeBinding("runtime:fake", "a")
	definition, _ := BuildDefinition(Definition{NodeID: "node:one", Version: 1, Lifecycle: LifecycleActive, OS: "linux", Architecture: "amd64", Capacity: Capacity{}, RepositoryAccess: RepositoryRead, Runtimes: []executioncontract.BindingReference{runtimeRef}, MaxActiveLeases: 1})
	when := time.Date(2026, time.August, 17, 19, 0, 0, 0, time.UTC)
	nodeRef := executioncontract.BindingReference{ID: definition.NodeID, Version: definition.Version, Digest: definition.DefinitionDigest}
	fake, err := NewDeterministicFake(FakeConfig{Definition: definition, Observation: Observation{Node: nodeRef, Health: HealthHealthy, Available: definition.Capacity, AvailableRuntimes: []executioncontract.BindingReference{runtimeRef}, ObservedAt: when, ExpiresAt: when.Add(time.Hour)}, Now: func() time.Time { return when }})
	if err != nil {
		t.Fatal(err)
	}
	request := LeaseRequest{Node: nodeRef, OwnerID: "assignment:one", Duration: time.Minute, IdempotencyKeyDigest: nodeDigest("1")}
	lease, err := fake.AcquireLease(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := fake.AcquireLease(context.Background(), request); err != nil || replay != lease {
		t.Fatalf("lease replay=%+v err=%v", replay, err)
	}
	second := request
	second.OwnerID = "assignment:two"
	second.IdempotencyKeyDigest = nodeDigest("2")
	if _, err := fake.AcquireLease(context.Background(), second); !errorsIs(err, ErrNodeUnavailable) {
		t.Fatalf("lease limit error=%v", err)
	}
	renewed, err := fake.RenewLease(context.Background(), RenewRequest{LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, Generation: lease.Generation, Duration: time.Minute, IdempotencyKeyDigest: nodeDigest("3")})
	if err != nil || renewed.Generation != 2 {
		t.Fatalf("renewed=%+v err=%v", renewed, err)
	}
	if err := fake.ReleaseLease(context.Background(), renewed.LeaseID, renewed.OwnerID, 1); !errorsIs(err, ErrLeaseConflict) {
		t.Fatalf("stale release error=%v", err)
	}
	if err := fake.ReleaseLease(context.Background(), renewed.LeaseID, renewed.OwnerID, renewed.Generation); err != nil {
		t.Fatal(err)
	}
}

func nodeBinding(id, seed string) executioncontract.BindingReference {
	return executioncontract.BindingReference{ID: id, Version: 1, Digest: nodeDigest(seed)}
}
func nodeDigest(seed string) string { return strings.Repeat(seed, 64) }
func containsReason(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func errorsIs(err, target error) bool { return err == target }
