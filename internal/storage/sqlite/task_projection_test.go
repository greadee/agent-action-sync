package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"syncgate/internal/storage"
)

func TestTaskProjectionIsIdempotentConflictSafeAndPaginated(t *testing.T) {
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-tasks")
	ctx := context.Background()
	when := time.Date(2026, time.August, 17, 11, 0, 0, 0, time.UTC)
	task := storage.ProjectTaskProjection{
		ProjectID: "project-tasks", TaskID: "task:one", TaskRevision: 1, GraphRevision: 1,
		TaskRecordID: "task-record", TaskRecordHash: projectionHash("a"), TaskRecordPath: ".agent-project/tasks/task:one/revisions/1/task.json",
		GraphRecordID: "graph-record", GraphRecordHash: projectionHash("b"), GraphRecordPath: ".agent-project/tasks/task:one/revisions/1/graphs/1.json",
		Objective: "objective", Priority: "normal", State: "ready", ExplanationCode: "task_has_ready_work",
		EventWatermark: projectionHash("c"), ParallelReady: []string{"wp-a"}, CreatedAt: when,
	}
	if result, err := store.ProjectTasks().SaveProjectTask(ctx, task); err != nil || result.AlreadyPresent {
		t.Fatalf("first save=%+v err=%v", result, err)
	}
	task.State = "waiting"
	task.ExplanationCode = "task_waiting"
	task.EventWatermark = projectionHash("d")
	task.ParallelReady = nil
	if result, err := store.ProjectTasks().SaveProjectTask(ctx, task); err != nil || !result.AlreadyPresent {
		t.Fatalf("readiness update=%+v err=%v", result, err)
	}
	got, err := store.ProjectTasks().GetProjectTask(ctx, task.ProjectID, task.TaskID, 1)
	if err != nil || got.State != "waiting" || len(got.ParallelReady) != 0 {
		t.Fatalf("task=%+v err=%v", got, err)
	}
	conflict := task
	conflict.GraphRecordHash = projectionHash("e")
	if _, err := store.ProjectTasks().SaveProjectTask(ctx, conflict); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflicting task error=%v", err)
	}

	for _, id := range []string{"wp-c", "wp-a", "wp-b"} {
		node := storage.ProjectTaskNodeProjection{
			ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1,
			WorkPackageID: id, DefinitionRecordID: "record-" + id, DefinitionHash: projectionHash("f"),
			DefinitionPath: ".agent-project/work-packages/" + id + "/definition.json",
			Readiness:      "ready", ExplanationCode: "dependencies_satisfied", Dependencies: []string{}, Barrier: id == "wp-c",
		}
		if _, err := store.ProjectTaskNodes().SaveProjectTaskNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ProjectTaskNodes().ListProjectTaskNodes(ctx, storage.ProjectTaskNodeQuery{
		ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1, Page: storage.PageRequest{Limit: 2},
	})
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil || first.Items[0].WorkPackageID != "wp-a" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	second, err := store.ProjectTaskNodes().ListProjectTaskNodes(ctx, storage.ProjectTaskNodeQuery{
		ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1,
		Page: storage.PageRequest{Limit: 2, Cursor: *first.NextCursor},
	})
	if err != nil || !reflect.DeepEqual([]string{second.Items[0].WorkPackageID}, []string{"wp-c"}) {
		t.Fatalf("second page=%+v err=%v", second, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ProjectTasks().ListProjectTasks(canceled, storage.ProjectTaskQuery{ProjectID: task.ProjectID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled task query error=%v", err)
	}
}
