package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/core"
	"syncgate/internal/project"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	"syncgate/internal/taskspec"
	"syncgate/internal/workhistory"
)

func TestLocalSetupPublishesImmutableTaskSpecificationAndReplays(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	bootstrap, err := (project.ProjectBootstrapper{Now: func() time.Time { return now }}).Bootstrap(project.ProjectBootstrapRequest{
		RootPath: root, ProjectID: "project:setup-pilot", Name: "Setup Pilot",
		Authority: project.Authority{DeviceID: "device:setup-authority", ShareID: "share-setup-pilot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	layout := bootstrap.Preflight.Layout
	manifestRecord, err := project.ReadPortableRecord(layout, project.ManifestRelativePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := manifestRecord.Value.(*project.ProjectManifest)
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "setup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	shareID := core.ShareID("share-setup-pilot")
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: shareID, Name: "Setup Pilot", RootPath: root, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, storage.ProjectRegistration{
		ProjectID: manifest.ProjectID, ShareID: shareID, RootPath: root, Name: manifest.Name,
		AuthorityDeviceID: core.DeviceID(manifest.Authority.DeviceID), ManifestRecordID: manifest.RecordID,
		ManifestRecordHash: manifestRecord.Digest, ManifestPath: project.ManifestRelativePath, RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := workhistory.New(store, &projector.Projector{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	specifications := taskspec.FileStore{Root: filepath.Join(t.TempDir(), "task-specifications")}
	specification := setupSpecification()
	raw, err := json.Marshal(specification)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := specifications.Put(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := NewLocalSetupAdministration(LocalSetupOptions{
		Specifications: specifications, History: history, Projects: store.ProjectRegistrations(), Tasks: store.ProjectTasks(),
		DeviceID: "device:setup-authority",
	})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := admin.ValidateTaskGraph(ctx, api.TaskGraphValidationInput{ProjectID: specification.ProjectID, SpecificationID: specification.SpecificationID, SpecificationDigest: stored.Digest})
	if err != nil || !validation.Valid || validation.TaskCount != 1 || validation.WorkPackageCount != 2 {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}
	input := api.TaskGraphCreateInput{
		ProjectID: specification.ProjectID, TaskID: specification.TaskID, TaskRevision: 1, GraphRevision: 1,
		SpecificationID: specification.SpecificationID, SpecificationDigest: stored.Digest, IdempotencyKey: "create-setup-pilot",
	}
	created, err := admin.CreateTaskGraph(ctx, input)
	if err != nil || created.AlreadyPresent || created.TaskRecordID == "" || created.GraphRecordID == "" || len(created.TaskDigest) != 64 || len(created.GraphDigest) != 64 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	replay, err := admin.CreateTaskGraph(ctx, input)
	if err != nil || !replay.AlreadyPresent || replay.TaskDigest != created.TaskDigest || replay.GraphDigest != created.GraphDigest {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	nodes, err := store.ProjectTaskNodes().ListProjectTaskNodes(ctx, storage.ProjectTaskNodeQuery{
		ProjectID: specification.ProjectID, TaskID: specification.TaskID, TaskRevision: 1, GraphRevision: 1,
		Page: storage.PageRequest{Limit: 10},
	})
	if err != nil || len(nodes.Items) != 2 || nodes.Items[1].Dependencies[0] != "work:implement" {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	conflict := input
	conflict.ProjectID = "project:other"
	if _, err := admin.CreateTaskGraph(ctx, conflict); err == nil {
		t.Fatal("cross-project specification was accepted")
	} else {
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 409 {
			t.Fatalf("cross-project error=%v", err)
		}
	}
}

func TestLocalSetupLeavesLaterAuthorityMethodsExplicitlyUnavailable(t *testing.T) {
	admin := &LocalSetupAdministration{}
	if _, err := admin.ContextPreflight(context.Background(), api.ContextPreflightInput{}); err == nil {
		t.Fatal("context preflight unexpectedly became available")
	}
	if _, err := admin.RuntimePreflight(context.Background(), api.RuntimePreflightInput{}); err == nil {
		t.Fatal("runtime preflight unexpectedly became available")
	}
	if _, err := admin.PreviewExecutionContract(context.Background(), api.ExecutionContractPreviewInput{}); err == nil {
		t.Fatal("contract preview unexpectedly became available")
	}
}

func setupSpecification() taskspec.Specification {
	return taskspec.Specification{
		Schema: taskspec.Schema, SpecificationID: "spec:setup-pilot", ProjectID: "project:setup-pilot",
		CreatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		TaskID:    "task:setup-pilot", TaskRevision: 1, GraphRevision: 1,
		Objective: "publish an immutable supervised task", Priority: project.TaskPriorityHigh,
		QualityGates: []project.QualityGateReference{{GateID: "gate:review", Version: 1, Digest: strings.Repeat("a", 64), Required: true}},
		Barriers:     []string{"work:verify"},
		WorkPackages: []taskspec.WorkPackage{
			{WorkPackageID: "work:implement", Objective: "implement the slice", Trade: "engineering", Scope: project.WorkScope{Allowed: []string{"internal"}}, Deliverables: []string{"implementation"}, AcceptanceCriteria: []string{"focused tests pass"}},
			{WorkPackageID: "work:verify", Objective: "verify the slice", Trade: "quality", Scope: project.WorkScope{Allowed: []string{"tests"}}, Dependencies: []string{"work:implement"}, Deliverables: []string{"verification"}, AcceptanceCriteria: []string{"full suite passes"}, ReviewRequired: true},
		},
	}
}
