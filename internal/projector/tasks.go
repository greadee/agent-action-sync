package projector

import (
	"context"
	"fmt"
	"sort"

	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/storage"
)

type taskAggregate struct {
	task            *candidateRecord
	graph           *candidateRecord
	definitions     map[string]orchestration.WorkPackageDefinition
	definitionPaths map[string]string
}

func prepareTaskAggregates(ctx context.Context, candidates []*candidateRecord) ([]taskAggregate, error) {
	tasks := make(map[string]*candidateRecord)
	graphs := make(map[string]*candidateRecord)
	workPackages := make(map[string]*candidateRecord)
	for _, candidate := range candidates {
		if !candidate.valid {
			continue
		}
		switch record := candidate.decoded.Value.(type) {
		case *project.TaskRevision:
			tasks[taskKey(record.TaskID, record.Revision)] = candidate
		case *project.DependencyGraphRevision:
			graphs[graphKey(record.TaskID, record.TaskRevision, record.Revision)] = candidate
		case *project.WorkPackageDefinition:
			workPackages[record.WorkPackageID] = candidate
		}
	}

	result := make([]taskAggregate, 0, len(tasks))
	usedGraphs := make(map[*candidateRecord]bool)
	for _, taskCandidate := range tasks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		task := taskCandidate.decoded.Value.(*project.TaskRevision)
		graphCandidate := graphs[graphKey(task.TaskID, task.Revision, task.GraphRevision)]
		if graphCandidate == nil {
			invalidateTaskCandidate(taskCandidate, "missing_task_graph")
			continue
		}
		usedGraphs[graphCandidate] = true
		graph := graphCandidate.decoded.Value.(*project.DependencyGraphRevision)
		definitions := make(map[string]orchestration.WorkPackageDefinition, len(graph.Members))
		paths := make(map[string]string, len(graph.Members))
		complete := true
		for _, member := range graph.Members {
			candidate := workPackages[member.WorkPackageID]
			if candidate == nil {
				complete = false
				break
			}
			record := candidate.decoded.Value.(*project.WorkPackageDefinition)
			definitions[member.WorkPackageID] = orchestration.WorkPackageDefinition{
				Record: *record, RecordDigest: candidate.decoded.Digest, RecordPath: candidate.relativePath,
			}
			paths[member.WorkPackageID] = candidate.relativePath
		}
		if !complete {
			invalidateTaskCandidate(taskCandidate, "invalid_task_graph")
			invalidateTaskCandidate(graphCandidate, "invalid_task_graph")
			continue
		}
		aggregate := taskAggregate{task: taskCandidate, graph: graphCandidate, definitions: definitions, definitionPaths: paths}
		if err := orchestration.ValidateGraph(ctx, aggregate.graphValue()); err != nil {
			invalidateTaskCandidate(taskCandidate, "invalid_task_graph")
			invalidateTaskCandidate(graphCandidate, "invalid_task_graph")
			continue
		}
		result = append(result, aggregate)
	}
	for _, graphCandidate := range graphs {
		if !usedGraphs[graphCandidate] {
			invalidateTaskCandidate(graphCandidate, "missing_task_revision")
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i].task.decoded.Value.(*project.TaskRevision)
		right := result[j].task.decoded.Value.(*project.TaskRevision)
		if left.TaskID == right.TaskID {
			return left.Revision < right.Revision
		}
		return left.TaskID < right.TaskID
	})
	return result, nil
}

func (aggregate taskAggregate) graphValue() orchestration.Graph {
	return orchestration.Graph{
		Task:        *aggregate.task.decoded.Value.(*project.TaskRevision),
		Revision:    *aggregate.graph.decoded.Value.(*project.DependencyGraphRevision),
		Definitions: aggregate.definitions,
	}
}

func (aggregate taskAggregate) reduce(ctx context.Context, candidates []*candidateRecord, state projectionState) (orchestration.Snapshot, error) {
	events := make([]orchestration.Event, 0)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return orchestration.Snapshot{}, err
		}
		if !candidate.valid {
			continue
		}
		event, ok := candidate.decoded.Value.(*project.WorkEvent)
		if !ok || eventProjectionStatus(*event, state) != "accepted" {
			continue
		}
		if _, member := aggregate.definitions[event.WorkPackageID]; member {
			events = append(events, orchestration.Event{Record: *event, RecordDigest: candidate.decoded.Digest})
		}
	}
	return orchestration.ReduceReadiness(ctx, aggregate.graphValue(), events)
}

func (aggregate taskAggregate) project(snapshot orchestration.Snapshot) (storage.ProjectTaskProjection, []storage.ProjectTaskNodeProjection) {
	task := aggregate.task.decoded.Value.(*project.TaskRevision)
	graph := aggregate.graph.decoded.Value.(*project.DependencyGraphRevision)
	projection := storage.ProjectTaskProjection{
		ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: task.Revision, GraphRevision: graph.Revision,
		TaskRecordID: task.RecordID, TaskRecordHash: aggregate.task.decoded.Digest, TaskRecordPath: aggregate.task.relativePath,
		GraphRecordID: graph.RecordID, GraphRecordHash: aggregate.graph.decoded.Digest, GraphRecordPath: aggregate.graph.relativePath,
		Objective: task.Objective, Priority: string(task.Priority), State: string(snapshot.State),
		ExplanationCode: snapshot.ExplanationCode, EventWatermark: snapshot.EventWatermark,
		ParallelReady: append([]string(nil), snapshot.ParallelReady...), CreatedAt: task.CreatedAt,
	}
	nodes := make([]storage.ProjectTaskNodeProjection, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		definition := aggregate.definitions[node.WorkPackageID]
		nodes = append(nodes, storage.ProjectTaskNodeProjection{
			ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: task.Revision, GraphRevision: graph.Revision,
			WorkPackageID: node.WorkPackageID, DefinitionRecordID: definition.Record.RecordID,
			DefinitionHash: definition.RecordDigest, DefinitionPath: aggregate.definitionPaths[node.WorkPackageID],
			CanonicalState: string(node.CanonicalState), Readiness: string(node.State), ExplanationCode: node.ExplanationCode,
			Dependencies: append([]string(nil), node.Dependencies...), Barrier: node.Barrier,
		})
	}
	return projection, nodes
}

func invalidateTaskCandidate(candidate *candidateRecord, reason string) {
	if candidate == nil {
		return
	}
	candidate.valid = false
	candidate.reasonCode = reason
}

func taskKey(taskID string, revision int64) string {
	return fmt.Sprintf("%s\x00%d", taskID, revision)
}

func graphKey(taskID string, taskRevision, graphRevision int64) string {
	return fmt.Sprintf("%s\x00%d\x00%d", taskID, taskRevision, graphRevision)
}
