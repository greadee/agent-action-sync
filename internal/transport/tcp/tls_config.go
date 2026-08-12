package tcp

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
)

const (
	ALPNProtocol                 = "syncgate/1"
	IdentityCertificateLifetime  = 24 * time.Hour
	identityCertificateClockSkew = 5 * time.Minute
)

var (
	ErrPeerCertificateInvalid     = errors.New("peer identity certificate is invalid")
	ErrPeerCertificateExpired     = errors.New("peer identity certificate is expired")
	ErrPeerCertificateNotYetValid = errors.New("peer identity certificate is not yet valid")
)

type PeerIdentityVerifier interface {
	VerifyPeerIdentity(deviceID core.DeviceID, publicKey ed25519.PublicKey, fingerprint string) error
}

type IdentityTLSConfigOptions struct {
	Identity     identity.DeviceIdentity
	PeerVerifier PeerIdentityVerifier
	Server       bool
}

func IdentityTLSConfig(options IdentityTLSConfigOptions) (*tls.Config, error) {
	now := time.Now().UTC()
	return identityTLSConfigAt(
		options,
		now.Add(-identityCertificateClockSkew),
		now.Add(IdentityCertificateLifetime),
		func() time.Time { return time.Now().UTC() },
		rand.Reader,
	)
}

func identityTLSConfigAt(options IdentityTLSConfigOptions, notBefore, notAfter time.Time, now func() time.Time, reader io.Reader) (*tls.Config, error) {
	if options.PeerVerifier == nil {
		return nil, errors.New("peer identity verifier is required")
	}
	if now == nil {
		return nil, errors.New("TLS verification clock is required")
	}
	certificate, err := identityCertificate(options.Identity, notBefore, notAfter, reader)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{
		Certificates:           []tls.Certificate{certificate},
		MinVersion:             tls.VersionTLS13,
		NextProtos:             []string{ALPNProtocol},
		SessionTicketsDisabled: true,
		// SyncGate pins paired Ed25519 keys instead of using the Web PKI. The
		// VerifyConnection callback below performs all peer certificate checks.
		InsecureSkipVerify: true,
	}
	if options.Server {
		config.ClientAuth = tls.RequireAnyClientCert
	}
	config.VerifyConnection = func(state tls.ConnectionState) error {
		return verifyPeerConnection(state, options.PeerVerifier, options.Server, now().UTC())
	}
	return config, nil
}

func identityCertificate(deviceIdentity identity.DeviceIdentity, notBefore, notAfter time.Time, reader io.Reader) (tls.Certificate, error) {
	if reader == nil {
		reader = rand.Reader
	}
	validated, err := identity.FromKeyPair(deviceIdentity.PublicKey, deviceIdentity.PrivateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("validate TLS device identity: %w", err)
	}
	if !notAfter.After(notBefore) {
		return tls.Certificate{}, errors.New("identity certificate expiry must follow its start time")
	}
	serialBytes := make([]byte, 16)
	if _, err := io.ReadFull(reader, serialBytes); err != nil {
		return tls.Certificate{}, fmt.Errorf("generate identity certificate serial: %w", err)
	}
	serial := new(big.Int).SetBytes(serialBytes)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{},
		NotBefore:             notBefore.UTC(),
		NotAfter:              notAfter.UTC(),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		SignatureAlgorithm:    x509.PureEd25519,
	}
	certificateDER, err := x509.CreateCertificate(reader, &template, &template, validated.PublicKey, validated.PrivateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create identity certificate: %w", err)
	}
	parsed, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse identity certificate: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{certificateDER},
		PrivateKey:  validated.PrivateKey,
		Leaf:        parsed,
	}, nil
}

func verifyPeerConnection(state tls.ConnectionState, verifier PeerIdentityVerifier, localIsServer bool, now time.Time) error {
	if state.NegotiatedProtocol != ALPNProtocol {
		return fmt.Errorf("%w: negotiated protocol %q", ErrPeerCertificateInvalid, state.NegotiatedProtocol)
	}
	if len(state.PeerCertificates) != 1 {
		return fmt.Errorf("%w: expected one peer certificate, got %d", ErrPeerCertificateInvalid, len(state.PeerCertificates))
	}
	certificate := state.PeerCertificates[0]
	if !certificate.NotAfter.After(certificate.NotBefore) ||
		certificate.NotAfter.Sub(certificate.NotBefore) > IdentityCertificateLifetime+identityCertificateClockSkew {
		return fmt.Errorf("%w: certificate lifetime exceeds policy", ErrPeerCertificateInvalid)
	}
	if now.Before(certificate.NotBefore) {
		return fmt.Errorf("%w: valid from %s", ErrPeerCertificateNotYetValid, certificate.NotBefore.UTC().Format(time.RFC3339))
	}
	if !now.Before(certificate.NotAfter) {
		return fmt.Errorf("%w: expired at %s", ErrPeerCertificateExpired, certificate.NotAfter.UTC().Format(time.RFC3339))
	}
	if certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return fmt.Errorf("%w: digital-signature key usage is required", ErrPeerCertificateInvalid)
	}
	requiredUsage := x509.ExtKeyUsageServerAuth
	if localIsServer {
		requiredUsage = x509.ExtKeyUsageClientAuth
	}
	if !hasExtendedKeyUsage(certificate, requiredUsage) {
		return fmt.Errorf("%w: required extended key usage is missing", ErrPeerCertificateInvalid)
	}
	publicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: peer key is not Ed25519", ErrPeerCertificateInvalid)
	}
	if err := certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature); err != nil {
		return fmt.Errorf("%w: certificate signature: %v", ErrPeerCertificateInvalid, err)
	}
	fingerprint := identity.Fingerprint(publicKey)
	deviceID := identity.DeviceIDFromFingerprint(fingerprint)
	if err := verifier.VerifyPeerIdentity(deviceID, publicKey, fingerprint); err != nil {
		return fmt.Errorf("verify paired peer %s: %w", deviceID, err)
	}
	return nil
}

func hasExtendedKeyUsage(certificate *x509.Certificate, required x509.ExtKeyUsage) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == required {
			return true
		}
	}
	return false
}
