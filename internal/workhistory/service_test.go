package workhistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/project"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

type historyFixture struct {
	root      string
	dbPath    string
	store     *sqlite.Store
	projector *projector.Projector
	service   *Service
	when      time.Time
}

func newHistoryFixture(t *testing.T) *historyFixture {
	t.Helper()
	root := t.TempDir()
	if _, err := project.BootstrapProject(project.ProjectBootstrapRequest{
		RootPath: root, ProjectID: "project-stage-seven", Name: "Stage Seven",
		Authority: project.Authority{DeviceID: "device-local", ShareID: "share-project"},
	}); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "history.db")
	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Shares().SaveShare(context.Background(), storage.Share{ID: core.ShareID("share-project"), Name: "Stage Seven", RootPath: root, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	projection := &projector.Projector{Store: store}
	service, err := New(store, projection)
	if err != nil {
		t.Fatal(err)
	}
	return &historyFixture{root: root, dbPath: dbPath, store: store, projector: projection, service: service, when: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)}
}

func (fixture *historyFixture) metadata(key string, offset int) Metadata {
	return Metadata{
		RootPath: fixture.root, IdempotencyKey: key, OccurredAt: fixture.when.Add(time.Duration(offset) * time.Minute),
		Producer:    project.Producer{WorkerID: "worker-one", DeviceID: "device-local", Trade: "engineering", Provider: "openai", Model: "codex"},
		Correlation: &project.Correlation{AuditID: "audit-" + key, RevisionID: "revision-" + key},
	}
}

func (fixture *historyFixture) createWork(t *testing.T, objective string) CreateWorkPackageRequest {
	t.Helper()
	request := CreateWorkPackageRequest{
		Metadata: fixture.metadata("create", 1), WorkPackageID: "wp-one", Objective: objective, Trade: "engineering",
		Scope: project.WorkScope{Allowed: []string{"workspace"}}, Deliverables: []string{"implementation"},
		AcceptanceCriteria: []string{"tests pass"}, ReviewRequired: true,
	}
	if _, err := fixture.service.CreateWorkPackage(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	return request
}

func (fixture *historyFixture) transition(t *testing.T, key string, offset int, from, to project.WorkPackageState) {
	t.Helper()
	_, err := fixture.service.TransitionWorkPackage(context.Background(), TransitionWorkPackageRequest{
		Metadata: fixture.metadata(key, offset), WorkPackageID: "wp-one", From: from, To: to,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkPackageReplayConflictInvalidOrderAndConcurrentDuplicate(t *testing.T) {
	fixture := newHistoryFixture(t)
	request := fixture.createWork(t, "build the writer")

	replay, err := fixture.service.CreateWorkPackage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range replay.Records {
		if !record.AlreadyPresent || record.Created || filepath.IsAbs(record.RelativePath) {
			t.Fatalf("unexpected replay record: %+v", record)
		}
	}
	conflict := request
	conflict.Objective = "different content"
	if _, err := fixture.service.CreateWorkPackage(context.Background(), conflict); !errors.Is(err, project.ErrRecordConflict) {
		t.Fatalf("expected conflicting replay, got %v", err)
	}

	before := countEvents(t, fixture.service, "project-stage-seven")
	_, err = fixture.service.TransitionWorkPackage(context.Background(), TransitionWorkPackageRequest{
		Metadata: fixture.metadata("invalid", 2), WorkPackageID: "wp-one", From: project.WorkPackagePlanned, To: project.WorkPackageInProgress,
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition, got %v", err)
	}
	if after := countEvents(t, fixture.service, "project-stage-seven"); after != before {
		t.Fatalf("invalid transition created an event: before=%d after=%d", before, after)
	}

	duplicate := TransitionWorkPackageRequest{Metadata: fixture.metadata("ready", 3), WorkPackageID: "wp-one", From: project.WorkPackagePlanned, To: project.WorkPackageReady}
	var failures atomic.Int32
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, callErr := fixture.service.TransitionWorkPackage(context.Background(), duplicate); callErr != nil {
				failures.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 {
		t.Fatalf("concurrent replay failures: %d", failures.Load())
	}
}

func TestExecutionReviewAcceptanceHandoffArtifactPrivacyAndRebuild(t *testing.T) {
	fixture := newHistoryFixture(t)
	privateText := strings.Join([]string{"pass", "word=example-value"}, "") + " " + strings.Join([]string{"C:", `\private`, `\worker`, `\notes.txt`}, "")
	fixture.createWork(t, "build output "+privateText)
	fixture.transition(t, "ready", 2, project.WorkPackagePlanned, project.WorkPackageReady)
	fixture.transition(t, "active", 3, project.WorkPackageReady, project.WorkPackageInProgress)

	start := StartExecutionRequest{Metadata: fixture.metadata("start", 4), WorkPackageID: "wp-one", ExecutionID: "exec-one"}
	if _, err := fixture.service.StartExecution(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	testRequest := RecordTestRequest{ExecutionRequest: ExecutionRequest{Metadata: fixture.metadata("test", 5), WorkPackageID: "wp-one", ExecutionID: "exec-one"}, Name: "unit suite", Outcome: project.TestPassed, DurationMilliseconds: 42}
	if _, err := fixture.service.RecordTest(context.Background(), testRequest); err != nil {
		t.Fatal(err)
	}
	conflictingTest := testRequest
	conflictingTest.OccurredAt = conflictingTest.OccurredAt.Add(time.Second)
	if _, err := fixture.service.RecordTest(context.Background(), conflictingTest); !errors.Is(err, project.ErrRecordConflict) {
		t.Fatalf("expected cross-path idempotency conflict, got %v", err)
	}
	if _, err := fixture.service.PauseExecution(context.Background(), PauseExecutionRequest{ExecutionRequest: ExecutionRequest{Metadata: fixture.metadata("pause", 6), WorkPackageID: "wp-one", ExecutionID: "exec-one"}, ReasonCode: "checkpoint"}); err != nil {
		t.Fatal(err)
	}
	resume := ExecutionRequest{Metadata: fixture.metadata("resume", 7), WorkPackageID: "wp-one", ExecutionID: "exec-one"}
	if _, err := fixture.service.ResumeExecution(context.Background(), resume); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CompleteExecution(context.Background(), CompleteExecutionRequest{ExecutionRequest: ExecutionRequest{Metadata: fixture.metadata("complete", 8), WorkPackageID: "wp-one", ExecutionID: "exec-one"}, Summary: "complete " + privateText}); err != nil {
		t.Fatal(err)
	}

	handoff, err := fixture.service.CreateHandoff(context.Background(), CreateHandoffRequest{
		ExecutionRequest: ExecutionRequest{Metadata: fixture.metadata("handoff", 9), WorkPackageID: "wp-one", ExecutionID: "exec-one"}, HandoffID: "handoff-one",
		CompletedWork: []string{"writer complete"}, ChangedFiles: []string{"workspace/result.txt"}, Decisions: []string{"immutable events"},
		Tests: []project.TestResult{{Name: "unit suite", Outcome: project.TestPassed}}, Limitations: []string{"single writer"},
		UnresolvedIssues: []string{"none known"}, Assumptions: []string{"trusted caller"}, FollowUpWork: []string{"stage eight"},
		ReviewRequirements: []string{"inspect records"}, IntegrationConsiderations: []string{"rebuild projection"},
		Confidence: project.ConfidenceHigh, FailureConditions: []string{"projection unavailable"},
	})
	if err != nil || len(handoff.Records) != 2 {
		t.Fatalf("handoff failed: result=%+v err=%v", handoff, err)
	}

	if err := os.WriteFile(filepath.Join(fixture.root, "workspace", "result.txt"), []byte("artifact body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := fixture.service.RegisterArtifact(context.Background(), RegisterArtifactRequest{
		Metadata: fixture.metadata("artifact", 10), ArtifactID: "artifact-one", WorkPackageID: "wp-one", ExecutionID: "exec-one",
		Name: "result", MediaType: "text/plain", SourceRelativePath: "workspace/result.txt", EmbedBlob: true,
	})
	if err != nil || len(artifact.Records) != 3 || !strings.HasPrefix(artifact.Records[0].RelativePath, project.ArtifactBlobsDirectory+"/") {
		t.Fatalf("artifact failed: result=%+v err=%v", artifact, err)
	}

	fixture.transition(t, "review", 11, project.WorkPackageInProgress, project.WorkPackageReview)
	if _, err := fixture.service.AcceptWork(context.Background(), AcceptWorkRequest{Metadata: fixture.metadata("accept-too-early", 12), WorkPackageID: "wp-one", AcceptedBy: "reviewer-one"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected acceptance to require approval, got %v", err)
	}
	if _, err := fixture.service.RecordReview(context.Background(), RecordReviewRequest{Metadata: fixture.metadata("review-result", 12), WorkPackageID: "wp-one", ExecutionID: "exec-one", Outcome: project.ReviewApproved, ReviewerID: "reviewer-one", Summary: "approved"}); err != nil {
		t.Fatal(err)
	}
	accept := AcceptWorkRequest{Metadata: fixture.metadata("accept", 13), WorkPackageID: "wp-one", AcceptedBy: "reviewer-one", Summary: "accepted"}
	if _, err := fixture.service.AcceptWork(context.Background(), accept); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.AcceptWork(context.Background(), accept); err != nil {
		t.Fatalf("acceptance replay failed: %v", err)
	}

	portable, err := collectPortableText(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(portable, "example-value") || strings.Contains(portable, `private\worker`) || strings.Contains(portable, fixture.root) {
		t.Fatal("portable history contains private local data")
	}

	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.store = reopened
	fixture.projector = &projector.Projector{Store: reopened}
	fixture.service, err = New(reopened, fixture.projector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.projector.Rebuild(context.Background(), fixture.root); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.GetArtifact(context.Background(), "project-stage-seven", "artifact-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ListEvents(context.Background(), storage.ProjectEventQuery{ProjectID: "project-stage-seven", Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit + 1}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected bounded query rejection, got %v", err)
	}
}

func TestFailureAndRecoverableProjectionError(t *testing.T) {
	fixture := newHistoryFixture(t)
	var calls atomic.Int32
	fixture.service.Project = func(ctx context.Context, root string, trigger projector.Trigger) (projector.Report, error) {
		if calls.Add(1) == 2 {
			return projector.Report{}, errors.New("projection unavailable")
		}
		return fixture.projector.Ingest(ctx, root, trigger)
	}
	request := CreateWorkPackageRequest{
		Metadata: fixture.metadata("recover", 1), WorkPackageID: "wp-recover", Objective: "durable first", Trade: "engineering",
		Scope: project.WorkScope{Allowed: []string{"workspace"}}, Deliverables: []string{"record"}, AcceptanceCriteria: []string{"recoverable"},
	}
	result, err := fixture.service.CreateWorkPackage(context.Background(), request)
	var recoverable *RecoverableProjectionError
	if !errors.As(err, &recoverable) || len(result.Records) != 2 {
		t.Fatalf("expected recoverable projection error, result=%+v err=%v", result, err)
	}
	if _, err := fixture.projector.Ingest(context.Background(), fixture.root, projector.TriggerLocalWrite); err != nil {
		t.Fatal(err)
	}
	if countEvents(t, fixture.service, "project-stage-seven") < 2 {
		t.Fatal("canonical event was not recovered into projection")
	}

	fixture.service.Project = nil
	if _, err := fixture.service.TransitionWorkPackage(context.Background(), TransitionWorkPackageRequest{Metadata: fixture.metadata("recover-ready", 2), WorkPackageID: "wp-recover", From: project.WorkPackagePlanned, To: project.WorkPackageReady}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.TransitionWorkPackage(context.Background(), TransitionWorkPackageRequest{Metadata: fixture.metadata("recover-active", 3), WorkPackageID: "wp-recover", From: project.WorkPackageReady, To: project.WorkPackageInProgress}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.StartExecution(context.Background(), StartExecutionRequest{Metadata: fixture.metadata("recover-start", 4), WorkPackageID: "wp-recover", ExecutionID: "exec-fail"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.FailExecution(context.Background(), FailExecutionRequest{ExecutionRequest: ExecutionRequest{Metadata: fixture.metadata("recover-fail", 5), WorkPackageID: "wp-recover", ExecutionID: "exec-fail"}, FailureCode: "test_failure", Summary: "expected failure"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CompleteExecution(context.Background(), CompleteExecutionRequest{ExecutionRequest: ExecutionRequest{Metadata: fixture.metadata("after-fail", 6), WorkPackageID: "wp-recover", ExecutionID: "exec-fail"}}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected terminal execution rejection, got %v", err)
	}
}

func countEvents(t *testing.T, service *Service, projectID string) int {
	t.Helper()
	page, err := service.ListEvents(context.Background(), storage.ProjectEventQuery{ProjectID: projectID, Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit}})
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Items)
}

func collectPortableText(root string) (string, error) {
	var combined strings.Builder
	err := filepath.WalkDir(filepath.Join(root, project.ControlDirectory), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		combined.Write(raw)
		return nil
	})
	return combined.String(), err
}
