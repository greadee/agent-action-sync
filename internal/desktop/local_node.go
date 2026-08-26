package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"time"

	"syncgate/internal/computenode"
	"syncgate/internal/executioncontract"
)

type LocalNodeOptions struct {
	NodeID        string
	Runtime       executioncontract.BindingReference
	ResourceRoot  string
	MaxConcurrent int
	Now           func() time.Time
	Observe       ResourceObserver
}

type LocalNodeProvider struct {
	mu           sync.Mutex
	definition   computenode.Definition
	resourceRoot string
	max          int
	now          func() time.Time
	observe      ResourceObserver
	leases       map[string]computenode.Lease
	acquisitions map[string]localLeaseReplay
	renewals     map[string]localLeaseReplay
}

type localLeaseReplay struct {
	fingerprint string
	lease       computenode.Lease
}

func NewLocalNodeProvider(options LocalNodeOptions) (*LocalNodeProvider, error) {
	if !strings.HasPrefix(options.NodeID, "node:") || options.MaxConcurrent < 1 || options.MaxConcurrent > 2 || options.Now == nil {
		return nil, computenode.ErrInvalidNode
	}
	observer := options.Observe
	if observer == nil {
		observer = ObserveMachineResources
	}
	resources, err := observer(options.ResourceRoot, options.MaxConcurrent, options.Now().UTC())
	if err != nil {
		return nil, err
	}
	definition, err := computenode.BuildDefinition(computenode.Definition{
		NodeID: options.NodeID, Version: 1, Lifecycle: computenode.LifecycleActive,
		OS: runtime.GOOS, Architecture: runtime.GOARCH,
		Capacity:         computenode.Capacity{CPUMillis: resources.CPUMillis, DiskBytes: resources.DiskTotalBytes},
		RepositoryAccess: computenode.RepositoryWrite,
		Runtimes:         []executioncontract.BindingReference{options.Runtime}, MaxActiveLeases: int64(options.MaxConcurrent),
	})
	if err != nil {
		return nil, err
	}
	return &LocalNodeProvider{
		definition: definition, resourceRoot: options.ResourceRoot, max: options.MaxConcurrent,
		now: options.Now, observe: observer, leases: map[string]computenode.Lease{},
		acquisitions: map[string]localLeaseReplay{}, renewals: map[string]localLeaseReplay{},
	}, nil
}

func (provider *LocalNodeProvider) Definition(ctx context.Context) (computenode.Definition, error) {
	if err := localContext(ctx); err != nil {
		return computenode.Definition{}, err
	}
	return provider.definition, nil
}

func (provider *LocalNodeProvider) Observe(ctx context.Context) (computenode.Observation, error) {
	if err := localContext(ctx); err != nil {
		return computenode.Observation{}, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.expireLocked()
	resources, err := provider.observe(provider.resourceRoot, provider.max, provider.now().UTC())
	if err != nil {
		return computenode.Observation{}, err
	}
	ref := executioncontract.BindingReference{ID: provider.definition.NodeID, Version: provider.definition.Version, Digest: provider.definition.DefinitionDigest}
	return computenode.Observation{
		Node: ref, Health: computenode.HealthHealthy,
		Available:         computenode.Capacity{CPUMillis: resources.CPUMillis, DiskBytes: resources.DiskAvailableBytes},
		AvailableRuntimes: append([]executioncontract.BindingReference(nil), provider.definition.Runtimes...),
		ActiveLeases:      int64(len(provider.leases)), ObservedAt: resources.ObservedAt, ExpiresAt: resources.ExpiresAt,
	}, nil
}

func (provider *LocalNodeProvider) AcquireLease(ctx context.Context, request computenode.LeaseRequest) (computenode.Lease, error) {
	if err := localContext(ctx); err != nil {
		return computenode.Lease{}, err
	}
	want := executioncontract.BindingReference{ID: provider.definition.NodeID, Version: provider.definition.Version, Digest: provider.definition.DefinitionDigest}
	if request.Node != want || !strings.HasPrefix(request.OwnerID, "assignment:") || request.Duration <= 0 || !localDigest(request.IdempotencyKeyDigest) {
		return computenode.Lease{}, computenode.ErrInvalidNode
	}
	fingerprint := localFingerprint(request)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if replay, ok := provider.acquisitions[request.IdempotencyKeyDigest]; ok {
		if replay.fingerprint != fingerprint {
			return computenode.Lease{}, computenode.ErrLeaseConflict
		}
		return replay.lease, nil
	}
	provider.expireLocked()
	if len(provider.leases) >= provider.max {
		return computenode.Lease{}, computenode.ErrNodeUnavailable
	}
	now := provider.now().UTC()
	lease := computenode.Lease{LeaseID: "lease:" + localHash(request.OwnerID, request.IdempotencyKeyDigest)[:32], Node: want, OwnerID: request.OwnerID, Generation: 1, AcquiredAt: now, ExpiresAt: now.Add(request.Duration)}
	provider.leases[lease.LeaseID] = lease
	provider.acquisitions[request.IdempotencyKeyDigest] = localLeaseReplay{fingerprint: fingerprint, lease: lease}
	return lease, nil
}

func (provider *LocalNodeProvider) RenewLease(ctx context.Context, request computenode.RenewRequest) (computenode.Lease, error) {
	if err := localContext(ctx); err != nil {
		return computenode.Lease{}, err
	}
	if !strings.HasPrefix(request.LeaseID, "lease:") || !strings.HasPrefix(request.OwnerID, "assignment:") || request.Generation < 1 || request.Duration <= 0 || !localDigest(request.IdempotencyKeyDigest) {
		return computenode.Lease{}, computenode.ErrInvalidNode
	}
	fingerprint := localFingerprint(request)
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if replay, ok := provider.renewals[request.IdempotencyKeyDigest]; ok {
		if replay.fingerprint != fingerprint {
			return computenode.Lease{}, computenode.ErrLeaseConflict
		}
		return replay.lease, nil
	}
	provider.expireLocked()
	lease, ok := provider.leases[request.LeaseID]
	if !ok {
		return computenode.Lease{}, computenode.ErrLeaseNotFound
	}
	if lease.OwnerID != request.OwnerID || lease.Generation != request.Generation {
		return computenode.Lease{}, computenode.ErrLeaseConflict
	}
	lease.Generation++
	lease.ExpiresAt = provider.now().UTC().Add(request.Duration)
	provider.leases[lease.LeaseID] = lease
	provider.renewals[request.IdempotencyKeyDigest] = localLeaseReplay{fingerprint: fingerprint, lease: lease}
	return lease, nil
}

func (provider *LocalNodeProvider) ReleaseLease(ctx context.Context, leaseID, ownerID string, generation int64) error {
	if err := localContext(ctx); err != nil {
		return err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.expireLocked()
	lease, ok := provider.leases[leaseID]
	if !ok {
		return computenode.ErrLeaseNotFound
	}
	if lease.OwnerID != ownerID || lease.Generation != generation {
		return computenode.ErrLeaseConflict
	}
	delete(provider.leases, leaseID)
	return nil
}

func (provider *LocalNodeProvider) expireLocked() {
	now := provider.now().UTC()
	for id, lease := range provider.leases {
		if !now.Before(lease.ExpiresAt) {
			delete(provider.leases, id)
		}
	}
}

func localContext(ctx context.Context) error {
	if ctx == nil {
		return computenode.ErrInvalidNode
	}
	return ctx.Err()
}

func localFingerprint(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func localHash(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func localDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

var _ computenode.Provider = (*LocalNodeProvider)(nil)
