package pairing

import (
	"context"
	"errors"
	"testing"

	"syncgate/internal/storage"
)

func TestTrustedPeerVerifierAcceptsOnlyPinnedTrustedIdentity(t *testing.T) {
	ctx := context.Background()
	peer := pairingIdentity(t, 31)
	other := pairingIdentity(t, 32)

	t.Run("trusted", func(t *testing.T) {
		store := pairingTestStore(t)
		if err := store.Devices().TrustDevice(ctx, storage.Device{
			ID: peer.DeviceID, DisplayName: "Peer", PublicKey: peer.PublicKey,
			Fingerprint: peer.Fingerprint, TrustState: storage.TrustTrusted,
		}); err != nil {
			t.Fatalf("TrustDevice: %v", err)
		}
		if err := (TrustedPeerVerifier{Devices: store.Devices()}).VerifyPeerIdentity(peer.DeviceID, peer.PublicKey, peer.Fingerprint); err != nil {
			t.Fatalf("VerifyPeerIdentity: %v", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		store := pairingTestStore(t)
		err := (TrustedPeerVerifier{Devices: store.Devices()}).VerifyPeerIdentity(peer.DeviceID, peer.PublicKey, peer.Fingerprint)
		if !errors.Is(err, ErrUnknownPeer) {
			t.Fatalf("unknown peer error = %v, want %v", err, ErrUnknownPeer)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		store := pairingTestStore(t)
		if err := store.Devices().TrustDevice(ctx, storage.Device{
			ID: peer.DeviceID, DisplayName: "Peer", PublicKey: peer.PublicKey,
			Fingerprint: peer.Fingerprint, TrustState: storage.TrustRevoked,
		}); err != nil {
			t.Fatalf("TrustDevice: %v", err)
		}
		err := (TrustedPeerVerifier{Devices: store.Devices()}).VerifyPeerIdentity(peer.DeviceID, peer.PublicKey, peer.Fingerprint)
		if !errors.Is(err, ErrRevokedPeer) {
			t.Fatalf("revoked peer error = %v, want %v", err, ErrRevokedPeer)
		}
	})

	t.Run("mismatched pinned key", func(t *testing.T) {
		store := pairingTestStore(t)
		if err := store.Devices().TrustDevice(ctx, storage.Device{
			ID: peer.DeviceID, DisplayName: "Peer", PublicKey: other.PublicKey,
			Fingerprint: peer.Fingerprint, TrustState: storage.TrustTrusted,
		}); err != nil {
			t.Fatalf("TrustDevice: %v", err)
		}
		err := (TrustedPeerVerifier{Devices: store.Devices()}).VerifyPeerIdentity(peer.DeviceID, peer.PublicKey, peer.Fingerprint)
		if !errors.Is(err, ErrPeerIdentityMismatch) {
			t.Fatalf("mismatched peer error = %v, want %v", err, ErrPeerIdentityMismatch)
		}
	})
}
