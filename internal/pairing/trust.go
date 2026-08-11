package pairing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/storage"
)

var (
	ErrUnknownPeer          = errors.New("peer identity is not paired")
	ErrUntrustedPeer        = errors.New("peer identity is not trusted")
	ErrRevokedPeer          = errors.New("peer identity is revoked")
	ErrPeerIdentityMismatch = errors.New("peer identity does not match paired material")
)

type TrustedPeerVerifier struct {
	Devices storage.DeviceStore
}

func (verifier TrustedPeerVerifier) VerifyPeerIdentity(deviceID core.DeviceID, publicKey ed25519.PublicKey, fingerprint string) error {
	if verifier.Devices == nil {
		return errors.New("trusted device store is required")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: Ed25519 public key is malformed", ErrPeerIdentityMismatch)
	}
	derivedFingerprint := identity.Fingerprint(publicKey)
	derivedDeviceID := identity.DeviceIDFromFingerprint(derivedFingerprint)
	if deviceID != derivedDeviceID || fingerprint != derivedFingerprint {
		return fmt.Errorf("%w: derived identity metadata differs", ErrPeerIdentityMismatch)
	}
	device, err := verifier.Devices.GetDevice(context.Background(), deviceID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrUnknownPeer, deviceID)
		}
		return fmt.Errorf("load paired peer %s: %w", deviceID, err)
	}
	switch device.TrustState {
	case storage.TrustTrusted:
	case storage.TrustRevoked:
		return fmt.Errorf("%w: %s", ErrRevokedPeer, deviceID)
	default:
		return fmt.Errorf("%w: %s has state %s", ErrUntrustedPeer, deviceID, device.TrustState)
	}
	if device.Fingerprint != derivedFingerprint || !bytes.Equal(device.PublicKey, publicKey) {
		return fmt.Errorf("%w: stored key or fingerprint differs for %s", ErrPeerIdentityMismatch, deviceID)
	}
	return nil
}
