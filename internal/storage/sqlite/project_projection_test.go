package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestProjectRegistrationIsIdempotentAndIdentityBound(t *testing.T) {
	store := newTestStore(t)
	registration := saveProjectRegistration(t, store, "project-1")

	duplicate, err := store.ProjectRegistrations().RegisterProject(context.Background(), registration)
	if err != nil || !duplicate.AlreadyPresent {
		t.Fatalf("duplicate registration = %#v, err=%v", duplicate, err)
	}
	conflict := registration
	conflict.ManifestRecordHash = projectionHash("b")
	if _, err := store.ProjectRegistrations().RegisterProject(context.Background(), conflict); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("hash conflict error = %v", err)
	}
	conflict = registration
	conflict.RootPath = t.TempDir()
	if _, err := store.ProjectRegistrations().RegisterProject(context.Background(), conflict); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("root identity conflict error = %v", err)
	}
	got, err := store.ProjectRegistrations().GetProject(context.Background(), registration.ProjectID)
	if err != nil || got.ManifestRecordHash != registration.ManifestRecordHash || got.ShareID != registration.ShareID {
		t.Fatalf("registration = %#v, err=%v", got, err)
	}
}

func TestProjectEventsAreIdempotentAndPaginateTiedTimestamps(t *testing.T) {
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-events")
	ctx := context.Background()
	occurredAt := time.Date(2026, time.August, 14, 8, 0, 0, 0, time.UTC)
	for _, id := range []string{"event-c", "event-a", "event-b"} {
		event := testProjectEvent("project-events", id, occurredAt)
		if _, err := store.ProjectEvents().SaveProjectEvent(ctx, event); err != nil {
			t.Fatalf("SaveProjectEvent(%s): %v", id, err)
		}
	}
	event := testProjectEvent("project-events", "event-a", occurredAt)
	duplicate, err := store.ProjectEvents().SaveProjectEvent(ctx, event)
	if err != nil || !duplicate.AlreadyPresent {
		t.Fatalf("duplicate event = %#v, err=%v", duplicate, err)
	}
	event.RecordHash = projectionHash("d")
	if _, err := store.ProjectEvents().SaveProjectEvent(ctx, event); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("event hash conflict error = %v", err)
	}

	first, err := store.ProjectEvents().ListProjectEvents(ctx, storage.ProjectEventQuery{
		ProjectID: "project-events", Page: storage.PageRequest{Limit: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].EventID != "event-a" || first.Items[1].EventID != "event-b" || first.NextCursor == nil {
		t.Fatalf("first page = %#v", first)
	}
	second, err := store.ProjectEvents().ListProjectEvents(ctx, storage.ProjectEventQuery{
		ProjectID: "project-events", Page: storage.PageRequest{Limit: 2, Cursor: *first.NextCursor},
	})
	if err != nil || len(second.Items) != 1 || second.Items[0].EventID != "event-c" || second.NextCursor != nil {
		t.Fatalf("second page = %#v, err=%v", second, err)
	}
	got, err := store.ProjectEvents().GetProjectEvent(ctx, "project-events", "event-b")
	if err != nil || string(got.PayloadJSON) != `{"sequence":"event-b"}` {
		t.Fatalf("event = %#v, err=%v", got, err)
	}
}

func TestProjectArtifactsAndCheckpointsAreBoundedAndQueryable(t *testing.T) {
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-artifacts")
	ctx := context.Background()
	createdAt := time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"artifact-b", "artifact-a"} {
		artifact := testProjectArtifact("project-artifacts", id, createdAt)
		if _, err := store.ProjectArtifacts().SaveProjectArtifact(ctx, artifact); err != nil {
			t.Fatalf("SaveProjectArtifact(%s): %v", id, err)
		}
	}
	artifact := testProjectArtifact("project-artifacts", "artifact-a", createdAt)
	duplicate, err := store.ProjectArtifacts().SaveProjectArtifact(ctx, artifact)
	if err != nil || !duplicate.AlreadyPresent {
		t.Fatalf("duplicate artifact = %#v, err=%v", duplicate, err)
	}
	artifact.RecordHash = projectionHash("e")
	if _, err := store.ProjectArtifacts().SaveProjectArtifact(ctx, artifact); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("artifact hash conflict error = %v", err)
	}
	page, err := store.ProjectArtifacts().ListProjectArtifacts(ctx, storage.ProjectArtifactQuery{
		ProjectID: "project-artifacts", MediaType: "text/plain", Page: storage.PageRequest{Limit: 10},
	})
	if err != nil || len(page.Items) != 2 || page.Items[0].ArtifactID != "artifact-a" {
		t.Fatalf("artifact page = %#v, err=%v", page, err)
	}
	checkpoint := storage.ProjectProjectionCheckpoint{
		ProjectID: "project-artifacts", Stream: "portable-records",
		LastRecordPath: artifact.RecordPath, LastRecordHash: projectionHash("f"), UpdatedAt: createdAt,
	}
	if err := store.ProjectCheckpoints().SetProjectCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.LastRecordPath = ".agent-project/artifacts/manifests/artifact-b.json"
	checkpoint.UpdatedAt = createdAt.Add(time.Minute)
	if err := store.ProjectCheckpoints().SetProjectCheckpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	got, err := store.ProjectCheckpoints().GetProjectCheckpoint(ctx, checkpoint.ProjectID, checkpoint.Stream)
	if err != nil || got.LastRecordPath != checkpoint.LastRecordPath || !got.UpdatedAt.Equal(checkpoint.UpdatedAt) {
		t.Fatalf("checkpoint = %#v, err=%v", got, err)
	}
}

func TestProjectProjectionRebuildIsAtomicAndLeavesCanonicalFilesAlone(t *testing.T) {
	store := newTestStore(t)
	registration := saveProjectRegistration(t, store, "project-rebuild")
	ctx := context.Background()
	canonicalPath := filepath.Join(registration.RootPath, ".agent-project", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonicalPath, []byte("canonical-authority"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := testProjectEvent(registration.ProjectID, "event-old", time.Unix(100, 0).UTC())
	if _, err := store.ProjectEvents().SaveProjectEvent(ctx, old); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("stop rebuild")
	err := store.ProjectProjections().RebuildProjectProjection(ctx, registration.ProjectID, func(writer storage.ProjectProjectionWriter) error {
		if _, err := writer.SaveProjectEvent(ctx, testProjectEvent(registration.ProjectID, "event-rolled-back", time.Unix(101, 0).UTC())); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rebuild rollback error = %v", err)
	}
	if _, err := store.ProjectEvents().GetProjectEvent(ctx, registration.ProjectID, old.EventID); err != nil {
		t.Fatalf("old projection did not survive rollback: %v", err)
	}
	if _, err := store.ProjectEvents().GetProjectEvent(ctx, registration.ProjectID, "event-rolled-back"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rolled-back event error = %v", err)
	}

	newEvent := testProjectEvent(registration.ProjectID, "event-new", time.Unix(102, 0).UTC())
	newArtifact := testProjectArtifact(registration.ProjectID, "artifact-new", time.Unix(102, 0).UTC())
	err = store.ProjectProjections().RebuildProjectProjection(ctx, registration.ProjectID, func(writer storage.ProjectProjectionWriter) error {
		if _, err := writer.SaveProjectEvent(ctx, newEvent); err != nil {
			return err
		}
		if _, err := writer.SaveProjectArtifact(ctx, newArtifact); err != nil {
			return err
		}
		return writer.SetProjectCheckpoint(ctx, storage.ProjectProjectionCheckpoint{
			ProjectID: registration.ProjectID, Stream: "records", LastRecordPath: newEvent.RecordPath,
			LastRecordHash: newEvent.RecordHash, UpdatedAt: newEvent.OccurredAt,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectEvents().GetProjectEvent(ctx, registration.ProjectID, old.EventID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old event after rebuild error = %v", err)
	}
	if _, err := store.ProjectEvents().GetProjectEvent(ctx, registration.ProjectID, newEvent.EventID); err != nil {
		t.Fatalf("new event after rebuild: %v", err)
	}
	if err := store.ProjectProjections().ClearProjectProjection(ctx, registration.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectRegistrations().GetProject(ctx, registration.ProjectID); err != nil {
		t.Fatalf("clear removed project registration: %v", err)
	}
	if got, err := os.ReadFile(canonicalPath); err != nil || string(got) != "canonical-authority" {
		t.Fatalf("canonical file changed: %q, %v", got, err)
	}
}

func TestProjectProjectionApplyIsAtomic(t *testing.T) {
	store := newTestStore(t)
	registration := saveProjectRegistration(t, store, "project-apply")
	ctx := context.Background()
	event := testProjectEvent(registration.ProjectID, "event-apply", time.Unix(103, 0).UTC())
	sentinel := errors.New("rollback apply")
	err := store.ProjectProjections().ApplyProjectProjection(ctx, registration.ProjectID, func(writer storage.ProjectProjectionWriter) error {
		if _, err := writer.SaveProjectEvent(ctx, event); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("apply error = %v", err)
	}
	if _, err := store.ProjectEvents().GetProjectEvent(ctx, registration.ProjectID, event.EventID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rolled-back event error = %v", err)
	}
	if err := store.ProjectProjections().ApplyProjectProjection(ctx, registration.ProjectID, func(writer storage.ProjectProjectionWriter) error {
		_, err := writer.SaveProjectEvent(ctx, event)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectEvents().GetProjectEvent(ctx, registration.ProjectID, event.EventID); err != nil {
		t.Fatal(err)
	}
}

func TestProjectProjectionRejectsCanceledAndUnboundedRequests(t *testing.T) {
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-bounds")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ProjectEvents().ListProjectEvents(canceled, storage.ProjectEventQuery{ProjectID: "project-bounds"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query error = %v", err)
	}
	if _, err := store.ProjectEvents().ListProjectEvents(context.Background(), storage.ProjectEventQuery{
		ProjectID: "project-bounds", Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit + 1},
	}); err == nil {
		t.Fatal("expected oversized page to be rejected")
	}
	event := testProjectEvent("project-bounds", "event-large", time.Now().UTC())
	event.PayloadJSON = append([]byte{'"'}, append(make([]byte, storage.MaxProjectProjectionPayloadBytes), '"')...)
	if _, err := store.ProjectEvents().SaveProjectEvent(context.Background(), event); err == nil {
		t.Fatal("expected oversized payload to be rejected")
	}
}

func TestProjectMigrationUpgradesExistingDatabaseWithoutChangingRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, migration := range storage.Migrations[:6] {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`, migration.Version, migration.Name, time.Unix(1, 0).UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO shares(share_id, name, root_path, mode, case_policy, version_policy, created_at, updated_at)
VALUES ('legacy-share', 'Legacy', 'C:/legacy', 'read_only', '', '', ?, ?)`,
		time.Unix(2, 0).UTC().Format(time.RFC3339Nano), time.Unix(2, 0).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := store.db.QueryRowContext(ctx, `SELECT name FROM shares WHERE share_id = 'legacy-share'`).Scan(&name); err != nil || name != "Legacy" {
		t.Fatalf("legacy row changed or lost: name=%q err=%v", name, err)
	}
	var migrations int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil || migrations != len(storage.Migrations) {
		t.Fatalf("migration count=%d err=%v", migrations, err)
	}
}

func saveProjectRegistration(t *testing.T, store *Store, projectID string) storage.ProjectRegistration {
	t.Helper()
	shareID := core.ShareID("share-" + projectID)
	root := t.TempDir()
	if err := store.Shares().SaveShare(context.Background(), storage.Share{ID: shareID, Name: "Project", RootPath: root, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	registration := storage.ProjectRegistration{
		ProjectID: projectID, ShareID: shareID, RootPath: root, Name: "Project",
		AuthorityDeviceID: "device-1", ManifestRecordID: "manifest-1", ManifestRecordHash: projectionHash("a"),
		ManifestPath: ".agent-project/manifest.json", RegisteredAt: time.Date(2026, time.August, 14, 7, 0, 0, 0, time.UTC),
	}
	result, err := store.ProjectRegistrations().RegisterProject(context.Background(), registration)
	if err != nil || result.AlreadyPresent {
		t.Fatalf("RegisterProject = %#v, err=%v", result, err)
	}
	return registration
}

func testProjectEvent(projectID, eventID string, occurredAt time.Time) storage.ProjectEventProjection {
	return storage.ProjectEventProjection{
		ProjectID: projectID, EventID: eventID, RecordHash: projectionHash("c"), EventType: "TEST_RECORDED",
		OccurredAt: occurredAt, WorkPackageID: "work-1", ExecutionID: "execution-1",
		ProducerDeviceID: "device-1", ProducerWorkerID: "worker-1", Status: "accepted",
		RecordPath:  ".agent-project/history/events/2026/08/14/" + eventID + ".json",
		PayloadJSON: []byte(`{"sequence":"` + eventID + `"}`),
	}
}

func testProjectArtifact(projectID, artifactID string, createdAt time.Time) storage.ProjectArtifactProjection {
	return storage.ProjectArtifactProjection{
		ProjectID: projectID, ArtifactID: artifactID, RecordID: "record-" + artifactID,
		RecordHash: projectionHash("1"), Name: artifactID + ".txt", MediaType: "text/plain", Size: 12,
		ContentHash: projectionHash("2"), WorkPackageID: "work-1", ExecutionID: "execution-1",
		ProducerDeviceID: "device-1", CreatedAt: createdAt,
		RecordPath: ".agent-project/artifacts/manifests/" + artifactID + ".json",
	}
}

func projectionHash(character string) string {
	return strings.Repeat(character, 64)
}
