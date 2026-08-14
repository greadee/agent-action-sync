package projector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/project"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestIncrementalIngestionAndCleanRebuildAreEquivalent(t *testing.T) {
	root, store := setupProjectorTest(t, "project-equivalent")
	publishTestArtifact(t, root, "artifact-1")
	service := &Projector{Store: store, Now: fixedProjectorTime}

	first, err := service.Ingest(context.Background(), root, TriggerShareScan)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectedEvents != 1 || first.ProjectedArtifacts != 1 || first.RejectedRecords != 0 {
		t.Fatalf("incremental report = %#v", first)
	}
	before := listProjectedEventIdentity(t, store, "project-equivalent")
	artifactBefore, err := store.ProjectArtifacts().GetProjectArtifact(context.Background(), "project-equivalent", "artifact-1")
	if err != nil {
		t.Fatal(err)
	}

	rebuilt, err := service.Rebuild(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	after := listProjectedEventIdentity(t, store, "project-equivalent")
	artifactAfter, err := store.ProjectArtifacts().GetProjectArtifact(context.Background(), "project-equivalent", "artifact-1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || artifactBefore.RecordHash != artifactAfter.RecordHash || rebuilt.CheckpointHash != first.CheckpointHash {
		t.Fatalf("projection mismatch: before=%#v after=%#v", before, after)
	}
}

func TestInterruptedIngestionWindowsReplaySafely(t *testing.T) {
	t.Run("after discovery before commit", func(t *testing.T) {
		root, store := setupProjectorTest(t, "project-discovery-restart")
		sentinel := errors.New("interrupted after discovery")
		service := &Projector{Store: store, Hooks: Hooks{AfterDiscovery: func() error { return sentinel }}}
		if _, err := service.Ingest(context.Background(), root, TriggerShareScan); !errors.Is(err, sentinel) {
			t.Fatalf("interruption error = %v", err)
		}
		if _, err := store.ProjectRegistrations().GetProject(context.Background(), "project-discovery-restart"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("registration before commit error = %v", err)
		}
		if _, err := (&Projector{Store: store}).Ingest(context.Background(), root, TriggerShareScan); err != nil {
			t.Fatalf("restart ingestion: %v", err)
		}
	})

	t.Run("after projection commit before checkpoint", func(t *testing.T) {
		root, store := setupProjectorTest(t, "project-checkpoint-restart")
		sentinel := errors.New("interrupted before checkpoint")
		service := &Projector{Store: store, Hooks: Hooks{AfterProjectionCommit: func() error { return sentinel }}}
		if _, err := service.Ingest(context.Background(), root, TriggerShareScan); !errors.Is(err, sentinel) {
			t.Fatalf("interruption error = %v", err)
		}
		if events := listProjectedEventIdentity(t, store, "project-checkpoint-restart"); len(events) != 1 {
			t.Fatalf("committed events = %#v", events)
		}
		if _, err := store.ProjectCheckpoints().GetProjectCheckpoint(context.Background(), "project-checkpoint-restart", CheckpointStream); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("checkpoint before restart error = %v", err)
		}
		report, err := (&Projector{Store: store}).Ingest(context.Background(), root, TriggerShareScan)
		if err != nil || report.ProjectedEvents != 1 {
			t.Fatalf("restart report=%#v err=%v", report, err)
		}
		if _, err := store.ProjectCheckpoints().GetProjectCheckpoint(context.Background(), "project-checkpoint-restart", CheckpointStream); err != nil {
			t.Fatalf("checkpoint after restart: %v", err)
		}
	})
}

func TestIncrementalIngestionRemovesProjectionForRecordThatBecomesInvalid(t *testing.T) {
	root, store := setupProjectorTest(t, "project-stale")
	publishWorkPackageCreatedEvent(t, root, "project-stale", "work-1", "definition-record-1")
	service := &Projector{Store: store, Now: fixedProjectorTime}
	if _, err := service.Ingest(context.Background(), root, TriggerShareScan); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectEvents().GetProjectEvent(context.Background(), "project-stale", "event-work-created"); err != nil {
		t.Fatal(err)
	}
	layout, err := project.NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	eventPath, err := project.WorkEventRelativePath("event-work-created", time.Date(2026, time.August, 14, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := layout.ResolveManaged(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolved, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := service.Ingest(context.Background(), root, TriggerShareScan)
	if err != nil || report.RejectedRecords != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, err := store.ProjectEvents().GetProjectEvent(context.Background(), "project-stale", "event-work-created"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("stale projected event error = %v", err)
	}
}

func TestOutOfOrderEventBecomesAcceptedWhenDependencyArrives(t *testing.T) {
	root, store := setupProjectorTest(t, "project-order")
	publishWorkPackageCreatedEvent(t, root, "project-order", "work-1", "definition-record-1")
	service := &Projector{Store: store, Now: fixedProjectorTime}
	first, err := service.Ingest(context.Background(), root, TriggerShareScan)
	if err != nil || first.PendingEvents != 1 {
		t.Fatalf("first report=%#v err=%v", first, err)
	}
	event, err := store.ProjectEvents().GetProjectEvent(context.Background(), "project-order", "event-work-created")
	if err != nil || event.Status != "pending" {
		t.Fatalf("pending event=%#v err=%v", event, err)
	}

	publishWorkPackageDefinition(t, root, "project-order", "work-1", "definition-record-1")
	second, err := service.Ingest(context.Background(), root, TriggerShareScan)
	if err != nil || second.PendingEvents != 0 {
		t.Fatalf("second report=%#v err=%v", second, err)
	}
	event, err = store.ProjectEvents().GetProjectEvent(context.Background(), "project-order", "event-work-created")
	if err != nil || event.Status != "accepted" {
		t.Fatalf("reconciled event=%#v err=%v", event, err)
	}
}

func TestCorruptRecordIsQuarantinedWithoutBlockingValidRecords(t *testing.T) {
	root, store := setupProjectorTest(t, "project-corrupt")
	corruptRelative := ".agent-project/history/events/2026/08/14/corrupt.json"
	corruptPath := filepath.Join(root, filepath.FromSlash(corruptRelative))
	if err := os.MkdirAll(filepath.Dir(corruptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptPath, []byte(`{"token":"secret","broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(root, "workspace", "outside.json")
	if err := os.WriteFile(outsidePath, []byte(`{"record_kind":"work_event"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := (&Projector{Store: store, Now: fixedProjectorTime}).Ingest(context.Background(), root, TriggerShareScan)
	if err != nil {
		t.Fatal(err)
	}
	if report.RejectedRecords != 1 || report.ProjectedEvents != 1 || len(report.Diagnostics) != 1 {
		t.Fatalf("report = %#v", report)
	}
	diagnostic := report.Diagnostics[0]
	if diagnostic.RelativePath != corruptRelative || diagnostic.QuarantinePath == "" || strings.Contains(diagnostic.Message, root) || strings.Contains(strings.ToLower(diagnostic.Message), "secret") {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(diagnostic.QuarantinePath))); err != nil {
		t.Fatalf("quarantine copy: %v", err)
	}
	if got, err := os.ReadFile(corruptPath); err != nil || !strings.Contains(string(got), "secret") {
		t.Fatalf("authoritative corrupt record was changed: %q, %v", got, err)
	}
	if events := listProjectedEventIdentity(t, store, "project-corrupt"); len(events) != 1 {
		t.Fatalf("outside-layout record entered projection: %#v", events)
	}
}

func TestConcurrentIngestionDoesNotDuplicateEventsOrCorruptCheckpoint(t *testing.T) {
	root, store := setupProjectorTest(t, "project-concurrent")
	const workers = 16
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := (&Projector{Store: store, Now: fixedProjectorTime}).Ingest(context.Background(), root, TriggerShareScan)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if events := listProjectedEventIdentity(t, store, "project-concurrent"); len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	checkpoint, err := store.ProjectCheckpoints().GetProjectCheckpoint(context.Background(), "project-concurrent", CheckpointStream)
	if err != nil || checkpoint.LastRecordHash == "" {
		t.Fatalf("checkpoint=%#v err=%v", checkpoint, err)
	}
}

func TestLifecycleHookSkipsOrdinarySharesAndBindsProjectShare(t *testing.T) {
	ordinaryRoot := t.TempDir()
	projectRoot, store := setupProjectorTest(t, "project-hook")
	hook := LifecycleHook{Projector: &Projector{Store: store}}
	if report, err := hook.AfterShareUpdate(context.Background(), "ordinary-share", ordinaryRoot); err != nil || report.ProjectID != "" {
		t.Fatalf("ordinary share report=%#v err=%v", report, err)
	}
	if _, err := hook.AfterShareUpdate(context.Background(), "wrong-share", projectRoot); !errors.Is(err, ErrShareMismatch) {
		t.Fatalf("share mismatch error = %v", err)
	}
}

func setupProjectorTest(t *testing.T, projectID string) (string, *sqlite.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	shareID := core.ShareID("share-" + projectID)
	if err := store.Shares().SaveShare(context.Background(), storage.Share{ID: shareID, Name: "Project", RootPath: root, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	bootstrapper := project.ProjectBootstrapper{
		Now:         func() time.Time { return time.Date(2026, time.August, 14, 7, 0, 0, 0, time.UTC) },
		NewRecordID: func() (string, error) { return "manifest-" + projectID, nil },
	}
	if _, err := bootstrapper.Bootstrap(project.ProjectBootstrapRequest{
		RootPath: root, ProjectID: projectID, Name: "Project",
		Authority: project.Authority{DeviceID: "device-1", ShareID: string(shareID)},
	}); err != nil {
		t.Fatal(err)
	}
	return root, store
}

func publishWorkPackageCreatedEvent(t *testing.T, root, projectID, workPackageID, definitionRecordID string) {
	t.Helper()
	layout, err := project.NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := jsonBytes(project.WorkPackageCreatedPayload{DefinitionRecordID: definitionRecordID})
	event := project.WorkEvent{
		RecordHeader: project.NewRecordHeader(project.RecordWorkEvent, "event-work-created", projectID),
		EventType:    project.EventWorkPackageCreated, OccurredAt: time.Date(2026, time.August, 14, 8, 0, 0, 0, time.UTC),
		WorkPackageID: workPackageID, Producer: project.Producer{DeviceID: "device-1"}, Payload: payload,
	}
	if _, err := project.PublishRecord(layout, event); err != nil {
		t.Fatal(err)
	}
}

func publishWorkPackageDefinition(t *testing.T, root, projectID, workPackageID, recordID string) {
	t.Helper()
	layout, err := project.NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.August, 14, 8, 1, 0, 0, time.UTC)
	producer := project.Producer{WorkerID: "worker-1", DeviceID: "device-1", Trade: "engineering"}
	record := project.WorkPackageDefinition{
		RecordHeader:  project.NewRecordHeader(project.RecordWorkPackage, recordID, projectID),
		WorkPackageID: workPackageID, Objective: "Implement projection", Trade: "engineering",
		Deliverables: []string{"projection"}, AcceptanceCriteria: []string{"tests pass"}, CreatedAt: createdAt,
		Provenance: project.Provenance{Producer: producer, WorkPackageID: workPackageID, CreatedAt: createdAt},
	}
	if _, err := project.PublishRecord(layout, record); err != nil {
		t.Fatal(err)
	}
}

func publishTestArtifact(t *testing.T, root, artifactID string) {
	t.Helper()
	layout, err := project.NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC)
	record := project.ArtifactManifest{
		RecordHeader: project.NewRecordHeader(project.RecordArtifact, "record-"+artifactID, projectIDFromRoot(t, layout)),
		ArtifactID:   artifactID, Name: artifactID + ".txt", MediaType: "text/plain", Size: 0,
		HashAlgorithm: project.HashAlgorithmSHA256, ContentHash: strings.Repeat("0", 64), CreatedAt: createdAt,
		Provenance: project.Provenance{Producer: project.Producer{DeviceID: "device-1"}, CreatedAt: createdAt},
	}
	if _, err := project.PublishRecord(layout, record); err != nil {
		t.Fatal(err)
	}
}

func projectIDFromRoot(t *testing.T, layout project.Layout) string {
	t.Helper()
	decoded, err := project.ReadPortableRecord(layout, project.ManifestRelativePath)
	if err != nil {
		t.Fatal(err)
	}
	return decoded.Value.(*project.ProjectManifest).ProjectID
}

func listProjectedEventIdentity(t *testing.T, store *sqlite.Store, projectID string) []string {
	t.Helper()
	page, err := store.ProjectEvents().ListProjectEvents(context.Background(), storage.ProjectEventQuery{
		ProjectID: projectID, Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, len(page.Items))
	for index, event := range page.Items {
		result[index] = event.EventID + ":" + event.RecordHash + ":" + event.Status
	}
	return result
}

func jsonBytes(value any) ([]byte, error) {
	return json.Marshal(value)
}

func fixedProjectorTime() time.Time {
	return time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
}
