package resultintake

import (
	"context"
	"sync"
)

type UploadReceipt struct {
	ResultID           string
	EnvelopeDigest     string
	AlreadyPresent     bool
	CanonicalAuthority bool
}

type UploadOnlyReceiver interface {
	ReceiveCandidate(context.Context, []byte) (UploadReceipt, error)
}

// DeterministicUploadFake models a future upload-only return path. Receipt of
// bytes never invokes intake and never grants canonical publication authority.
type DeterministicUploadFake struct {
	mu      sync.Mutex
	digests map[string]string
}

func NewDeterministicUploadFake() *DeterministicUploadFake {
	return &DeterministicUploadFake{digests: map[string]string{}}
}

func (fake *DeterministicUploadFake) ReceiveCandidate(ctx context.Context, raw []byte) (UploadReceipt, error) {
	if ctx == nil {
		return UploadReceipt{}, ErrInvalidEnvelope
	}
	if err := ctx.Err(); err != nil {
		return UploadReceipt{}, err
	}
	envelope, _, err := DecodeEnvelope(raw)
	if err != nil {
		return UploadReceipt{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if digest, ok := fake.digests[envelope.ResultID]; ok {
		if digest != envelope.Digest {
			return UploadReceipt{}, ErrResultConflict
		}
		return UploadReceipt{ResultID: envelope.ResultID, EnvelopeDigest: envelope.Digest, AlreadyPresent: true, CanonicalAuthority: false}, nil
	}
	fake.digests[envelope.ResultID] = envelope.Digest
	return UploadReceipt{ResultID: envelope.ResultID, EnvelopeDigest: envelope.Digest, CanonicalAuthority: false}, nil
}
