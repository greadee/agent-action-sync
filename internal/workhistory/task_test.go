package workhistory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
)

func TestCreateTaskIsIdempotentProjectsReadinessAndRejectsRevisionConflict(t *testing.T) {
	fixture := newHistoryFixture(t)
	request := taskRequest(fixture)
	result, err := fixture.service.CreateTask(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 8 {
		t.Fatalf("task record count = %d, want 8", len(result.Records))
	}
	replay, err := fixture.service.CreateTask(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range replay.Records {
		if !record.AlreadyPresent || record.Created {
			t.Fatalf("task replay record = %+v", record)
		}
	}

	task, err := fixture.store.ProjectTasks().GetProjectTask(context.Background(), "project-stage-seven", "task:release", 1)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != string(orchestration.ReadinessReady) || task.ExplanationCode != orchestration.ReasonTaskHasReadyWork || !reflect.DeepEqual(task.ParallelReady, []string{"wp-a", "wp-c"}) {
		t.Fatalf("task projection = %+v", task)
	}
	first, err := fixture.store.ProjectTaskNodes().ListProjectTaskNodes(context.Background(), storage.ProjectTaskNodeQuery{
		ProjectID: "project-stage-seven", TaskID: "task:release", TaskRevision: 1, GraphRevision: 1,
		Page: storage.PageRequest{Limit: 2},
	})
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil || first.Items[0].WorkPackageID != "wp-a" {
		t.Fatalf("first node page=%+v err=%v", first, err)
	}
	second, err := fixture.store.ProjectTaskNodes().ListProjectTaskNodes(context.Background(), storage.ProjectTaskNodeQuery{
		ProjectID: "project-stage-seven", TaskID: "task:release", TaskRevision: 1, GraphRevision: 1,
		Page: storage.PageRequest{Limit: 2, Cursor: *first.NextCursor},
	})
	if err != nil || len(second.Items) != 1 || second.Items[0].WorkPackageID != "wp-c" {
		t.Fatalf("second node page=%+v err=%v", second, err)
	}

	conflict := request
	conflict.Objective = "different immutable task"
	if _, err := fixture.service.CreateTask(context.Background(), conflict); !errors.Is(err, project.ErrRecordConflict) {
		t.Fatalf("task revision conflict error = %v", err)
	}
	differentKey := request
	differentKey.IdempotencyKey = "different-key"
	if _, err := fixture.service.CreateTask(context.Background(), differentKey); !errors.Is(err, project.ErrRecordConflict) {
		t.Fatalf("same revision with another identity error = %v", err)
	}
}

func TestCreateTaskRejectsMissingCycleAndCancellationBeforePublication(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*CreateTaskRequest)
	}{
		{name: "missing dependency", mutate: func(request *CreateTaskRequest) {
			request.WorkPackages[1].Dependencies = []string{"wp-missing"}
		}},
		{name: "cycle", mutate: func(request *CreateTaskRequest) {
			request.WorkPackages[0].Dependencies = []string{"wp-b"}
			request.WorkPackages[1].Dependencies = []string{"wp-c"}
		}},
		{name: "duplicate edge", mutate: func(request *CreateTaskRequest) {
			request.WorkPackages[1].Dependencies = []string{"wp-a", "wp-a"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHistoryFixture(t)
			request := taskRequest(fixture)
			test.mutate(&request)
			if _, err := fixture.service.CreateTask(context.Background(), request); !errors.Is(err, orchestration.ErrInvalidGraph) && !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("CreateTask error = %v", err)
			}
			path, err := project.TaskRevisionRelativePath(request.TaskID, request.TaskRevision)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := project.NewLayout(fixture.root)
			if err != nil {
				t.Fatal(err)
			}
			absolute, err := resolved.ResolvePortable(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(absolute); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid task published a record: %v", err)
			}
		})
	}

	fixture := newHistoryFixture(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.service.CreateTask(canceled, taskRequest(fixture)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled task error = %v", err)
	}
}

func TestTaskReadinessIncrementalProjectionEqualsCleanRebuild(t *testing.T) {
	fixture := newHistoryFixture(t)
	request := taskRequest(fixture)
	if _, err := fixture.service.CreateTask(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	transitionTaskWork(t, fixture, "wp-a", "a-ready", project.WorkPackagePlanned, project.WorkPackageReady, 2)
	transitionTaskWork(t, fixture, "wp-a", "a-active", project.WorkPackageReady, project.WorkPackageInProgress, 3)
	transitionTaskWork(t, fixture, "wp-a", "a-review", project.WorkPackageInProgress, project.WorkPackageReview, 4)
	if _, err := fixture.service.RecordReview(context.Background(), RecordReviewRequest{
		Metadata: fixture.metadata("a-approved", 5), WorkPackageID: "wp-a", Outcome: project.ReviewApproved, ReviewerID: "reviewer-one",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.AcceptWork(context.Background(), AcceptWorkRequest{
		Metadata: fixture.metadata("a-accepted", 6), WorkPackageID: "wp-a", AcceptedBy: "reviewer-one",
	}); err != nil {
		t.Fatal(err)
	}

	incrementalTask, incrementalNodes := readTaskProjection(t, fixture)
	if incrementalTask.State != string(orchestration.ReadinessReady) || !reflect.DeepEqual(incrementalTask.ParallelReady, []string{"wp-b", "wp-c"}) {
		t.Fatalf("incremental task = %+v", incrementalTask)
	}
	if err := fixture.store.ProjectProjections().ClearProjectProjection(context.Background(), "project-stage-seven"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.projector.Rebuild(context.Background(), fixture.root); err != nil {
		t.Fatal(err)
	}
	rebuiltTask, rebuiltNodes := readTaskProjection(t, fixture)
	if !reflect.DeepEqual(incrementalTask, rebuiltTask) || !reflect.DeepEqual(incrementalNodes, rebuiltNodes) {
		t.Fatalf("rebuild diverged:\nincremental=%+v %+v\nrebuilt=%+v %+v", incrementalTask, incrementalNodes, rebuiltTask, rebuiltNodes)
	}
}

func TestTaskProjectionRecoversWhenLateDefinitionArrives(t *testing.T) {
	fixture := newHistoryFixture(t)
	request := taskRequest(fixture)
	if _, err := fixture.service.CreateTask(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	layout, err := project.NewLayout(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := project.WorkPackageDefinitionRelativePath("wp-b")
	if err != nil {
		t.Fatal(err)
	}
	definitionPath, err := layout.ResolvePortable(relative)
	if err != nil {
		t.Fatal(err)
	}
	latePath := filepath.Join(fixture.root, project.WorkspaceDirectory, "late-wp-b.json")
	if err := os.Rename(definitionPath, latePath); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.projector.Ingest(context.Background(), fixture.root, projector.TriggerShareScan); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ProjectTasks().GetProjectTask(context.Background(), "project-stage-seven", request.TaskID, 1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("incomplete graph task projection error = %v", err)
	}
	if err := os.Rename(latePath, definitionPath); err != nil {
		t.Fatal(err)
	}
	report, err := fixture.projector.Ingest(context.Background(), fixture.root, projector.TriggerShareScan)
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectedTasks != 1 || report.ProjectedTaskNodes != 3 {
		t.Fatalf("late definition report = %+v", report)
	}
	if _, err := fixture.store.ProjectTasks().GetProjectTask(context.Background(), "project-stage-seven", request.TaskID, 1); err != nil {
		t.Fatalf("recovered task projection: %v", err)
	}
}

func taskRequest(fixture *historyFixture) CreateTaskRequest {
	return CreateTaskRequest{
		Metadata: fixture.metadata("create-task", 1), TaskID: "task:release", TaskRevision: 1, GraphRevision: 1,
		Objective: "prepare a release", Priority: project.TaskPriorityHigh,
		Risk:         []project.RiskDimension{{Name: "security", Level: project.RiskHigh}},
		Resources:    &project.ResourceConstraints{RequiredCapabilities: []string{"write"}, RequiredTools: []string{"go"}},
		QualityGates: []project.QualityGateReference{{GateID: "gate:review", Version: 1, Digest: strings64("a"), Required: true}},
		Barriers:     []string{"wp-b"},
		WorkPackages: []TaskWorkPackageRequest{
			{WorkPackageID: "wp-c", Objective: "write documentation", Trade: "documentation", Scope: project.WorkScope{Allowed: []string{"docs"}}, Deliverables: []string{"documentation"}, AcceptanceCriteria: []string{"reviewed"}},
			{WorkPackageID: "wp-b", Objective: "integrate implementation", Trade: "engineering", Dependencies: []string{"wp-a"}, Scope: project.WorkScope{Allowed: []string{"internal"}}, Deliverables: []string{"integration"}, AcceptanceCriteria: []string{"tests pass"}},
			{WorkPackageID: "wp-a", Objective: "build implementation", Trade: "engineering", Scope: project.WorkScope{Allowed: []string{"internal"}}, Deliverables: []string{"implementation"}, AcceptanceCriteria: []string{"tests pass"}},
		},
	}
}

func transitionTaskWork(t *testing.T, fixture *historyFixture, workPackageID, key string, from, to project.WorkPackageState, offset int) {
	t.Helper()
	if _, err := fixture.service.TransitionWorkPackage(context.Background(), TransitionWorkPackageRequest{
		Metadata: fixture.metadata(key, offset), WorkPackageID: workPackageID, From: from, To: to,
	}); err != nil {
		t.Fatal(err)
	}
}

func readTaskProjection(t *testing.T, fixture *historyFixture) (storage.ProjectTaskProjection, []storage.ProjectTaskNodeProjection) {
	t.Helper()
	task, err := fixture.store.ProjectTasks().GetProjectTask(context.Background(), "project-stage-seven", "task:release", 1)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := fixture.store.ProjectTaskNodes().ListProjectTaskNodes(context.Background(), storage.ProjectTaskNodeQuery{
		ProjectID: "project-stage-seven", TaskID: "task:release", TaskRevision: 1, GraphRevision: 1,
		Page: storage.PageRequest{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	return task, nodes.Items
}

func strings64(character string) string {
	result := ""
	for len(result) < 64 {
		result += character
	}
	return result
}
