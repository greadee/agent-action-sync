package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"syncgate/internal/project"
)

func TestValidateGraphRejectsInvalidShapesAndHonorsCancellation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Graph)
		want   error
	}{
		{name: "missing dependency", mutate: func(graph *Graph) {
			definition := graph.Definitions["wp-b"]
			definition.Record.Dependencies = []string{"wp-missing"}
			graph.Definitions["wp-b"] = definition
			setDependencyDigest(t, graph)
		}, want: ErrInvalidGraph},
		{name: "self dependency", mutate: func(graph *Graph) {
			definition := graph.Definitions["wp-a"]
			definition.Record.Dependencies = []string{"wp-a"}
			graph.Definitions["wp-a"] = definition
			setDependencyDigest(t, graph)
		}, want: ErrInvalidGraph},
		{name: "duplicate edge", mutate: func(graph *Graph) {
			definition := graph.Definitions["wp-b"]
			definition.Record.Dependencies = []string{"wp-a", "wp-a"}
			graph.Definitions["wp-b"] = definition
			setDependencyDigest(t, graph)
		}, want: ErrInvalidGraph},
		{name: "cycle", mutate: func(graph *Graph) {
			left := graph.Definitions["wp-a"]
			left.Record.Dependencies = []string{"wp-b"}
			graph.Definitions["wp-a"] = left
			right := graph.Definitions["wp-b"]
			right.Record.Dependencies = []string{"wp-a"}
			graph.Definitions["wp-b"] = right
			setDependencyDigest(t, graph)
		}, want: ErrInvalidGraph},
		{name: "cross project", mutate: func(graph *Graph) {
			definition := graph.Definitions["wp-b"]
			definition.Record.ProjectID = "project-other"
			graph.Definitions["wp-b"] = definition
		}, want: ErrInvalidGraph},
		{name: "definition digest conflict", mutate: func(graph *Graph) {
			graph.Revision.Members[0].DefinitionDigest = strings.Repeat("f", 64)
		}, want: ErrGraphRevisionConflict},
		{name: "task graph revision conflict", mutate: func(graph *Graph) {
			graph.Revision.Revision++
		}, want: ErrGraphRevisionConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			graph := testGraph(t, map[string][]string{"wp-a": nil, "wp-b": {"wp-a"}}, nil)
			test.mutate(&graph)
			if err := ValidateGraph(context.Background(), graph); !errors.Is(err, test.want) {
				t.Fatalf("ValidateGraph error = %v, want %v", err, test.want)
			}
		})
	}

	large := make(map[string][]string)
	for index := 0; index < 100; index++ {
		id := fmt.Sprintf("wp-%03d", index)
		large[id] = nil
		for dependency := 0; dependency < index; dependency++ {
			large[id] = append(large[id], fmt.Sprintf("wp-%03d", dependency))
		}
	}
	graph := testGraph(t, large, nil)
	if err := ValidateGraph(context.Background(), graph); !errors.Is(err, ErrInvalidGraph) || !strings.Contains(err.Error(), "4096") {
		t.Fatalf("large graph error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ValidateGraph(canceled, testGraph(t, map[string][]string{"wp-a": nil}, nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled graph error = %v", err)
	}
}

func TestDiamondReadinessParallelNodesAndBarrier(t *testing.T) {
	graph := testGraph(t, map[string][]string{
		"wp-a": nil, "wp-b": nil, "wp-c": {"wp-a"}, "wp-d": {"wp-a"}, "wp-e": {"wp-c", "wp-d"},
	}, []string{"wp-e"})
	events := append(createdEvents(graph, time.Unix(100, 0).UTC()), acceptedEvents("wp-a", time.Unix(101, 0).UTC())...)
	snapshot, err := ReduceReadiness(context.Background(), graph, events)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.ParallelReady, []string{"wp-b", "wp-c", "wp-d"}) || snapshot.State != ReadinessReady || snapshot.ExplanationCode != ReasonTaskHasReadyWork {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	nodes := nodesByID(snapshot)
	if nodes["wp-e"].State != ReadinessWaiting || nodes["wp-e"].ExplanationCode != ReasonDependencyWaiting || !nodes["wp-e"].Barrier {
		t.Fatalf("barrier node = %+v", nodes["wp-e"])
	}
}

func TestReadinessFailuresRetryReopenCancellationAndTiedReplay(t *testing.T) {
	base := testGraph(t, map[string][]string{"wp-a": nil, "wp-b": {"wp-a"}}, nil)
	when := time.Unix(200, 0).UTC()
	created := createdEvents(base, when)

	failure := append(append([]Event{}, created...), transitionEvents("wp-a", when, project.WorkPackagePlanned, project.WorkPackageReady, project.WorkPackageInProgress, project.WorkPackageFailed)...)
	failedSnapshot, err := ReduceReadiness(context.Background(), base, failure)
	if err != nil {
		t.Fatal(err)
	}
	if nodesByID(failedSnapshot)["wp-b"].State != ReadinessBlocked {
		t.Fatalf("failed dependency snapshot = %+v", failedSnapshot)
	}

	retried := append(append([]Event{}, failure...), stateEvent("wp-a", "evt-90-retry", when, project.WorkPackageFailed, project.WorkPackageReady))
	retrySnapshot, err := ReduceReadiness(context.Background(), base, retried)
	if err != nil {
		t.Fatal(err)
	}
	if nodesByID(retrySnapshot)["wp-a"].State != ReadinessReady || nodesByID(retrySnapshot)["wp-b"].State != ReadinessWaiting {
		t.Fatalf("retry snapshot = %+v", retrySnapshot)
	}

	canceled := append(append([]Event{}, created...), stateEvent("wp-a", "evt-10-cancel", when, project.WorkPackagePlanned, project.WorkPackageCanceled))
	canceledSnapshot, err := ReduceReadiness(context.Background(), base, canceled)
	if err != nil {
		t.Fatal(err)
	}
	if nodesByID(canceledSnapshot)["wp-b"].State != ReadinessBlocked {
		t.Fatalf("canceled dependency snapshot = %+v", canceledSnapshot)
	}

	reviewEvents := append(append([]Event{}, created...), transitionEvents("wp-a", when, project.WorkPackagePlanned, project.WorkPackageReady, project.WorkPackageInProgress, project.WorkPackageReview)...)
	reviewSnapshot, err := ReduceReadiness(context.Background(), base, reviewEvents)
	if err != nil || nodesByID(reviewSnapshot)["wp-a"].State != ReadinessReview {
		t.Fatalf("review snapshot=%+v err=%v", reviewSnapshot, err)
	}
	reopened := append(reviewEvents, stateEvent("wp-a", "evt-90-reopen", when, project.WorkPackageReview, project.WorkPackageInProgress))
	reopenedSnapshot, err := ReduceReadiness(context.Background(), base, reopened)
	if err != nil || nodesByID(reopenedSnapshot)["wp-a"].State != ReadinessWaiting {
		t.Fatalf("reopened snapshot=%+v err=%v", reopenedSnapshot, err)
	}

	shuffled := append([]Event(nil), retried...)
	for left, right := 0, len(shuffled)-1; left < right; left, right = left+1, right-1 {
		shuffled[left], shuffled[right] = shuffled[right], shuffled[left]
	}
	replay, err := ReduceReadiness(context.Background(), base, shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retrySnapshot, replay) {
		t.Fatalf("tied replay diverged:\nfirst=%+v\nreplay=%+v", retrySnapshot, replay)
	}
}

func testGraph(t *testing.T, dependencies map[string][]string, barriers []string) Graph {
	t.Helper()
	definitions := make(map[string]WorkPackageDefinition, len(dependencies))
	ids := make([]string, 0, len(dependencies))
	for id := range dependencies {
		ids = append(ids, id)
	}
	sortStrings(ids)
	members := make([]project.DependencyGraphMember, 0, len(ids))
	for index, id := range ids {
		digest := strings.Repeat(fmt.Sprintf("%x", (index%15)+1), 64)
		record := project.WorkPackageDefinition{
			RecordHeader:  project.RecordHeader{RecordID: "record-" + id, ProjectID: "project-graph"},
			WorkPackageID: id, TaskID: "task:graph", TaskRevision: 1, GraphRevision: 1,
			Dependencies: append([]string(nil), dependencies[id]...),
		}
		definitions[id] = WorkPackageDefinition{Record: record, RecordDigest: digest}
		members = append(members, project.DependencyGraphMember{WorkPackageID: id, DefinitionRecordID: record.RecordID, DefinitionDigest: digest})
	}
	graph := Graph{
		Task: project.TaskRevision{RecordHeader: project.RecordHeader{ProjectID: "project-graph"}, TaskID: "task:graph", Revision: 1, GraphRevision: 1},
		Revision: project.DependencyGraphRevision{
			RecordHeader: project.RecordHeader{ProjectID: "project-graph"}, TaskID: "task:graph", TaskRevision: 1, Revision: 1,
			Members: members, Barriers: append([]string(nil), barriers...),
		},
		Definitions: definitions,
	}
	setDependencyDigest(t, &graph)
	return graph
}

func setDependencyDigest(t *testing.T, graph *Graph) {
	t.Helper()
	digest, err := DependencySetDigest(context.Background(), graph.Definitions)
	if err != nil {
		t.Fatal(err)
	}
	graph.Revision.DependencySetDigest = digest
}

func createdEvents(graph Graph, when time.Time) []Event {
	events := make([]Event, 0, len(graph.Revision.Members))
	for index, member := range graph.Revision.Members {
		events = append(events, Event{Record: project.WorkEvent{
			RecordHeader: project.RecordHeader{RecordID: fmt.Sprintf("create-%03d", index)}, EventType: project.EventWorkPackageCreated,
			OccurredAt: when, WorkPackageID: member.WorkPackageID,
		}, RecordDigest: strings.Repeat("a", 64)})
	}
	return events
}

func acceptedEvents(workPackageID string, when time.Time) []Event {
	events := transitionEvents(workPackageID, when, project.WorkPackagePlanned, project.WorkPackageReady, project.WorkPackageInProgress, project.WorkPackageReview)
	reviewPayload, _ := json.Marshal(project.ReviewRecordedPayload{Outcome: project.ReviewApproved, ReviewerID: "reviewer-one"})
	events = append(events, Event{Record: project.WorkEvent{
		RecordHeader: project.RecordHeader{RecordID: "review-80-" + workPackageID}, EventType: project.EventReviewRecorded,
		OccurredAt: when, WorkPackageID: workPackageID, Payload: reviewPayload,
	}, RecordDigest: strings.Repeat("b", 64)})
	events = append(events, Event{Record: project.WorkEvent{
		RecordHeader: project.RecordHeader{RecordID: "accept-90-" + workPackageID}, EventType: project.EventWorkAccepted,
		OccurredAt: when, WorkPackageID: workPackageID,
	}, RecordDigest: strings.Repeat("c", 64)})
	return events
}

func transitionEvents(workPackageID string, when time.Time, states ...project.WorkPackageState) []Event {
	events := make([]Event, 0, len(states)-1)
	for index := 0; index+1 < len(states); index++ {
		events = append(events, stateEvent(workPackageID, fmt.Sprintf("evt-%02d-%s", index+10, workPackageID), when, states[index], states[index+1]))
	}
	return events
}

func stateEvent(workPackageID, id string, when time.Time, from, to project.WorkPackageState) Event {
	payload, _ := json.Marshal(project.WorkPackageStateChangedPayload{From: from, To: to})
	return Event{Record: project.WorkEvent{
		RecordHeader: project.RecordHeader{RecordID: id}, EventType: project.EventWorkPackageStateChanged,
		OccurredAt: when, WorkPackageID: workPackageID, Payload: payload,
	}, RecordDigest: strings.Repeat("d", 64)}
}

func nodesByID(snapshot Snapshot) map[string]NodeReadiness {
	result := make(map[string]NodeReadiness, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		result[node.WorkPackageID] = node
	}
	return result
}

func sortStrings(values []string) {
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if values[right] < values[left] {
				values[left], values[right] = values[right], values[left]
			}
		}
	}
}
