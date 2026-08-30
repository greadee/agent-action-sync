package nodestatus

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/identity"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestTwoNodeStatusReplicationIsSignedRevocableAndStaleAware(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "two-node.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	local := testIdentity(t, 41)
	remote := testIdentity(t, 42)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.Devices().TrustDevice(ctx, storage.Device{ID: remote.DeviceID, DisplayName: "Paired node", PublicKey: remote.PublicKey, Fingerprint: remote.Fingerprint, TrustState: storage.TrustTrusted}); err != nil {
		t.Fatalf("TrustDevice: %v", err)
	}
	if err := store.NodeStatus().SaveControlPlaneGrant(ctx, storage.ControlPlaneGrant{DeviceID: remote.DeviceID, ReadStatus: true, GrantedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("SaveControlPlaneGrant: %v", err)
	}
	receiver := Receiver{Devices: store.Devices(), Store: store.NodeStatus(), Now: func() time.Time { return now }}
	snapshot := testSnapshot(remote, now, 1, digest("revision-1"))
	envelope, err := Build(remote, snapshot)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	wire, err := EncodeEnvelope(envelope)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	envelope, err = DecodeEnvelope(wire)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if _, err := receiver.Apply(ctx, envelope); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	visible, err := store.NodeStatus().ListVisibleNodeStatusReplicas(ctx, now, 10)
	if err != nil || len(visible) != 1 || visible[0].DeviceID != remote.DeviceID {
		t.Fatalf("visible replicas = %+v, err=%v", visible, err)
	}

	stale := testSnapshot(remote, now, 2, snapshot.Watermark)
	staleEnvelope, _ := Build(remote, stale)
	if _, err := receiver.Apply(ctx, staleEnvelope); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("stale watermark error = %v", err)
	}
	lower := testSnapshot(remote, now, 1, digest("older-revision"))
	lowerEnvelope, _ := Build(remote, lower)
	if _, err := receiver.Apply(ctx, lowerEnvelope); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("stale revision error = %v", err)
	}

	offline, err := store.NodeStatus().ListVisibleNodeStatusReplicas(ctx, now.Add(2*time.Minute), 10)
	if err != nil || len(offline) != 1 || offline[0].ExpiresAt.After(now.Add(2*time.Minute)) {
		t.Fatalf("offline replica = %+v, err=%v", offline, err)
	}
	expiredGrant, err := store.NodeStatus().ListVisibleNodeStatusReplicas(ctx, now.Add(2*time.Hour), 10)
	if err != nil || len(expiredGrant) != 0 {
		t.Fatalf("expired grant retained visibility: %+v, err=%v", expiredGrant, err)
	}
	_, err = store.Pairings().Revoke(ctx, storage.PairingRevocation{
		DeviceID: remote.DeviceID, RevokedAt: now.Add(3 * time.Minute),
		AuditEvent: storage.AuditEvent{ID: "audit-revoke-status", EventName: "pairing.revoked", DeviceID: local.DeviceID, PeerDeviceID: remote.DeviceID, Severity: "warning", OccurredAt: now.Add(3 * time.Minute)},
	})
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	visible, err = store.NodeStatus().ListVisibleNodeStatusReplicas(ctx, now.Add(3*time.Minute), 10)
	if err != nil || len(visible) != 0 {
		t.Fatalf("revoked replica remained visible: %+v, err=%v", visible, err)
	}
	if _, err := receiver.Apply(ctx, envelope); err == nil {
		t.Fatal("revoked node status was accepted")
	}
}

func TestNodeStatusRejectsTamperingAndSensitiveProjectNames(t *testing.T) {
	remote := testIdentity(t, 43)
	now := time.Now().UTC().Truncate(time.Millisecond)
	snapshot := testSnapshot(remote, now, 1, digest("signed"))
	envelope, err := Build(remote, snapshot)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	envelope.Snapshot.Health = "unhealthy"
	payload, computed, err := encodeSnapshot(envelope.Snapshot)
	if err != nil || computed == envelope.Digest || ed25519.Verify(remote.PublicKey, payload, mustSignature(t, envelope.Signature)) {
		t.Fatal("tampered snapshot retained a valid digest or signature")
	}
	snapshot.Projects[0].DisplayName = `C:\secret\workspace`
	if err := ValidateSnapshot(snapshot, now); err == nil {
		t.Fatal("absolute-path-like project name was accepted")
	}
	if _, err := DecodeEnvelope(append([]byte(`{"snapshot":{}}`), []byte(` {}`)...)); err == nil {
		t.Fatal("trailing envelope data was accepted")
	}
}

func testIdentity(t *testing.T, value byte) identity.DeviceIdentity {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{value}, ed25519.SeedSize))
	result, err := identity.FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	return result
}

func testSnapshot(remote identity.DeviceIdentity, now time.Time, revision int64, watermark string) Snapshot {
	return Snapshot{Protocol: Protocol, SourceDeviceID: remote.DeviceID, Revision: revision, Watermark: watermark, Health: "healthy", Lifecycle: "running", ObservedAt: now, ExpiresAt: now.Add(time.Minute), Projects: []ProjectSummary{{ProjectID: "project:demo", DisplayName: "demo", SchedulerState: "running", AssignmentCounts: []StatusCount{{State: "running", Count: 1}}, AcceptedHistoryWatermark: digest("accepted")}}}
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func mustSignature(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	return decoded
}
