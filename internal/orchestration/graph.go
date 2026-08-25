// Package orchestration contains provider-independent deterministic control
// logic. It does not start runtimes or own portable project history.
package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"syncgate/internal/project"
)

const (
	MaxGraphNodes = project.MaxListItems
	MaxGraphEdges = 4096
)

var (
	ErrInvalidGraph          = errors.New("invalid task dependency graph")
	ErrGraphRevisionConflict = errors.New("task graph revision conflict")
)

type ReadinessState string

const (
	ReadinessPlanned  ReadinessState = "planned"
	ReadinessWaiting  ReadinessState = "waiting"
	ReadinessReady    ReadinessState = "ready"
	ReadinessBlocked  ReadinessState = "blocked"
	ReadinessReview   ReadinessState = "review"
	ReadinessAccepted ReadinessState = "accepted"
	ReadinessFailed   ReadinessState = "failed"
	ReadinessCanceled ReadinessState = "canceled"
)

const (
	ReasonWorkPackageNotCreated = "work_package_not_created"
	ReasonDependencyWaiting     = "dependency_waiting"
	ReasonDependencyFailed      = "dependency_failed"
	ReasonDependenciesSatisfied = "dependencies_satisfied"
	ReasonExecutionInProgress   = "execution_in_progress"
	ReasonCanonicalBlocked      = "canonical_blocked"
	ReasonReviewRequired        = "review_required"
	ReasonWorkAccepted          = "work_accepted"
	ReasonWorkFailed            = "work_failed"
	ReasonWorkCanceled          = "work_canceled"
	ReasonTaskHasReadyWork      = "task_has_ready_work"
	ReasonTaskWaiting           = "task_waiting"
	ReasonTaskBlocked           = "task_blocked"
	ReasonTaskInReview          = "task_in_review"
	ReasonTaskAccepted          = "task_accepted"
	ReasonTaskFailed            = "task_failed"
	ReasonTaskCanceled          = "task_canceled"
	ReasonTaskPlanned           = "task_planned"
)

type WorkPackageDefinition struct {
	Record       project.WorkPackageDefinition
	RecordDigest string
	RecordPath   string
}

type Event struct {
	Record       project.WorkEvent
	RecordDigest string
}

type Graph struct {
	Task        project.TaskRevision
	Revision    project.DependencyGraphRevision
	Definitions map[string]WorkPackageDefinition
}

type NodeReadiness struct {
	WorkPackageID   string
	CanonicalState  project.WorkPackageState
	State           ReadinessState
	ExplanationCode string
	Dependencies    []string
	Barrier         bool
}

type Snapshot struct {
	ProjectID       string
	TaskID          string
	TaskRevision    int64
	GraphRevision   int64
	State           ReadinessState
	ExplanationCode string
	Nodes           []NodeReadiness
	ParallelReady   []string
	EventWatermark  string
}

func ValidateGraph(ctx context.Context, graph Graph) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidGraph)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if graph.Task.ProjectID == "" || graph.Task.ProjectID != graph.Revision.ProjectID ||
		graph.Task.TaskID != graph.Revision.TaskID || graph.Task.Revision != graph.Revision.TaskRevision ||
		graph.Task.GraphRevision != graph.Revision.Revision {
		return fmt.Errorf("%w: task and graph identity do not match", ErrGraphRevisionConflict)
	}
	if len(graph.Revision.Members) == 0 || len(graph.Revision.Members) > MaxGraphNodes || len(graph.Definitions) != len(graph.Revision.Members) {
		return fmt.Errorf("%w: graph must contain between 1 and %d exact members", ErrInvalidGraph, MaxGraphNodes)
	}

	members := make(map[string]struct{}, len(graph.Revision.Members))
	for _, member := range graph.Revision.Members {
		if err := ctx.Err(); err != nil {
			return err
		}
		definition, exists := graph.Definitions[member.WorkPackageID]
		if !exists {
			return fmt.Errorf("%w: missing work package %s", ErrInvalidGraph, member.WorkPackageID)
		}
		record := definition.Record
		if record.ProjectID != graph.Task.ProjectID || record.TaskID != graph.Task.TaskID ||
			record.TaskRevision != graph.Task.Revision || record.GraphRevision != graph.Revision.Revision {
			return fmt.Errorf("%w: cross-project or mismatched work package %s", ErrInvalidGraph, member.WorkPackageID)
		}
		if record.RecordID != member.DefinitionRecordID || definition.RecordDigest != member.DefinitionDigest {
			return fmt.Errorf("%w: work package definition identity conflict for %s", ErrGraphRevisionConflict, member.WorkPackageID)
		}
		if _, duplicate := members[member.WorkPackageID]; duplicate {
			return fmt.Errorf("%w: duplicate member %s", ErrInvalidGraph, member.WorkPackageID)
		}
		members[member.WorkPackageID] = struct{}{}
	}

	edges := 0
	indegree := make(map[string]int, len(members))
	dependents := make(map[string][]string, len(members))
	for _, member := range graph.Revision.Members {
		definition := graph.Definitions[member.WorkPackageID].Record
		seen := make(map[string]struct{}, len(definition.Dependencies))
		for _, dependency := range definition.Dependencies {
			if err := ctx.Err(); err != nil {
				return err
			}
			edges++
			if edges > MaxGraphEdges {
				return fmt.Errorf("%w: graph exceeds %d edges", ErrInvalidGraph, MaxGraphEdges)
			}
			if dependency == member.WorkPackageID {
				return fmt.Errorf("%w: self dependency for %s", ErrInvalidGraph, member.WorkPackageID)
			}
			if _, duplicate := seen[dependency]; duplicate {
				return fmt.Errorf("%w: duplicate edge %s -> %s", ErrInvalidGraph, member.WorkPackageID, dependency)
			}
			seen[dependency] = struct{}{}
			if _, exists := members[dependency]; !exists {
				return fmt.Errorf("%w: missing dependency %s for %s", ErrInvalidGraph, dependency, member.WorkPackageID)
			}
			indegree[member.WorkPackageID]++
			dependents[dependency] = append(dependents[dependency], member.WorkPackageID)
		}
	}
	digest, err := DependencySetDigest(ctx, graph.Definitions)
	if err != nil {
		return err
	}
	if digest != graph.Revision.DependencySetDigest {
		return fmt.Errorf("%w: dependency set digest conflict", ErrGraphRevisionConflict)
	}

	ready := make([]string, 0, len(members))
	for id := range members {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	visited := 0
	for len(ready) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := ready[0]
		ready = ready[1:]
		visited++
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
		sort.Strings(ready)
	}
	if visited != len(members) {
		return fmt.Errorf("%w: dependency cycle", ErrInvalidGraph)
	}
	return nil
}

func DependencySetDigest(ctx context.Context, definitions map[string]WorkPackageDefinition) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("%w: context is required", ErrInvalidGraph)
	}
	ids := make([]string, 0, len(definitions))
	for id := range definitions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	hasher := sha256.New()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_, _ = io.WriteString(hasher, id)
		_, _ = hasher.Write([]byte{0})
		dependencies := append([]string(nil), definitions[id].Record.Dependencies...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			_, _ = io.WriteString(hasher, dependency)
			_, _ = hasher.Write([]byte{0})
		}
		_, _ = hasher.Write([]byte{0xff})
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func ReduceReadiness(ctx context.Context, graph Graph, events []Event) (Snapshot, error) {
	if err := ValidateGraph(ctx, graph); err != nil {
		return Snapshot{}, err
	}
	events = append([]Event(nil), events...)
	sort.Slice(events, func(i, j int) bool {
		left, right := events[i].Record, events[j].Record
		if !left.OccurredAt.Equal(right.OccurredAt) {
			return left.OccurredAt.Before(right.OccurredAt)
		}
		if eventPrecedence(left.EventType) != eventPrecedence(right.EventType) {
			return eventPrecedence(left.EventType) < eventPrecedence(right.EventType)
		}
		return left.RecordID < right.RecordID
	})
	states := make(map[string]project.WorkPackageState, len(graph.Definitions))
	approved := make(map[string]bool, len(graph.Definitions))
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, graph.Revision.DependencySetDigest)
	_, _ = hasher.Write([]byte{0})
	for _, item := range events {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		event := item.Record
		if _, exists := graph.Definitions[event.WorkPackageID]; !exists && event.WorkPackageID != "" {
			continue
		}
		_, _ = io.WriteString(hasher, event.RecordID)
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, item.RecordDigest)
		_, _ = hasher.Write([]byte{0xff})
		applyEvent(states, approved, event)
	}

	barriers := make(map[string]bool, len(graph.Revision.Barriers))
	for _, id := range graph.Revision.Barriers {
		barriers[id] = true
	}
	nodesByID := make(map[string]NodeReadiness, len(graph.Revision.Members))
	remaining := make(map[string]bool, len(graph.Revision.Members))
	for _, member := range graph.Revision.Members {
		remaining[member.WorkPackageID] = true
	}
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		progress := false
		ids := make([]string, 0, len(remaining))
		for id := range remaining {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			definition := graph.Definitions[id].Record
			dependenciesReady := true
			for _, dependency := range definition.Dependencies {
				if _, done := nodesByID[dependency]; !done {
					dependenciesReady = false
					break
				}
			}
			if !dependenciesReady {
				continue
			}
			node := reduceNode(id, definition.Dependencies, states[id], nodesByID)
			node.Barrier = barriers[id]
			nodesByID[id] = node
			delete(remaining, id)
			progress = true
		}
		if !progress {
			return Snapshot{}, fmt.Errorf("%w: traversal could not resolve graph", ErrInvalidGraph)
		}
	}

	snapshot := Snapshot{
		ProjectID: graph.Task.ProjectID, TaskID: graph.Task.TaskID, TaskRevision: graph.Task.Revision,
		GraphRevision: graph.Revision.Revision, EventWatermark: hex.EncodeToString(hasher.Sum(nil)),
	}
	for _, member := range graph.Revision.Members {
		node := nodesByID[member.WorkPackageID]
		snapshot.Nodes = append(snapshot.Nodes, node)
		if node.State == ReadinessReady {
			snapshot.ParallelReady = append(snapshot.ParallelReady, node.WorkPackageID)
		}
	}
	sort.Strings(snapshot.ParallelReady)
	snapshot.State, snapshot.ExplanationCode = summarizeTask(snapshot.Nodes)
	return snapshot, nil
}

func applyEvent(states map[string]project.WorkPackageState, approved map[string]bool, event project.WorkEvent) {
	switch event.EventType {
	case project.EventWorkPackageCreated:
		if _, exists := states[event.WorkPackageID]; !exists {
			states[event.WorkPackageID] = project.WorkPackagePlanned
		}
	case project.EventWorkPackageStateChanged:
		var payload project.WorkPackageStateChangedPayload
		if json.Unmarshal(event.Payload, &payload) == nil && states[event.WorkPackageID] == payload.From && project.ValidWorkPackageTransition(payload.From, payload.To) {
			states[event.WorkPackageID] = payload.To
			if payload.To != project.WorkPackageReview {
				approved[event.WorkPackageID] = false
			}
		}
	case project.EventReviewRecorded:
		var payload project.ReviewRecordedPayload
		if json.Unmarshal(event.Payload, &payload) == nil && states[event.WorkPackageID] == project.WorkPackageReview {
			approved[event.WorkPackageID] = payload.Outcome == project.ReviewApproved
		}
	case project.EventWorkAccepted:
		if states[event.WorkPackageID] == project.WorkPackageReview && approved[event.WorkPackageID] {
			states[event.WorkPackageID] = project.WorkPackageAccepted
		}
	}
}

func eventPrecedence(eventType project.EventType) int {
	switch eventType {
	case project.EventWorkPackageCreated:
		return 0
	case project.EventWorkPackageStateChanged:
		return 1
	case project.EventReviewRecorded:
		return 2
	case project.EventWorkAccepted:
		return 3
	default:
		return 4
	}
}

func reduceNode(id string, dependencies []string, state project.WorkPackageState, nodes map[string]NodeReadiness) NodeReadiness {
	node := NodeReadiness{WorkPackageID: id, CanonicalState: state, Dependencies: append([]string(nil), dependencies...)}
	switch state {
	case "":
		node.State, node.ExplanationCode = ReadinessPlanned, ReasonWorkPackageNotCreated
		return node
	case project.WorkPackageInProgress:
		node.State, node.ExplanationCode = ReadinessWaiting, ReasonExecutionInProgress
		return node
	case project.WorkPackageBlocked:
		node.State, node.ExplanationCode = ReadinessBlocked, ReasonCanonicalBlocked
		return node
	case project.WorkPackageReview:
		node.State, node.ExplanationCode = ReadinessReview, ReasonReviewRequired
		return node
	case project.WorkPackageAccepted:
		node.State, node.ExplanationCode = ReadinessAccepted, ReasonWorkAccepted
		return node
	case project.WorkPackageFailed:
		node.State, node.ExplanationCode = ReadinessFailed, ReasonWorkFailed
		return node
	case project.WorkPackageCanceled:
		node.State, node.ExplanationCode = ReadinessCanceled, ReasonWorkCanceled
		return node
	}
	for _, dependency := range dependencies {
		dependencyState := nodes[dependency].State
		if dependencyState == ReadinessFailed || dependencyState == ReadinessCanceled || dependencyState == ReadinessBlocked {
			node.State, node.ExplanationCode = ReadinessBlocked, ReasonDependencyFailed
			return node
		}
		if dependencyState != ReadinessAccepted {
			node.State, node.ExplanationCode = ReadinessWaiting, ReasonDependencyWaiting
			return node
		}
	}
	node.State, node.ExplanationCode = ReadinessReady, ReasonDependenciesSatisfied
	return node
}

func summarizeTask(nodes []NodeReadiness) (ReadinessState, string) {
	counts := make(map[ReadinessState]int)
	for _, node := range nodes {
		counts[node.State]++
	}
	if counts[ReadinessAccepted] == len(nodes) {
		return ReadinessAccepted, ReasonTaskAccepted
	}
	if counts[ReadinessCanceled] == len(nodes) {
		return ReadinessCanceled, ReasonTaskCanceled
	}
	for _, candidate := range []struct {
		state  ReadinessState
		reason string
	}{
		{ReadinessFailed, ReasonTaskFailed}, {ReadinessBlocked, ReasonTaskBlocked},
		{ReadinessReview, ReasonTaskInReview}, {ReadinessReady, ReasonTaskHasReadyWork},
		{ReadinessWaiting, ReasonTaskWaiting}, {ReadinessPlanned, ReasonTaskPlanned},
	} {
		if counts[candidate.state] > 0 {
			return candidate.state, candidate.reason
		}
	}
	return ReadinessPlanned, ReasonTaskPlanned
}

func NormalizeTaskInputs(risk []project.RiskDimension, resources *project.ResourceConstraints, gates []project.QualityGateReference, barriers []string) ([]project.RiskDimension, *project.ResourceConstraints, []project.QualityGateReference, []string) {
	risk = append([]project.RiskDimension(nil), risk...)
	sort.Slice(risk, func(i, j int) bool { return risk[i].Name < risk[j].Name })
	if resources != nil {
		copy := *resources
		copy.RequiredCapabilities = sortedStrings(copy.RequiredCapabilities)
		copy.RequiredTools = sortedStrings(copy.RequiredTools)
		copy.AllowedOS = sortedStrings(copy.AllowedOS)
		copy.AllowedArchitectures = sortedStrings(copy.AllowedArchitectures)
		resources = &copy
	}
	gates = append([]project.QualityGateReference(nil), gates...)
	sort.Slice(gates, func(i, j int) bool {
		if gates[i].GateID == gates[j].GateID {
			return gates[i].Version < gates[j].Version
		}
		return gates[i].GateID < gates[j].GateID
	})
	return risk, resources, gates, sortedStrings(barriers)
}

func sortedStrings(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	return values
}
