// Package insights derives deterministic, versioned work summaries from the
// accepted local history projection. Insights are not portable project truth.
package insights

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"syncgate/internal/project"
	"syncgate/internal/storage"
)

const (
	ScopeProject      = "project"
	DefinitionVersion = 1
)

type Definition struct {
	Name    string
	Version int
}

var Definitions = []Definition{
	{"work_package_state_counts", DefinitionVersion}, {"completion_rate", DefinitionVersion},
	{"first_pass_acceptance_rate", DefinitionVersion}, {"execution_duration", DefinitionVersion},
	{"creation_to_acceptance_duration", DefinitionVersion}, {"test_outcomes", DefinitionVersion},
	{"retry_and_rework", DefinitionVersion}, {"work_by_producer", DefinitionVersion},
	{"artifact_and_handoff_counts", DefinitionVersion}, {"projection_freshness", DefinitionVersion},
}

type Calculator struct {
	Store storage.Store
	Now   func() time.Time
}

func (calculator *Calculator) Rebuild(ctx context.Context, projectID string) ([]storage.ProjectInsightProjection, error) {
	if calculator == nil || calculator.Store == nil {
		return nil, errors.New("work insight store is required")
	}
	events, err := listEvents(ctx, calculator.Store, projectID)
	if err != nil {
		return nil, err
	}
	results, err := calculate(projectID, events, calculator.now())
	if err != nil {
		return nil, err
	}
	err = calculator.Store.ProjectInsightProjections().ReplaceProjectInsights(ctx, projectID, func(writer storage.ProjectInsightWriter) error {
		for _, result := range results {
			if err := writer.SaveProjectInsight(ctx, result); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("replace work insights: %w", err)
	}
	return results, nil
}

func (calculator *Calculator) now() time.Time {
	if calculator.Now != nil {
		return calculator.Now().UTC()
	}
	return time.Now().UTC()
}

func listEvents(ctx context.Context, store storage.Store, projectID string) ([]storage.ProjectEventProjection, error) {
	query := storage.ProjectEventQuery{ProjectID: projectID, Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit}}
	events := []storage.ProjectEventProjection{}
	for {
		page, err := store.ProjectEvents().ListProjectEvents(ctx, query)
		if err != nil {
			return nil, err
		}
		events = append(events, page.Items...)
		if page.NextCursor == nil {
			return events, nil
		}
		query.Page.Cursor = *page.NextCursor
	}
}

type packageState struct {
	state             project.WorkPackageState
	created, accepted time.Time
	retries, rework   int64
}
type executionState struct {
	started, ended time.Time
	terminal       bool
}

func calculate(projectID string, all []storage.ProjectEventProjection, calculatedAt time.Time) ([]storage.ProjectInsightProjection, error) {
	accepted := make([]storage.ProjectEventProjection, 0, len(all))
	pending := 0
	for _, event := range all {
		if event.Status == "accepted" {
			accepted = append(accepted, event)
		} else {
			pending++
		}
	}
	packages := map[string]*packageState{}
	executions := map[string]*executionState{}
	testsPassed, testsFailed, testsSkipped := int64(0), int64(0), int64(0)
	artifacts, handoffs := int64(0), int64(0)
	groups := map[string]map[string]int64{"worker": {}, "model": {}, "provider": {}, "device": {}}
	for _, event := range accepted {
		addGroup(groups["worker"], event.ProducerWorkerID)
		addGroup(groups["model"], event.ProducerModel)
		addGroup(groups["provider"], event.ProducerProvider)
		addGroup(groups["device"], string(event.ProducerDeviceID))
		if event.WorkPackageID != "" && packages[event.WorkPackageID] == nil {
			packages[event.WorkPackageID] = &packageState{}
		}
		switch project.EventType(event.EventType) {
		case project.EventWorkPackageCreated:
			packages[event.WorkPackageID].state = project.WorkPackagePlanned
			packages[event.WorkPackageID].created = event.OccurredAt
		case project.EventWorkPackageStateChanged:
			var payload project.WorkPackageStateChangedPayload
			if json.Unmarshal(event.PayloadJSON, &payload) == nil {
				state := packages[event.WorkPackageID]
				state.state = payload.To
				if payload.From == project.WorkPackageFailed && payload.To == project.WorkPackageReady {
					state.retries++
				}
				if payload.From == project.WorkPackageReview && payload.To == project.WorkPackageInProgress {
					state.rework++
				}
			}
		case project.EventWorkAccepted:
			packages[event.WorkPackageID].state = project.WorkPackageAccepted
			packages[event.WorkPackageID].accepted = event.OccurredAt
		case project.EventExecutionStarted:
			if executions[event.ExecutionID] == nil {
				executions[event.ExecutionID] = &executionState{started: event.OccurredAt}
			} else {
				executions[event.ExecutionID].started = event.OccurredAt
			}
		case project.EventExecutionCompleted, project.EventExecutionFailed:
			if executions[event.ExecutionID] == nil {
				executions[event.ExecutionID] = &executionState{}
			}
			executions[event.ExecutionID].ended = event.OccurredAt
			executions[event.ExecutionID].terminal = true
		case project.EventTestRecorded:
			var payload project.TestRecordedPayload
			if json.Unmarshal(event.PayloadJSON, &payload) == nil {
				switch payload.Outcome {
				case project.TestPassed:
					testsPassed++
				case project.TestFailed:
					testsFailed++
				case project.TestSkipped:
					testsSkipped++
				}
			}
		case project.EventArtifactRecorded:
			artifacts++
		case project.EventHandoffCreated:
			handoffs++
		}
	}
	watermark := eventWatermark(accepted)
	completeness, evidence := quality(len(accepted), pending)
	insights := []storage.ProjectInsightProjection{}
	appendValue := func(name string, value any, sample int64, partial bool) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		c, e := completeness, evidence
		if partial && c == "complete" {
			c, e = "partial", "weak"
		}
		insights = append(insights, storage.ProjectInsightProjection{ProjectID: projectID, Scope: ScopeProject, MetricName: name, DefinitionVersion: DefinitionVersion, SourceEventWatermark: watermark, ValueJSON: raw, SampleCount: sample, Completeness: c, Evidence: e, CalculatedAt: calculatedAt})
		return nil
	}
	states := map[string]int64{"active": 0, "blocked": 0, "failed": 0, "accepted": 0, "completed": 0}
	acceptedPackages, firstPass := int64(0), int64(0)
	retries, rework := int64(0), int64(0)
	acceptanceDurations := []int64{}
	for _, state := range packages {
		switch state.state {
		case project.WorkPackageInProgress:
			states["active"]++
		case project.WorkPackageBlocked:
			states["blocked"]++
		case project.WorkPackageFailed:
			states["failed"]++
		case project.WorkPackageAccepted:
			states["accepted"]++
			acceptedPackages++
			if state.rework == 0 {
				firstPass++
			}
		}
		retries += state.retries
		rework += state.rework
		if !state.created.IsZero() && !state.accepted.IsZero() {
			acceptanceDurations = append(acceptanceDurations, state.accepted.Sub(state.created).Milliseconds())
		}
	}
	for _, state := range executions {
		if state.terminal {
			states["completed"]++
		}
	}
	if err := appendValue("work_package_state_counts", states, int64(len(packages)), false); err != nil {
		return nil, err
	}
	terminal := states["accepted"] + states["failed"]
	if err := appendValue("completion_rate", ratio(states["accepted"], terminal), terminal, terminal == 0); err != nil {
		return nil, err
	}
	if err := appendValue("first_pass_acceptance_rate", ratio(firstPass, acceptedPackages), acceptedPackages, acceptedPackages == 0); err != nil {
		return nil, err
	}
	executionDurations := []int64{}
	missingDuration := false
	for _, state := range executions {
		if state.terminal && !state.started.IsZero() {
			executionDurations = append(executionDurations, state.ended.Sub(state.started).Milliseconds())
		} else if state.terminal {
			missingDuration = true
		}
	}
	if err := appendValue("execution_duration", durationValue(executionDurations), int64(len(executionDurations)), missingDuration); err != nil {
		return nil, err
	}
	if err := appendValue("creation_to_acceptance_duration", durationValue(acceptanceDurations), int64(len(acceptanceDurations)), acceptedPackages != int64(len(acceptanceDurations))); err != nil {
		return nil, err
	}
	if err := appendValue("test_outcomes", map[string]int64{"passed": testsPassed, "failed": testsFailed, "skipped": testsSkipped}, testsPassed+testsFailed+testsSkipped, false); err != nil {
		return nil, err
	}
	if err := appendValue("retry_and_rework", map[string]int64{"retries": retries, "rework": rework}, int64(len(packages)), false); err != nil {
		return nil, err
	}
	if err := appendValue("work_by_producer", groups, int64(len(accepted)), false); err != nil {
		return nil, err
	}
	if err := appendValue("artifact_and_handoff_counts", map[string]int64{"artifacts": artifacts, "handoffs": handoffs}, artifacts+handoffs, false); err != nil {
		return nil, err
	}
	latest := time.Time{}
	for _, event := range accepted {
		if event.OccurredAt.After(latest) {
			latest = event.OccurredAt
		}
	}
	if err := appendValue("projection_freshness", map[string]any{"accepted_event_count": len(accepted), "pending_event_count": pending, "latest_accepted_at": latest}, int64(len(accepted)), pending > 0); err != nil {
		return nil, err
	}
	return insights, nil
}

func addGroup(group map[string]int64, value string) {
	if value != "" {
		group[value]++
	}
}
func ratio(n, d int64) map[string]any {
	if d == 0 {
		return map[string]any{"known": false}
	}
	return map[string]any{"known": true, "numerator": n, "denominator": d, "rate": float64(n) / float64(d)}
}
func durationValue(values []int64) map[string]any {
	if len(values) == 0 {
		return map[string]any{"known": false}
	}
	var sum int64
	for _, value := range values {
		sum += value
	}
	return map[string]any{"known": true, "count": len(values), "total_milliseconds": sum, "average_milliseconds": sum / int64(len(values))}
}
func quality(accepted, pending int) (string, string) {
	if accepted == 0 {
		return "insufficient", "none"
	}
	if accepted < 3 || pending > 0 {
		return "partial", "weak"
	}
	return "complete", "strong"
}
func eventWatermark(events []storage.ProjectEventProjection) string {
	sort.Slice(events, func(i, j int) bool { return events[i].EventID < events[j].EventID })
	hash := sha256.New()
	for _, event := range events {
		_, _ = hash.Write([]byte(event.EventID + "\x00" + event.RecordHash + "\x00" + event.OccurredAt.UTC().Format(time.RFC3339Nano) + "\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
