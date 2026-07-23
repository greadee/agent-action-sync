package sync

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"syncgate/internal/core"
)

type RevisionBuildOptions struct {
	ShareID        core.ShareID
	OriginDeviceID core.DeviceID
	SequenceStart  int64
	Now            func() time.Time
	NewID          func() (core.RevisionID, error)
}

func BuildRevisions(reconciliation ReconcileResult, options RevisionBuildOptions) ([]core.Revision, error) {
	if options.ShareID == "" {
		return nil, fmt.Errorf("share ID is required")
	}
	if options.OriginDeviceID == "" {
		return nil, fmt.Errorf("origin device ID is required")
	}
	if options.SequenceStart < 0 {
		return nil, fmt.Errorf("sequence start cannot be negative")
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newID := options.NewID
	if newID == nil {
		newID = newRevisionID
	}

	changes := append([]Change(nil), reconciliation.Changes...)
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].RelativePath < changes[j].RelativePath
	})

	createdAt := now().UTC()
	revisions := make([]core.Revision, 0, len(changes))
	for _, change := range changes {
		if change.Kind == ChangeUnchanged {
			continue
		}
		if change.RelativePath == "" {
			return nil, fmt.Errorf("change has empty relative path")
		}
		id, err := newID()
		if err != nil {
			return nil, fmt.Errorf("create revision ID: %w", err)
		}
		if id == "" {
			return nil, fmt.Errorf("create revision ID: empty ID")
		}

		revision := core.Revision{
			ID:               id,
			ShareID:          options.ShareID,
			RelativePath:     change.RelativePath,
			OriginDeviceID:   options.OriginDeviceID,
			Sequence:         options.SequenceStart + int64(len(revisions)) + 1,
			ParentRevisionID: change.Previous.CurrentRevisionID,
			CreatedAt:        createdAt,
		}
		if change.Kind == ChangeDeleted {
			revision.EntryType = core.EntryDeleted
			revision.IsDeleted = true
		} else {
			revision.EntryType = change.Current.EntryType
			revision.Size = change.Current.Size
			revision.ContentHash = change.Current.ContentHash
			revision.HashAlgorithm = change.Current.HashAlgorithm
		}
		revisions = append(revisions, revision)
	}
	return revisions, nil
}

func newRevisionID() (core.RevisionID, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return core.RevisionID(hex.EncodeToString(bytes)), nil
}
