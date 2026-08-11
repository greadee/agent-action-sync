package tcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/pairing"
	"syncgate/internal/storage"
)

func TestProductionTransportConnectsMutuallyTrustedAgents(t *testing.T) {
	serverIdentity := transportIdentity(t, 41)
	clientIdentity := transportIdentity(t, 42)
	serverDevices := newTransportDeviceStore(trustedTransportDevice(clientIdentity))
	clientDevices := newTransportDeviceStore(trustedTransportDevice(serverIdentity))
	serverTLS := transportTLSConfig(t, serverIdentity, serverDevices, true, time.Now().UTC())
	clientTLS := transportTLSConfig(t, clientIdentity, clientDevices, false, time.Now().UTC())
	if serverTLS.Certificates[0].Leaf.Subject.CommonName != "" || clientTLS.Certificates[0].Leaf.Subject.CommonName != "" {
		t.Fatal("identity certificates must not encode device IDs in certificate names")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := New("127.0.0.1:0", serverTLS)
	sessions, err := server.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer server.Close()
	client := New(server.Address, clientTLS)
	clientSession, err := client.Connect(ctx, serverIdentity.DeviceID)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer clientSession.Close()
	serverSession := <-sessions
	if serverSession == nil {
		t.Fatal("trusted client session was not accepted")
	}
	defer serverSession.Close()
	if clientSession.RemoteDeviceID() != serverIdentity.DeviceID || serverSession.RemoteDeviceID() != clientIdentity.DeviceID {
		t.Fatalf("remote IDs client=%s server=%s", clientSession.RemoteDeviceID(), serverSession.RemoteDeviceID())
	}

	serverStream, err := serverSession.AcceptStream(ctx)
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	clientStream, err := clientSession.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	writeErr := make(chan error, 1)
	go func() {
		_, err := serverStream.Write([]byte("pong"))
		writeErr <- err
	}()
	buffer := make([]byte, 4)
	if _, err := clientStream.Read(buffer); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := <-writeErr; err != nil || string(buffer) != "pong" {
		t.Fatalf("exchange buffer=%q err=%v", buffer, err)
	}
}

func TestMutualTLSRejectsUntrustedClientBeforeStream(t *testing.T) {
	tests := []struct {
		name       string
		serverPeer func(identity.DeviceIdentity) storage.Device
		want       error
	}{
		{
			name:       "unknown",
			serverPeer: func(identity.DeviceIdentity) storage.Device { return storage.Device{} },
			want:       pairing.ErrUnknownPeer,
		},
		{
			name: "revoked",
			serverPeer: func(peer identity.DeviceIdentity) storage.Device {
				device := trustedTransportDevice(peer)
				device.TrustState = storage.TrustRevoked
				return device
			},
			want: pairing.ErrRevokedPeer,
		},
		{
			name: "mismatched pinned key",
			serverPeer: func(peer identity.DeviceIdentity) storage.Device {
				device := trustedTransportDevice(peer)
				device.PublicKey = append([]byte(nil), transportIdentity(t, 49).PublicKey...)
				return device
			},
			want: pairing.ErrPeerIdentityMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Unix(10_000, 0).UTC()
			serverIdentity := transportIdentity(t, 43)
			clientIdentity := transportIdentity(t, 44)
			serverDevices := newTransportDeviceStore()
			if device := test.serverPeer(clientIdentity); device.ID != "" {
				serverDevices.devices[device.ID] = device
			}
			clientDevices := newTransportDeviceStore(trustedTransportDevice(serverIdentity))
			serverTLS := transportTLSConfig(t, serverIdentity, serverDevices, true, now)
			clientTLS := transportTLSConfig(t, clientIdentity, clientDevices, false, now)
			_, serverErr := handshakeSessions(t, serverTLS, clientTLS, serverIdentity.DeviceID)
			if !errors.Is(serverErr, test.want) {
				t.Fatalf("server handshake error = %v, want %v", serverErr, test.want)
			}
		})
	}
}

func TestMutualTLSRejectsExpiredPeerCertificate(t *testing.T) {
	now := time.Unix(20_000, 0).UTC()
	serverIdentity := transportIdentity(t, 45)
	clientIdentity := transportIdentity(t, 46)
	serverDevices := newTransportDeviceStore(trustedTransportDevice(clientIdentity))
	clientDevices := newTransportDeviceStore(trustedTransportDevice(serverIdentity))
	serverTLS := transportTLSConfig(t, serverIdentity, serverDevices, true, now)
	clientTLS, err := identityTLSConfigAt(
		IdentityTLSConfigOptions{
			Identity: clientIdentity, PeerVerifier: pairing.TrustedPeerVerifier{Devices: clientDevices}, Server: false,
		},
		now.Add(-2*time.Hour), now.Add(-time.Hour), func() time.Time { return now },
		bytes.NewReader(bytes.Repeat([]byte{47}, 64)),
	)
	if err != nil {
		t.Fatalf("expired identityTLSConfigAt: %v", err)
	}
	_, serverErr := handshakeSessions(t, serverTLS, clientTLS, serverIdentity.DeviceID)
	if !errors.Is(serverErr, ErrPeerCertificateExpired) {
		t.Fatalf("expired peer error = %v, want %v", serverErr, ErrPeerCertificateExpired)
	}
}

func TestMutualTLSRejectsAuthenticatedUnexpectedDevice(t *testing.T) {
	now := time.Unix(30_000, 0).UTC()
	serverIdentity := transportIdentity(t, 47)
	clientIdentity := transportIdentity(t, 48)
	serverDevices := newTransportDeviceStore(trustedTransportDevice(clientIdentity))
	clientDevices := newTransportDeviceStore(trustedTransportDevice(serverIdentity))
	serverTLS := transportTLSConfig(t, serverIdentity, serverDevices, true, now)
	clientTLS := transportTLSConfig(t, clientIdentity, clientDevices, false, now)
	clientErr, _ := handshakeSessions(t, serverTLS, clientTLS, transportIdentity(t, 50).DeviceID)
	if !errors.Is(clientErr, ErrUnexpectedPeer) {
		t.Fatalf("unexpected peer error = %v, want %v", clientErr, ErrUnexpectedPeer)
	}
}

func TestMutualTLSRejectsExcessivePeerCertificateLifetime(t *testing.T) {
	now := time.Unix(40_000, 0).UTC()
	serverIdentity := transportIdentity(t, 52)
	clientIdentity := transportIdentity(t, 53)
	serverDevices := newTransportDeviceStore(trustedTransportDevice(clientIdentity))
	clientDevices := newTransportDeviceStore(trustedTransportDevice(serverIdentity))
	serverTLS := transportTLSConfig(t, serverIdentity, serverDevices, true, now)
	clientTLS, err := identityTLSConfigAt(
		IdentityTLSConfigOptions{
			Identity: clientIdentity, PeerVerifier: pairing.TrustedPeerVerifier{Devices: clientDevices}, Server: false,
		},
		now.Add(-IdentityCertificateLifetime), now.Add(time.Hour), func() time.Time { return now },
		bytes.NewReader(bytes.Repeat([]byte{54}, 64)),
	)
	if err != nil {
		t.Fatalf("long-lived identityTLSConfigAt: %v", err)
	}
	_, serverErr := handshakeSessions(t, serverTLS, clientTLS, serverIdentity.DeviceID)
	if !errors.Is(serverErr, ErrPeerCertificateInvalid) {
		t.Fatalf("long-lived peer error = %v, want %v", serverErr, ErrPeerCertificateInvalid)
	}
}

func TestTransportConnectRequiresExpectedPairedDevice(t *testing.T) {
	transport := New("127.0.0.1:1", &tls.Config{MinVersion: tls.VersionTLS13})
	if _, err := transport.Connect(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "expected paired device") {
		t.Fatalf("Connect without expected peer error = %v", err)
	}
}

func handshakeSessions(t *testing.T, serverTLS, clientTLS *tls.Config, expectedServer core.DeviceID) (error, error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for handshake: %v", err)
	}
	defer listener.Close()
	deadline := time.Now().Add(5 * time.Second)
	serverResult := make(chan error, 1)
	go func() {
		serverConn, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer serverConn.Close()
		_ = serverConn.SetDeadline(deadline)
		_, err = newSession(tls.Server(serverConn, serverTLS), "").AcceptStream(context.Background())
		serverResult <- err
	}()
	clientConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial handshake: %v", err)
	}
	defer clientConn.Close()
	_ = clientConn.SetDeadline(deadline)
	_, clientErr := newSession(tls.Client(clientConn, clientTLS), expectedServer).OpenStream(context.Background())
	return clientErr, <-serverResult
}

func transportTLSConfig(t *testing.T, deviceIdentity identity.DeviceIdentity, devices storage.DeviceStore, server bool, now time.Time) *tls.Config {
	t.Helper()
	config, err := identityTLSConfigAt(
		IdentityTLSConfigOptions{
			Identity: deviceIdentity, PeerVerifier: pairing.TrustedPeerVerifier{Devices: devices}, Server: server,
		},
		now.Add(-time.Minute), now.Add(time.Hour), func() time.Time { return now },
		bytes.NewReader(bytes.Repeat([]byte{51}, 64)),
	)
	if err != nil {
		t.Fatalf("identityTLSConfigAt: %v", err)
	}
	return config
}

func transportIdentity(t *testing.T, value byte) identity.DeviceIdentity {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{value}, ed25519.SeedSize))
	deviceIdentity, err := identity.FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	return deviceIdentity
}

func trustedTransportDevice(deviceIdentity identity.DeviceIdentity) storage.Device {
	return storage.Device{
		ID: deviceIdentity.DeviceID, DisplayName: "Peer", PublicKey: append([]byte(nil), deviceIdentity.PublicKey...),
		Fingerprint: deviceIdentity.Fingerprint, TrustState: storage.TrustTrusted,
	}
}

type transportDeviceStore struct {
	devices map[core.DeviceID]storage.Device
}

func newTransportDeviceStore(devices ...storage.Device) *transportDeviceStore {
	store := &transportDeviceStore{devices: make(map[core.DeviceID]storage.Device)}
	for _, device := range devices {
		store.devices[device.ID] = device
	}
	return store
}

func (store *transportDeviceStore) TrustDevice(_ context.Context, device storage.Device) error {
	store.devices[device.ID] = device
	return nil
}

func (store *transportDeviceStore) RevokeDevice(_ context.Context, id core.DeviceID) error {
	device, ok := store.devices[id]
	if !ok {
		return storage.ErrNotFound
	}
	device.TrustState = storage.TrustRevoked
	store.devices[id] = device
	return nil
}

func (store *transportDeviceStore) GetDevice(_ context.Context, id core.DeviceID) (storage.Device, error) {
	device, ok := store.devices[id]
	if !ok {
		return storage.Device{}, storage.ErrNotFound
	}
	return device, nil
}
