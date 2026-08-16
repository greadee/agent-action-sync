package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/insights"
	"syncgate/internal/project"
	"syncgate/internal/projectmigration"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
	syncengine "syncgate/internal/sync"
	"syncgate/internal/workhistory"
)

func TestAgentProjectMigrationConvergesAcrossTrustedDaemonsAndCleanRebuild(t *testing.T) {
	fixture := newTrustedSyncFixture(t)
	fixture.pairDaemons(t)
	fixture.startTarget(t)

	for relativePath, content := range map[string]string{
		"workspace/source.txt":     "existing project content",
		".git/config":              "private git metadata",
		"workspace/.env":           "TOKEN=must-not-sync",
		"workspace/.secrets/token": "must-not-sync",
	} {
		fixture.writeSource(t, relativePath, content)
	}
	migration := &projectmigration.Service{
		Store: fixture.source.Store, Now: fixture.clock,
		Bootstrapper: project.ProjectBootstrapper{Now: fixture.clock, NewRecordID: func() (string, error) { return "manifest-two-daemon", nil }},
		Projector:    &projector.Projector{Store: fixture.source.Store, Now: fixture.clock},
		Insights:     &insights.Calculator{Store: fixture.source.Store, Now: fixture.clock},
	}
	migrationRequest := projectmigration.Request{
		ShareID: fixture.shareID, RootPath: fixture.sourceRoot, ShareMode: storage.ShareOneWaySource,
		ProjectID: "project-two-daemon", Name: "Two Daemon Project", AuthorityDeviceID: fixture.source.Identity.DeviceID,
	}
	preflight, err := migration.Preflight(fixture.ctx, migrationRequest)
	if err != nil || preflight.Status != projectmigration.StatusReady {
		t.Fatalf("migration preflight=%+v err=%v", preflight, err)
	}
	if _, err := migration.Apply(fixture.ctx, migrationRequest, preflight.Confirmation); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	projection := &projector.Projector{Store: fixture.source.Store, Now: fixture.clock}
	history, err := workhistory.New(fixture.source.Store, projection)
	if err != nil {
		t.Fatal(err)
	}
	recordTwoDaemonHistory(t, fixture, history)
	if _, err := (&insights.Calculator{Store: fixture.source.Store, Now: fixture.clock}).Rebuild(fixture.ctx, migrationRequest.ProjectID); err != nil {
		t.Fatal(err)
	}

	diagnostic, err := fixture.source.ScanOnce(fixture.ctx, fixture.shareID)
	if err != nil || !diagnostic.Committed {
		t.Fatalf("source project scan=%+v err=%v", diagnostic, err)
	}
	entries, err := fixture.source.Store.FileIndex().List(fixture.ctx, fixture.shareID)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if entry.EntryType != core.EntryFile {
			continue
		}
		for _, forbidden := range []string{".git/", ".env", ".secrets/", project.LocalDirectory + "/"} {
			if strings.Contains(entry.RelativePath, forbidden) {
				t.Fatalf("excluded project path entered source index: %s", entry.RelativePath)
			}
		}
		paths = append(paths, entry.RelativePath)
	}
	sort.Strings(paths)
	session := fixture.connectSource(t)
	for _, relativePath := range paths {
		revision, err := fixture.source.Store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, relativePath)
		if err != nil {
			t.Fatal(err)
		}
		queued := fixture.queueRevision(t, session.target, revision, syncengine.OneWayActionAdd)
		fixture.waitForJob(t, queued.Job.ID, core.OneWayJobCompleted)
	}
	session.close()

	sourceHashes := portableHashes(t, fixture.sourceRoot)
	targetHashes := portableHashes(t, fixture.targetRoot)
	if !reflect.DeepEqual(sourceHashes, targetHashes) {
		t.Fatalf("portable records did not converge\nsource=%v\ntarget=%v", sourceHashes, targetHashes)
	}
	sourceEvents := projectEventHashes(t, fixture.source.Store, migrationRequest.ProjectID)
	targetEvents := projectEventHashes(t, fixture.target.Store, migrationRequest.ProjectID)
	if !reflect.DeepEqual(sourceEvents, targetEvents) {
		t.Fatalf("history projections differ\nsource=%v\ntarget=%v", sourceEvents, targetEvents)
	}
	sourceInsights := projectInsightValues(t, fixture.source.Store, migrationRequest.ProjectID)
	targetInsights := projectInsightValues(t, fixture.target.Store, migrationRequest.ProjectID)
	if !reflect.DeepEqual(sourceInsights, targetInsights) {
		t.Fatalf("insights differ\nsource=%v\ntarget=%v", sourceInsights, targetInsights)
	}
	for _, path := range []string{".git/config", "workspace/.env", "workspace/.secrets/token", project.LocalDirectory + "/quarantine"} {
		if _, err := os.Stat(filepath.Join(fixture.targetRoot, filepath.FromSlash(path))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("excluded path reached target %s: %v", path, err)
		}
	}

	fixture.stopTarget()
	if err := fixture.source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.target.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.source = fixture.bootstrapSource(t)
	fixture.target = fixture.bootstrapTarget(t)
	if !reflect.DeepEqual(projectEventHashes(t, fixture.source.Store, migrationRequest.ProjectID), projectEventHashes(t, fixture.target.Store, migrationRequest.ProjectID)) {
		t.Fatal("history changed after daemon restart")
	}
	if err := fixture.target.Store.ProjectProjections().ClearProjectProjection(fixture.ctx, migrationRequest.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := (&projector.Projector{Store: fixture.target.Store, Now: fixture.clock}).Rebuild(fixture.ctx, fixture.targetRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := (&insights.Calculator{Store: fixture.target.Store, Now: fixture.clock}).Rebuild(fixture.ctx, migrationRequest.ProjectID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projectEventHashes(t, fixture.source.Store, migrationRequest.ProjectID), projectEventHashes(t, fixture.target.Store, migrationRequest.ProjectID)) ||
		!reflect.DeepEqual(projectInsightValues(t, fixture.source.Store, migrationRequest.ProjectID), projectInsightValues(t, fixture.target.Store, migrationRequest.ProjectID)) {
		t.Fatal("clean target rebuild did not reproduce source history and insights")
	}
}

func recordTwoDaemonHistory(t *testing.T, fixture *trustedSyncFixture, service *workhistory.Service) {
	t.Helper()
	metadata := func(key string, offset int) workhistory.Metadata {
		return workhistory.Metadata{
			RootPath: fixture.sourceRoot, IdempotencyKey: key, OccurredAt: fixture.now.Add(time.Duration(offset) * time.Minute),
			Producer: project.Producer{WorkerID: "worker-one", DeviceID: string(fixture.source.Identity.DeviceID), Trade: "engineering", Provider: "openai", Model: "codex"},
		}
	}
	must := func(_ workhistory.OperationResult, err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(service.CreateWorkPackage(fixture.ctx, workhistory.CreateWorkPackageRequest{
		Metadata: metadata("create", 1), WorkPackageID: "wp-one", Objective: "build synchronized history", Trade: "engineering",
		Scope: project.WorkScope{Allowed: []string{"workspace"}}, Deliverables: []string{"implementation"}, AcceptanceCriteria: []string{"tests pass"}, ReviewRequired: true,
	}))
	must(service.TransitionWorkPackage(fixture.ctx, workhistory.TransitionWorkPackageRequest{Metadata: metadata("ready", 2), WorkPackageID: "wp-one", From: project.WorkPackagePlanned, To: project.WorkPackageReady}))
	must(service.TransitionWorkPackage(fixture.ctx, workhistory.TransitionWorkPackageRequest{Metadata: metadata("active", 3), WorkPackageID: "wp-one", From: project.WorkPackageReady, To: project.WorkPackageInProgress}))
	must(service.StartExecution(fixture.ctx, workhistory.StartExecutionRequest{Metadata: metadata("start", 4), WorkPackageID: "wp-one", ExecutionID: "exec-one"}))
	must(service.RecordTest(fixture.ctx, workhistory.RecordTestRequest{ExecutionRequest: workhistory.ExecutionRequest{Metadata: metadata("test", 5), WorkPackageID: "wp-one", ExecutionID: "exec-one"}, Name: "integration", Outcome: project.TestPassed, DurationMilliseconds: 25}))
	must(service.CompleteExecution(fixture.ctx, workhistory.CompleteExecutionRequest{ExecutionRequest: workhistory.ExecutionRequest{Metadata: metadata("complete", 6), WorkPackageID: "wp-one", ExecutionID: "exec-one"}, Summary: "complete"}))
	must(service.CreateHandoff(fixture.ctx, workhistory.CreateHandoffRequest{
		ExecutionRequest: workhistory.ExecutionRequest{Metadata: metadata("handoff", 7), WorkPackageID: "wp-one", ExecutionID: "exec-one"}, HandoffID: "handoff-one",
		CompletedWork: []string{"implementation"}, ChangedFiles: []string{"workspace/result.txt"}, Decisions: []string{"portable history"},
		Tests: []project.TestResult{{Name: "integration", Outcome: project.TestPassed}}, Limitations: []string{"single authority"},
		UnresolvedIssues: []string{"none"}, Assumptions: []string{"trusted peer"}, FollowUpWork: []string{"release"},
		ReviewRequirements: []string{"review"}, IntegrationConsiderations: []string{"rebuild"}, Confidence: project.ConfidenceHigh,
	}))
	fixture.writeSource(t, "workspace/result.txt", "artifact result")
	must(service.RegisterArtifact(fixture.ctx, workhistory.RegisterArtifactRequest{
		Metadata: metadata("artifact", 8), ArtifactID: "artifact-one", WorkPackageID: "wp-one", ExecutionID: "exec-one",
		Name: "result", MediaType: "text/plain", SourceRelativePath: "workspace/result.txt", EmbedBlob: true,
	}))
	must(service.TransitionWorkPackage(fixture.ctx, workhistory.TransitionWorkPackageRequest{Metadata: metadata("review", 9), WorkPackageID: "wp-one", From: project.WorkPackageInProgress, To: project.WorkPackageReview}))
	must(service.RecordReview(fixture.ctx, workhistory.RecordReviewRequest{Metadata: metadata("reviewed", 10), WorkPackageID: "wp-one", ExecutionID: "exec-one", Outcome: project.ReviewApproved, ReviewerID: "reviewer-one", Summary: "approved"}))
	must(service.AcceptWork(fixture.ctx, workhistory.AcceptWorkRequest{Metadata: metadata("accepted", 11), WorkPackageID: "wp-one", AcceptedBy: "reviewer-one", Summary: "accepted"}))
}

func portableHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	control := filepath.Join(root, project.ControlDirectory)
	err := filepath.WalkDir(control, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == project.LocalDirectory || strings.HasPrefix(relative, project.LocalDirectory+"/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || strings.HasSuffix(relative, project.TemporaryRecordSuffix) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		result[relative] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func projectEventHashes(t *testing.T, store storage.Store, projectID string) map[string]string {
	t.Helper()
	page, err := store.ProjectEvents().ListProjectEvents(context.Background(), storage.ProjectEventQuery{ProjectID: projectID, Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(page.Items))
	for _, event := range page.Items {
		result[event.EventID] = event.RecordHash
	}
	return result
}

func projectInsightValues(t *testing.T, store storage.Store, projectID string) map[string]string {
	t.Helper()
	page, err := store.ProjectInsights().ListProjectInsights(context.Background(), storage.ProjectInsightQuery{ProjectID: projectID, Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(page.Items))
	for _, insight := range page.Items {
		result[insight.Scope+"/"+insight.MetricName+"/"+strconv.Itoa(insight.DefinitionVersion)] = string(insight.ValueJSON) + "/" + insight.SourceEventWatermark
	}
	return result
}
