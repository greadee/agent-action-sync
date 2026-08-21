package computenode

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"syncgate/internal/executioncontract"
)

type FakeConfig struct {
	Definition  Definition
	Observation Observation
	Now         func() time.Time
}

type DeterministicFake struct {
	mu           sync.Mutex
	definition   Definition
	observation  Observation
	now          func() time.Time
	leases       map[string]Lease
	acquisitions map[string]leaseReplay
	renewals     map[string]leaseReplay
}

type leaseReplay struct {
	fingerprint string
	lease       Lease
}

func NewDeterministicFake(config FakeConfig) (*DeterministicFake, error) {
	if VerifyDefinition(config.Definition) != nil || config.Now == nil {
		return nil, ErrInvalidNode
	}
	expected := executioncontract.BindingReference{ID: config.Definition.NodeID, Version: config.Definition.Version, Digest: config.Definition.DefinitionDigest}
	config.Observation.AvailableTools = normalizeTools(config.Observation.AvailableTools)
	config.Observation.AvailableRuntimes = normalizeBindings(config.Observation.AvailableRuntimes)
	if config.Observation.Node != expected || config.Observation.ActiveLeases < 0 || !validCapacity(config.Observation.Available) ||
		config.Observation.ObservedAt.IsZero() || config.Observation.ExpiresAt.IsZero() || !config.Observation.ObservedAt.Before(config.Observation.ExpiresAt) {
		return nil, ErrInvalidNode
	}
	for _, tool := range config.Observation.AvailableTools {
		if !containsTool(config.Definition.Tools, tool) {
			return nil, ErrInvalidNode
		}
	}
	for _, runtime := range config.Observation.AvailableRuntimes {
		if !containsBinding(config.Definition.Runtimes, runtime) {
			return nil, ErrInvalidNode
		}
	}
	return &DeterministicFake{definition: cloneDefinition(config.Definition), observation: cloneObservation(config.Observation), now: config.Now, leases: map[string]Lease{}, acquisitions: map[string]leaseReplay{}, renewals: map[string]leaseReplay{}}, nil
}

func (fake *DeterministicFake) Definition(ctx context.Context) (Definition, error) {
	if err := validContext(ctx); err != nil {
		return Definition{}, err
	}
	return cloneDefinition(fake.definition), nil
}

func (fake *DeterministicFake) Observe(ctx context.Context) (Observation, error) {
	if err := validContext(ctx); err != nil {
		return Observation{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.expire()
	observation := cloneObservation(fake.observation)
	observation.ActiveLeases = int64(len(fake.leases))
	return observation, nil
}

func (fake *DeterministicFake) AcquireLease(ctx context.Context, request LeaseRequest) (Lease, error) {
	if err := validContext(ctx); err != nil {
		return Lease{}, err
	}
	if request.Node != (executioncontract.BindingReference{ID: fake.definition.NodeID, Version: fake.definition.Version, Digest: fake.definition.DefinitionDigest}) ||
		!namespaced(request.OwnerID, "assignment:") || request.Duration <= 0 || !validDigest(request.IdempotencyKeyDigest) {
		return Lease{}, ErrInvalidNode
	}
	fingerprintRaw, _ := json.Marshal(request)
	fingerprint := hash(fingerprintRaw)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if prior, ok := fake.acquisitions[request.IdempotencyKeyDigest]; ok {
		if prior.fingerprint != fingerprint {
			return Lease{}, ErrLeaseConflict
		}
		return prior.lease, nil
	}
	fake.expire()
	observation := fake.observation
	observation.ActiveLeases = int64(len(fake.leases))
	if !Evaluate(fake.definition, observation, Requirements{}, fake.now().UTC()).Eligible {
		return Lease{}, ErrNodeUnavailable
	}
	when := fake.now().UTC()
	lease := Lease{
		LeaseID: "lease:" + hash([]byte(request.OwnerID + request.IdempotencyKeyDigest))[:32], Node: request.Node,
		OwnerID: request.OwnerID, Generation: 1, AcquiredAt: when, ExpiresAt: when.Add(request.Duration),
	}
	fake.leases[lease.LeaseID] = lease
	fake.acquisitions[request.IdempotencyKeyDigest] = leaseReplay{fingerprint: fingerprint, lease: lease}
	return lease, nil
}

func (fake *DeterministicFake) RenewLease(ctx context.Context, request RenewRequest) (Lease, error) {
	if err := validContext(ctx); err != nil {
		return Lease{}, err
	}
	if !namespaced(request.LeaseID, "lease:") || !namespaced(request.OwnerID, "assignment:") || request.Generation < 1 || request.Duration <= 0 || !validDigest(request.IdempotencyKeyDigest) {
		return Lease{}, ErrInvalidNode
	}
	fingerprintRaw, _ := json.Marshal(request)
	fingerprint := hash(fingerprintRaw)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if prior, ok := fake.renewals[request.IdempotencyKeyDigest]; ok {
		if prior.fingerprint != fingerprint {
			return Lease{}, ErrLeaseConflict
		}
		return prior.lease, nil
	}
	fake.expire()
	lease, ok := fake.leases[request.LeaseID]
	if !ok {
		return Lease{}, ErrLeaseNotFound
	}
	if lease.OwnerID != request.OwnerID || lease.Generation != request.Generation {
		return Lease{}, ErrLeaseConflict
	}
	lease.Generation++
	lease.ExpiresAt = fake.now().UTC().Add(request.Duration)
	fake.leases[lease.LeaseID] = lease
	fake.renewals[request.IdempotencyKeyDigest] = leaseReplay{fingerprint: fingerprint, lease: lease}
	return lease, nil
}

func (fake *DeterministicFake) ReleaseLease(ctx context.Context, leaseID, ownerID string, generation int64) error {
	if err := validContext(ctx); err != nil {
		return err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.expire()
	lease, ok := fake.leases[leaseID]
	if !ok {
		return ErrLeaseNotFound
	}
	if lease.OwnerID != ownerID || lease.Generation != generation {
		return ErrLeaseConflict
	}
	delete(fake.leases, leaseID)
	return nil
}

func (fake *DeterministicFake) expire() {
	now := fake.now().UTC()
	for leaseID, lease := range fake.leases {
		if !now.Before(lease.ExpiresAt) {
			delete(fake.leases, leaseID)
		}
	}
}

func validContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidNode
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func cloneDefinition(value Definition) Definition {
	value.Tools = append([]Tool(nil), value.Tools...)
	value.Runtimes = append([]executioncontract.BindingReference(nil), value.Runtimes...)
	return value
}

func cloneObservation(value Observation) Observation {
	value.AvailableTools = append([]Tool(nil), value.AvailableTools...)
	value.AvailableRuntimes = append([]executioncontract.BindingReference(nil), value.AvailableRuntimes...)
	return value
}
