package tcp

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/transport"
)

var ErrUnexpectedPeer = errors.New("authenticated peer does not match expected device")

type Transport struct {
	Address   string
	TLSConfig *tls.Config
	listener  net.Listener
}

func New(address string, tlsConfig *tls.Config) *Transport {
	return &Transport{Address: address, TLSConfig: tlsConfig}
}

func (tcp *Transport) Connect(ctx context.Context, deviceID core.DeviceID) (transport.Session, error) {
	if ctx == nil {
		return nil, errors.New("connect context is required")
	}
	if tcp.TLSConfig == nil {
		return nil, errors.New("TLS config is required")
	}
	if deviceID == "" {
		return nil, errors.New("expected paired device ID is required")
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", tcp.Address)
	if err != nil {
		return nil, fmt.Errorf("connect tcp tls session: %w", err)
	}
	tlsConn := tls.Client(conn, tcp.TLSConfig.Clone())
	session := newSession(tlsConn, deviceID)
	if err := session.authenticate(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return session, nil
}

func (tcp *Transport) Listen(ctx context.Context) (<-chan transport.Session, error) {
	if ctx == nil {
		return nil, errors.New("listen context is required")
	}
	if tcp.TLSConfig == nil {
		return nil, errors.New("TLS config is required")
	}
	listener, err := tls.Listen("tcp", tcp.Address, tcp.TLSConfig.Clone())
	if err != nil {
		return nil, fmt.Errorf("listen tcp tls sessions: %w", err)
	}
	tcp.listener = listener
	tcp.Address = listener.Addr().String()
	sessions := make(chan transport.Session)
	go func() {
		defer close(sessions)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			tlsConn, ok := conn.(*tls.Conn)
			if !ok {
				_ = conn.Close()
				continue
			}
			session := newSession(tlsConn, "")
			if err := session.authenticate(ctx); err != nil {
				_ = conn.Close()
				continue
			}
			select {
			case sessions <- session:
			case <-ctx.Done():
				_ = conn.Close()
				return
			}
		}
	}()
	return sessions, nil
}

func (tcp *Transport) Type() string {
	return "tcp_tls"
}

func (tcp *Transport) Close() error {
	if tcp.listener == nil {
		return nil
	}
	return tcp.listener.Close()
}

type session struct {
	conn           *tls.Conn
	expectedDevice core.DeviceID
	authMu         sync.Mutex
	authenticated  bool
	authErr        error
	remoteDeviceID core.DeviceID
}

func newSession(conn *tls.Conn, expectedDevice core.DeviceID) *session {
	return &session{conn: conn, expectedDevice: expectedDevice}
}

func (session *session) OpenStream(ctx context.Context) (transport.Stream, error) {
	if err := session.authenticate(ctx); err != nil {
		return nil, err
	}
	return session.conn, nil
}

func (session *session) AcceptStream(ctx context.Context) (transport.Stream, error) {
	return session.OpenStream(ctx)
}

func (session *session) RemoteDeviceID() core.DeviceID {
	session.authMu.Lock()
	defer session.authMu.Unlock()
	return session.remoteDeviceID
}

func (session *session) TransportType() string {
	return "tcp_tls"
}

func (session *session) Close() error {
	return session.conn.Close()
}

func (session *session) authenticate(ctx context.Context) error {
	if ctx == nil {
		return errors.New("TLS handshake context is required")
	}
	session.authMu.Lock()
	defer session.authMu.Unlock()
	if session.authenticated || session.authErr != nil {
		return session.authErr
	}
	if err := session.conn.HandshakeContext(ctx); err != nil {
		session.authErr = fmt.Errorf("TLS handshake: %w", err)
		return session.authErr
	}
	state := session.conn.ConnectionState()
	if len(state.PeerCertificates) != 1 {
		session.authErr = fmt.Errorf("%w: verified handshake has %d peer certificates", ErrPeerCertificateInvalid, len(state.PeerCertificates))
		return session.authErr
	}
	publicKey, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		session.authErr = fmt.Errorf("%w: verified peer key is not Ed25519", ErrPeerCertificateInvalid)
		return session.authErr
	}
	fingerprint := identity.Fingerprint(publicKey)
	remoteDeviceID := identity.DeviceIDFromFingerprint(fingerprint)
	if session.expectedDevice != "" && remoteDeviceID != session.expectedDevice {
		session.authErr = fmt.Errorf("%w: authenticated %s, expected %s", ErrUnexpectedPeer, remoteDeviceID, session.expectedDevice)
		return session.authErr
	}
	session.remoteDeviceID = remoteDeviceID
	session.authenticated = true
	return nil
}
