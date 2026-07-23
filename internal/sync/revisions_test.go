package sync

import (
	"errors"
	"testing"
	"time"

	"syncgate/internal/core"
)

func TestBuildRevisionsBuildsOrderedRevisionHistory(t *testing.T) {
	createdAt := time.Unix(789, 0).UTC()
	ids := []core.RevisionID{"revision-1", "revision-2", "revision-3"}
	idIndex := 0
	revisions, err := BuildRevisions(ReconcileResult{Changes: []Change{
		{Kind: ChangeModified, RelativePath: "z.txt", Previous: core.FileIndexEntry{CurrentRevisionID: "parent-z"}, Current: core.FileIndexEntry{EntryType: core.EntryFile, Size: 2, ContentHash: "z-hash", HashAlgorithm: "sha256"}},
		{Kind: ChangeUnchanged, RelativePath: "ignored.txt"},
		{Kind: ChangeDeleted, RelativePath: "a.txt", Previous: core.FileIndexEntry{CurrentRevisionID: "parent-a"}},
		{Kind: ChangeAdded, RelativePath: "b", Current: core.FileIndexEntry{EntryType: core.EntryDirectory}},
	}}, RevisionBuildOptions{
		ShareID:        "share-1",
		OriginDeviceID: "DEVICE-1",
		SequenceStart:  10,
		Now:            func() time.Time { return createdAt },
		NewID: func() (core.RevisionID, error) {
			id := ids[idIndex]
			idIndex++
			return id, nil
		},
	})
	if err != nil {
		t.Fatalf("BuildRevisions: %v", err)
	}
	if len(revisions) != 3 {
		t.Fatalf("revision count = %d", len(revisions))
	}
	if revisions[0].RelativePath != "a.txt" || revisions[0].EntryType != core.EntryDeleted || !revisions[0].IsDeleted {
		t.Fatalf("deletion revision = %+v", revisions[0])
	}
	if revisions[0].ParentRevisionID != "parent-a" || revisions[0].Sequence != 11 {
		t.Fatalf("deletion ancestry = %+v", revisions[0])
	}
	if revisions[1].RelativePath != "b" || revisions[1].EntryType != core.EntryDirectory || revisions[1].Sequence != 12 {
		t.Fatalf("directory revision = %+v", revisions[1])
	}
	if revisions[2].RelativePath != "z.txt" || revisions[2].ContentHash != "z-hash" || revisions[2].ParentRevisionID != "parent-z" || revisions[2].Sequence != 13 {
		t.Fatalf("file revision = %+v", revisions[2])
	}
	for _, revision := range revisions {
		if revision.ShareID != "share-1" || revision.OriginDeviceID != "DEVICE-1" || !revision.CreatedAt.Equal(createdAt) {
			t.Fatalf("revision metadata = %+v", revision)
		}
	}
}

func TestBuildRevisionsRejectsInvalidInput(t *testing.T) {
	_, err := BuildRevisions(ReconcileResult{}, RevisionBuildOptions{OriginDeviceID: "DEVICE-1"})
	if err == nil {
		t.Fatal("expected share ID error")
	}
	_, err = BuildRevisions(ReconcileResult{}, RevisionBuildOptions{ShareID: "share-1"})
	if err == nil {
		t.Fatal("expected origin device error")
	}
	_, err = BuildRevisions(ReconcileResult{}, RevisionBuildOptions{ShareID: "share-1", OriginDeviceID: "DEVICE-1", SequenceStart: -1})
	if err == nil {
		t.Fatal("expected sequence error")
	}
	_, err = BuildRevisions(ReconcileResult{Changes: []Change{{Kind: ChangeAdded, RelativePath: "a.txt"}}}, RevisionBuildOptions{
		ShareID: "share-1", OriginDeviceID: "DEVICE-1", NewID: func() (core.RevisionID, error) { return "", errors.New("entropy unavailable") },
	})
	if err == nil {
		t.Fatal("expected ID generator error")
	}
}
