package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"syncgate/internal/core"
)

const (
	DeviceIDGroups       = 4
	DeviceIDGroupLength  = 4
	PairingCodeByteCount = 10
)

type DeviceIdentity struct {
	DeviceID   core.DeviceID
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
	Fingerprint string
}

func GenerateDeviceIdentity(reader io.Reader) (DeviceIdentity, error) {
	if reader == nil {
		reader = rand.Reader
	}

	publicKey, privateKey, err := ed25519.GenerateKey(reader)
	if err != nil {
		return DeviceIdentity{}, fmt.Errorf("generate device key: %w", err)
	}
	return FromKeyPair(publicKey, privateKey)
}

func FromKeyPair(publicKey ed25519.PublicKey, privateKey ed25519.PrivateKey) (DeviceIdentity, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return DeviceIdentity{}, fmt.Errorf("public key must be %d bytes", ed25519.PublicKeySize)
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return DeviceIdentity{}, fmt.Errorf("private key must be %d bytes", ed25519.PrivateKeySize)
	}
	derivedPublic := privateKey.Public().(ed25519.PublicKey)
	if !publicKeysEqual(publicKey, derivedPublic) {
		return DeviceIdentity{}, errors.New("public key does not match private key")
	}

	fingerprint := Fingerprint(publicKey)
	return DeviceIdentity{
		DeviceID:    DeviceIDFromFingerprint(fingerprint),
		PublicKey:   append(ed25519.PublicKey(nil), publicKey...),
		PrivateKey:  append(ed25519.PrivateKey(nil), privateKey...),
		Fingerprint: fingerprint,
	}, nil
}

func Fingerprint(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	encoded := strings.ToUpper(hex.EncodeToString(sum[:]))
	groups := make([]string, 0, 8)
	for i := 0; i < len(encoded); i += 8 {
		end := i + 8
		if end > len(encoded) {
			end = len(encoded)
		}
		groups = append(groups, encoded[i:end])
	}
	return strings.Join(groups, "-")
}

func DeviceIDFromFingerprint(fingerprint string) core.DeviceID {
	clean := strings.NewReplacer("-", "", " ", "").Replace(strings.ToUpper(fingerprint))
	if len(clean) < DeviceIDGroups*DeviceIDGroupLength {
		return core.DeviceID(clean)
	}

	parts := make([]string, 0, DeviceIDGroups)
	for i := 0; i < DeviceIDGroups; i++ {
		start := i * DeviceIDGroupLength
		parts = append(parts, clean[start:start+DeviceIDGroupLength])
	}
	return core.DeviceID(strings.Join(parts, "-"))
}

func GeneratePairingCode(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}

	buf := make([]byte, PairingCodeByteCount)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", fmt.Errorf("generate pairing code: %w", err)
	}

	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	if len(code) <= 4 {
		return code, nil
	}
	return code[:4] + "-" + code[4:8] + "-" + code[8:12] + "-" + code[12:]
}

func publicKeysEqual(a, b ed25519.PublicKey) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
