package tcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"syncgate/internal/core"
	"syncgate/internal/transport"
)

type Transport struct {
	Address   string
	TLSConfig *tls.Config
	listener  net.Listener
}

func New(address string, tlsConfig *tls.Config) *Transport {
	return &Transport{Address: address, TLSConfig: tlsConfig}
}

func (tcp *Transport) Connect(ctx context.Context, deviceID core.DeviceID) (transport.Session, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", tcp.Address)
	if err != nil {
		return nil, fmt.Errorf("connect tcp tls session: %w", err)
	}
	return newSession(tls.Client(conn, tcp.TLSConfig), deviceID), nil
}

func (tcp *Transport) Listen(ctx context.Context) (<-chan transport.Session, error) {
	listener, err := tls.Listen("tcp", tcp.Address, tcp.TLSConfig)
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
			select {
			case sessions <- newSession(conn.(*tls.Conn), ""):
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
}

func newSession(conn *tls.Conn, expectedDevice core.DeviceID) *session {
	return &session{conn: conn, expectedDevice: expectedDevice}
}

func (session *session) OpenStream(context.Context) (transport.Stream, error) {
	if err := session.conn.Handshake(); err != nil {
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	if session.expectedDevice != "" && session.RemoteDeviceID() != session.expectedDevice {
		return nil, fmt.Errorf("connected device %s, expected %s", session.RemoteDeviceID(), session.expectedDevice)
	}
	return session.conn, nil
}

func (session *session) AcceptStream(ctx context.Context) (transport.Stream, error) {
	return session.OpenStream(ctx)
}

func (session *session) RemoteDeviceID() core.DeviceID {
	state := session.conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return ""
	}
	return core.DeviceID(state.PeerCertificates[0].Subject.CommonName)
}

func (session *session) TransportType() string {
	return "tcp_tls"
}

func (session *session) Close() error {
	return session.conn.Close()
}
