package pairing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestPairingAcceptanceIsExplicitAuditedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := pairingTestStore(t)
	local := pairingIdentity(t, 1)
	peer := pairingIdentity(t, 2)
	shareID := core.ShareID("drop")
	seedPairingState(t, store, local, shareID)
	now := time.Unix(1_000, 0).UTC()
	invite := pairingInvite(t, peer, now, []core.Capability{core.CapabilityRead, core.CapabilityDelete})
	encoded, err := identity.EncodePairingInvite(invite)
	if err != nil {
		t.Fatalf("EncodePairingInvite: %v", err)
	}
	service := Service{Pairings: store.Pairings(), Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{3}, 64))}
	request := AcceptRequest{
		LocalDeviceID: local.DeviceID, EncodedInvite: encoded,
		ExpectedFingerprint: peer.Fingerprint, OneTimeCode: invite.OneTimeCode,
		Grants:            []Grant{{ShareID: shareID, Capabilities: []core.Capability{core.CapabilityUpload}, LANOnly: true}},
		ControlPlaneGrant: &ControlPlaneGrant{ReadStatus: true, TTL: 24 * time.Hour},
	}

	accepted, err := service.Accept(ctx, request)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.AlreadyAccepted || accepted.Peer.DeviceID != peer.DeviceID {
		t.Fatalf("acceptance result = %+v", accepted)
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityUpload, false); err != nil {
		t.Fatalf("explicit upload grant was denied: %v", err)
	}
	for _, capability := range []core.Capability{core.CapabilityRead, core.CapabilityDelete, core.CapabilitySync} {
		if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, capability, false); err == nil {
			t.Fatalf("non-explicit %s capability was granted", capability)
		}
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityUpload, true); err == nil {
		t.Fatal("LAN-only grant allowed remote authorization")
	}
	statusGrant, err := store.NodeStatus().GetControlPlaneGrant(ctx, peer.DeviceID)
	if err != nil || !statusGrant.ReadStatus || !statusGrant.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("control-plane status grant = %+v, err=%v", statusGrant, err)
	}

	request.Grants = []Grant{{ShareID: shareID, Capabilities: []core.Capability{core.CapabilityRead}}}
	repeated, err := service.Accept(ctx, request)
	if err != nil {
		t.Fatalf("repeat Accept: %v", err)
	}
	if !repeated.AlreadyAccepted {
		t.Fatal("duplicate invite was not reported as already accepted")
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityRead, false); err == nil {
		t.Fatal("duplicate acceptance changed the original capabilities")
	}
	events, err := store.Audit().ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(events) != 1 || events[0].EventName != AuditPairingAccepted || strings.Contains(events[0].Metadata["grants"], "drop=read") || !strings.Contains(events[0].Metadata["grants"], "control_plane=read_status") {
		t.Fatalf("pairing audit events = %+v", events)
	}
}

func TestPairingAcceptanceFailuresDoNotChangeTrustOrPermissions(t *testing.T) {
	tests := []struct {
		name        string
		advance     time.Duration
		fingerprint string
		code        string
		want        error
	}{
		{name: "expired", advance: time.Minute, want: identity.ErrPairingInviteExpired},
		{name: "fingerprint mismatch", fingerprint: "WRONG", want: identity.ErrPairingFingerprintMismatch},
		{name: "code mismatch", code: "WRONG-CODE", want: identity.ErrPairingCodeMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := pairingTestStore(t)
			local := pairingIdentity(t, 4)
			peer := pairingIdentity(t, 5)
			shareID := core.ShareID("drop")
			seedPairingState(t, store, local, shareID)
			now := time.Unix(2_000, 0).UTC()
			invite := pairingInvite(t, peer, now, nil)
			encoded, err := identity.EncodePairingInvite(invite)
			if err != nil {
				t.Fatalf("EncodePairingInvite: %v", err)
			}
			fingerprint := peer.Fingerprint
			if test.fingerprint != "" {
				fingerprint = test.fingerprint
			}
			code := invite.OneTimeCode
			if test.code != "" {
				code = test.code
			}
			service := Service{Pairings: store.Pairings(), Now: func() time.Time { return now.Add(test.advance) }, Random: bytes.NewReader(bytes.Repeat([]byte{6}, 32))}
			_, err = service.Accept(ctx, AcceptRequest{
				LocalDeviceID: local.DeviceID, EncodedInvite: encoded,
				ExpectedFingerprint: fingerprint, OneTimeCode: code,
				Grants: []Grant{{ShareID: shareID, Capabilities: []core.Capability{core.CapabilityUpload}}},
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Accept error = %v, want %v", err, test.want)
			}
			if _, err := store.Devices().GetDevice(ctx, peer.DeviceID); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("failed acceptance changed peer trust: %v", err)
			}
			if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityUpload, false); err == nil {
				t.Fatal("failed acceptance created a permission")
			}
			events, err := store.Audit().ListRecent(ctx, 10)
			if err != nil || len(events) != 0 {
				t.Fatalf("failed acceptance audit state = %+v, err=%v", events, err)
			}
		})
	}
}

func TestPairingAcceptanceRollsBackWhenGrantCannotBeStored(t *testing.T) {
	ctx := context.Background()
	store := pairingTestStore(t)
	local := pairingIdentity(t, 7)
	peer := pairingIdentity(t, 8)
	seedPairingState(t, store, local, "drop")
	now := time.Unix(3_000, 0).UTC()
	invite := pairingInvite(t, peer, now, nil)
	encoded, _ := identity.EncodePairingInvite(invite)
	service := Service{Pairings: store.Pairings(), Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{9}, 32))}

	_, err := service.Accept(ctx, AcceptRequest{
		LocalDeviceID: local.DeviceID, EncodedInvite: encoded,
		ExpectedFingerprint: peer.Fingerprint, OneTimeCode: invite.OneTimeCode,
		Grants: []Grant{{ShareID: "missing-share", Capabilities: []core.Capability{core.CapabilityRead}}},
	})
	if err == nil {
		t.Fatal("expected missing share grant to fail")
	}
	if _, err := store.Devices().GetDevice(ctx, peer.DeviceID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("failed transaction retained trusted device: %v", err)
	}
	events, _ := store.Audit().ListRecent(ctx, 10)
	if len(events) != 0 {
		t.Fatalf("failed transaction retained audit events: %+v", events)
	}
}

func TestPairingRevocationRemovesAccessAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := pairingTestStore(t)
	local := pairingIdentity(t, 10)
	peer := pairingIdentity(t, 11)
	shareID := core.ShareID("drop")
	seedPairingState(t, store, local, shareID)
	now := time.Unix(4_000, 0).UTC()
	invite := pairingInvite(t, peer, now, nil)
	encoded, _ := identity.EncodePairingInvite(invite)
	randomValues := append(bytes.Repeat([]byte{12}, 16), bytes.Repeat([]byte{13}, 16)...)
	randomValues = append(randomValues, bytes.Repeat([]byte{14}, 16)...)
	service := Service{Pairings: store.Pairings(), Now: func() time.Time { return now }, Random: bytes.NewReader(randomValues)}
	_, err := service.Accept(ctx, AcceptRequest{
		LocalDeviceID: local.DeviceID, EncodedInvite: encoded,
		ExpectedFingerprint: peer.Fingerprint, OneTimeCode: invite.OneTimeCode,
		Grants: []Grant{{ShareID: shareID, Capabilities: []core.Capability{core.CapabilityRead}}},
	})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	result, err := service.Revoke(ctx, local.DeviceID, peer.DeviceID)
	if err != nil || result.AlreadyRevoked {
		t.Fatalf("Revoke result = %+v, err=%v", result, err)
	}
	device, err := store.Devices().GetDevice(ctx, peer.DeviceID)
	if err != nil || device.TrustState != storage.TrustRevoked {
		t.Fatalf("revoked device = %+v, err=%v", device, err)
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityRead, false); err == nil {
		t.Fatal("revoked device retained share access")
	}
	repeated, err := service.Revoke(ctx, local.DeviceID, peer.DeviceID)
	if err != nil || !repeated.AlreadyRevoked {
		t.Fatalf("repeat Revoke result = %+v, err=%v", repeated, err)
	}
	events, err := store.Audit().ListRecent(ctx, 10)
	if err != nil || len(events) != 2 || events[0].EventName != AuditPairingRevoked {
		t.Fatalf("revocation audit events = %+v, err=%v", events, err)
	}
}

func TestPairingInvitationCreationIsAuditedWithoutCodeDisclosure(t *testing.T) {
	ctx := context.Background()
	store := pairingTestStore(t)
	local := pairingIdentity(t, 13)
	now := time.Unix(5_000, 0).UTC()
	service := Service{Audit: store.Audit(), Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{14}, 64))}
	created, err := service.CreateInvitation(ctx, local, "Laptop", time.Minute, []core.Capability{core.CapabilityRead})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if strings.Contains(created.Encoded, created.Invite.OneTimeCode) {
		t.Fatal("encoded invitation disclosed one-time code")
	}
	events, err := store.Audit().ListRecent(ctx, 10)
	if err != nil || len(events) != 1 || events[0].EventName != AuditInvitationCreated {
		t.Fatalf("invitation audit events = %+v, err=%v", events, err)
	}
	for _, value := range events[0].Metadata {
		if strings.Contains(value, created.Invite.OneTimeCode) {
			t.Fatal("invitation audit metadata disclosed one-time code")
		}
	}
}

func pairingTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "syncgate.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return store
}

func pairingIdentity(t *testing.T, value byte) identity.DeviceIdentity {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{value}, ed25519.SeedSize))
	deviceIdentity, err := identity.FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	return deviceIdentity
}

func pairingInvite(t *testing.T, peer identity.DeviceIdentity, now time.Time, requested []core.Capability) identity.PairingInvite {
	t.Helper()
	invite, err := identity.NewPairingInviteAt(peer, "Peer Laptop", time.Minute, requested, bytes.NewReader(bytes.Repeat([]byte{15}, identity.PairingCodeByteCount)), now)
	if err != nil {
		t.Fatalf("NewPairingInviteAt: %v", err)
	}
	return invite
}

func seedPairingState(t *testing.T, store *sqlite.Store, local identity.DeviceIdentity, shareID core.ShareID) {
	t.Helper()
	ctx := context.Background()
	if err := store.Devices().TrustDevice(ctx, storage.Device{
		ID: local.DeviceID, DisplayName: "Local", PublicKey: local.PublicKey,
		Fingerprint: local.Fingerprint, TrustState: storage.TrustTrusted,
	}); err != nil {
		t.Fatalf("TrustDevice: %v", err)
	}
	if err := store.Shares().SaveShare(ctx, storage.Share{
		ID: shareID, Name: "Drop", RootPath: t.TempDir(), Mode: storage.ShareUploadOnly,
	}); err != nil {
		t.Fatalf("SaveShare: %v", err)
	}
}
