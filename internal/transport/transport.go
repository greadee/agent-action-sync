package transport

import (
	"context"
	"io"

	"syncgate/internal/core"
)

type PeerTransport interface {
	Connect(ctx context.Context, deviceID core.DeviceID) (Session, error)
	Listen(ctx context.Context) (<-chan Session, error)
	Type() string
	Close() error
}

type Session interface {
	OpenStream(ctx context.Context) (Stream, error)
	AcceptStream(ctx context.Context) (Stream, error)
	RemoteDeviceID() core.DeviceID
	TransportType() string
	Close() error
}

type Stream interface {
	io.Reader
	io.Writer
	Close() error
}
