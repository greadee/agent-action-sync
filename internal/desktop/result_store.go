package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"syncgate/internal/integrationgate"
	"syncgate/internal/resultintake"
)

const MaxLocalResultContentBytes = 256 << 20

type LocalResultStore struct{ Root string }

func (store LocalResultStore) PutEnvelope(ctx context.Context, raw []byte) (resultintake.Envelope, error) {
	if ctx == nil || ctx.Err() != nil {
		return resultintake.Envelope{}, errors.New("result import context is unavailable")
	}
	envelope, canonical, err := resultintake.DecodeEnvelope(raw)
	if err != nil {
		return resultintake.Envelope{}, err
	}
	path, err := store.envelopePath(envelope.ResultID)
	if err != nil {
		return resultintake.Envelope{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return resultintake.Envelope{}, err
	}
	if existing, readErr := readBoundedLocalFile(ctx, path, resultintake.MaxEnvelopeBytes); readErr == nil {
		if string(existing) != string(canonical) {
			return resultintake.Envelope{}, errors.New("result envelope ID already has different content")
		}
		return envelope, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return resultintake.Envelope{}, readErr
	}
	if err := writeBytesAtomic(path, canonical, 0o600); err != nil {
		return resultintake.Envelope{}, err
	}
	return envelope, nil
}

func (store LocalResultStore) PutContent(ctx context.Context, reference resultintake.ContentReference, raw []byte) error {
	if ctx == nil || ctx.Err() != nil || reference.Size != int64(len(raw)) || reference.Size < 0 || reference.Size > MaxLocalResultContentBytes || !lowerHexDigest(reference.Digest, 64) {
		return errors.New("result content import is invalid")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != reference.Digest {
		return errors.New("result content digest mismatch")
	}
	path, err := store.contentPath(reference.Digest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeBytesAtomic(path, raw, 0o600)
}

func (store LocalResultStore) GetEnvelope(ctx context.Context, resultID string) ([]byte, error) {
	path, err := store.envelopePath(resultID)
	if err != nil {
		return nil, err
	}
	return readBoundedLocalFile(ctx, path, resultintake.MaxEnvelopeBytes)
}

func (store LocalResultStore) GetContent(ctx context.Context, reference resultintake.ContentReference) (integrationgate.Content, error) {
	path, err := store.contentPath(reference.Digest)
	if err != nil {
		return integrationgate.Content{}, err
	}
	raw, err := readBoundedLocalFile(ctx, path, MaxLocalResultContentBytes)
	if err != nil {
		return integrationgate.Content{}, err
	}
	if int64(len(raw)) != reference.Size {
		return integrationgate.Content{}, errors.New("stored result content size mismatch")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != reference.Digest {
		return integrationgate.Content{}, errors.New("stored result content digest mismatch")
	}
	return integrationgate.Content{Name: reference.ID, MediaType: "application/octet-stream", Bytes: raw}, nil
}

func (store LocalResultStore) envelopePath(resultID string) (string, error) {
	if !strings.HasPrefix(resultID, "result:") || len(resultID) > 128 {
		return "", errors.New("result ID is invalid")
	}
	return store.safePath("envelopes", localHash(resultID)+".json")
}

func (store LocalResultStore) contentPath(digest string) (string, error) {
	if !lowerHexDigest(digest, 64) {
		return "", errors.New("content digest is invalid")
	}
	return store.safePath("content", digest+".bin")
}

func (store LocalResultStore) safePath(directory, name string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(store.Root))
	if !filepath.IsAbs(root) {
		return "", errors.New("local result root must be absolute")
	}
	return filepath.Join(root, directory, name), nil
}

func readBoundedLocalFile(ctx context.Context, path string, limit int) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("local result context is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("local result file exceeds %d-byte limit", limit)
	}
	return raw, ctx.Err()
}

var _ integrationgate.ResultSource = LocalResultStore{}
var _ integrationgate.ContentSource = LocalResultStore{}
