package desktop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"syncgate/internal/computenode"
	"syncgate/internal/executioncontract"
)

func TestLocalNodeEnforcesConfiguredConcurrencyAndReportsResources(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	runtimeRef := executioncontract.BindingReference{ID: "runtime:local", Version: 1, Digest: strings.Repeat("a", 64)}
	provider, err := NewLocalNodeProvider(LocalNodeOptions{
		NodeID: "node:local-test", Runtime: runtimeRef, ResourceRoot: t.TempDir(), MaxConcurrent: 1,
		Now: func() time.Time { return now },
		Observe: func(_ string, ceiling int, observedAt time.Time) (MachineResources, error) {
			return MachineResources{CPUMillis: 8000, LogicalCPUs: 8, DiskTotalBytes: 1000, DiskAvailableBytes: 600, ConfiguredConcurrency: ceiling, ObservedAt: observedAt, ExpiresAt: observedAt.Add(30 * time.Second)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := provider.Definition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if definition.MaxActiveLeases != 1 {
		t.Fatalf("max leases = %d", definition.MaxActiveLeases)
	}
	nodeRef := executioncontract.BindingReference{ID: definition.NodeID, Version: definition.Version, Digest: definition.DefinitionDigest}
	first, err := provider.AcquireLease(context.Background(), computenode.LeaseRequest{Node: nodeRef, OwnerID: "assignment:one", Duration: time.Minute, IdempotencyKeyDigest: strings.Repeat("1", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.AcquireLease(context.Background(), computenode.LeaseRequest{Node: nodeRef, OwnerID: "assignment:two", Duration: time.Minute, IdempotencyKeyDigest: strings.Repeat("2", 64)}); !errors.Is(err, computenode.ErrNodeUnavailable) {
		t.Fatalf("second lease error = %v", err)
	}
	observation, err := provider.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.ActiveLeases != 1 || observation.Available.CPUMillis != 8000 || observation.Available.DiskBytes != 600 {
		t.Fatalf("observation = %+v", observation)
	}
	if err := provider.ReleaseLease(context.Background(), first.LeaseID, first.OwnerID, first.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.AcquireLease(context.Background(), computenode.LeaseRequest{Node: nodeRef, OwnerID: "assignment:two", Duration: time.Minute, IdempotencyKeyDigest: strings.Repeat("2", 64)}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalNodeRejectsConcurrencyAboveDesktopCeiling(t *testing.T) {
	_, err := NewLocalNodeProvider(LocalNodeOptions{NodeID: "node:local-test", MaxConcurrent: 3, Now: time.Now})
	if !errors.Is(err, computenode.ErrInvalidNode) {
		t.Fatalf("error = %v", err)
	}
}
