package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/runtimecontract"
	"syncgate/internal/scheduler"
	"syncgate/internal/storage"
	"syncgate/internal/telemetry"
)

// localLifecycleTelemetry writes allowlisted telemetry when lifecycle evidence
// is observed. It intentionally stores references and measurements only: no
// prompt, command output, paths, or runtime event stream is persisted here.
type localLifecycleTelemetry struct {
	Contracts storage.ExecutionContractStore
	Telemetry storage.ExecutionTelemetryStore
	Control   orchestration.ControlService
	Runtime   runtimecontract.Adapter
	Now       func() time.Time
}

func (recorder localLifecycleTelemetry) RecordTerminal(ctx context.Context, event scheduler.TerminalEvent) error {
	snapshot, contract, err := recorder.authority(ctx, event.AssignmentID, event.AttemptID)
	if err != nil {
		return err
	}
	outcome := "partial"
	switch event.Outcome {
	case scheduler.OutcomeFailed:
		outcome = "failed"
	case scheduler.OutcomeCanceled:
		outcome = "canceled"
	}
	summary, err := recorder.summary(ctx, contract, snapshot, event.Observation, outcome)
	if err != nil {
		return err
	}
	return recorder.ingest(ctx, contract, summary, event.Observation.UpdatedAt, "terminal")
}

// RecordAccepted is called only after an explicit operator approval transition.
// It completes timing measures whose end is not knowable at runtime terminal.
func (recorder localLifecycleTelemetry) RecordAccepted(ctx context.Context, contract executioncontract.Contract, snapshot storage.OrchestrationSnapshot) (project.TelemetrySummaryPayload, error) {
	if snapshot.Assignment.ContractID != contract.ContractID || snapshot.Attempt.ExecutionID != contract.ExecutionID || snapshot.Attempt.State != storage.AssignmentAccepted {
		return project.TelemetrySummaryPayload{}, errors.New("accepted telemetry authority is stale")
	}
	observation := runtimecontract.Observation{SessionID: snapshot.Attempt.RuntimeSessionID, UpdatedAt: snapshot.Attempt.UpdatedAt}
	if snapshot.Resources != nil {
		observation.SessionID = snapshot.Resources.RuntimeSessionID
	}
	if recorder.Runtime != nil && observation.SessionID != "" {
		if observed, err := recorder.Runtime.Observe(ctx, observation.SessionID); err == nil {
			observation = observed
		}
	}
	summary, err := recorder.summary(ctx, contract, snapshot, observation, "succeeded")
	if err != nil {
		return project.TelemetrySummaryPayload{}, err
	}
	createdAt := snapshot.Attempt.UpdatedAt
	if createdAt.IsZero() {
		createdAt = recorder.now()
	}
	if err := recorder.ingest(ctx, contract, summary, createdAt, "accepted"); err != nil {
		return project.TelemetrySummaryPayload{}, err
	}
	return summary, nil
}

func (recorder localLifecycleTelemetry) authority(ctx context.Context, assignmentID, attemptID string) (storage.OrchestrationSnapshot, executioncontract.Contract, error) {
	snapshot, err := recorder.Control.GetAssignment(ctx, assignmentID)
	if err != nil || snapshot.Attempt.AttemptID != attemptID || snapshot.Assignment.CurrentAttemptID != attemptID {
		return storage.OrchestrationSnapshot{}, executioncontract.Contract{}, errors.New("telemetry assignment is unavailable")
	}
	record, err := recorder.Contracts.GetExecutionContract(ctx, snapshot.Assignment.ContractID, snapshot.Assignment.ContractVersion)
	if err != nil || record.Digest != snapshot.Assignment.ContractDigest {
		return storage.OrchestrationSnapshot{}, executioncontract.Contract{}, errors.New("telemetry contract is unavailable")
	}
	var contract executioncontract.Contract
	if json.Unmarshal(record.ContractJSON, &contract) != nil || executioncontract.VerifyDigest(contract) != nil || contract.Digest != record.Digest || contract.ExecutionID != snapshot.Attempt.ExecutionID {
		return storage.OrchestrationSnapshot{}, executioncontract.Contract{}, errors.New("telemetry contract is invalid")
	}
	return snapshot, contract, nil
}

func (recorder localLifecycleTelemetry) summary(ctx context.Context, contract executioncontract.Contract, snapshot storage.OrchestrationSnapshot, observation runtimecontract.Observation, outcome string) (project.TelemetrySummaryPayload, error) {
	events, err := recorder.Control.ListAudit(ctx, snapshot.Assignment.AssignmentID)
	if err != nil {
		return project.TelemetrySummaryPayload{}, err
	}
	times := lifecycleTimes(events, snapshot.Attempt.AttemptID, observation.UpdatedAt)
	observations := []project.TelemetryObservation{
		measurement("duration_milliseconds", times.active, "locally_measured"),
		measurement("queue_milliseconds", times.queue, "locally_measured"),
		measurement("active_runtime_milliseconds", times.active, "locally_measured"),
		measurement("gate_milliseconds", times.gate, "locally_measured"),
		measurement("review_milliseconds", times.review, "locally_measured"),
		measurement("human_wait_milliseconds", times.review, "locally_measured"),
		measurement("retries", nonnegative(snapshot.Attempt.AttemptNumber-1), "locally_measured"),
	}
	observations = append(observations, usageMeasurements(observation.Usage)...)
	return project.TelemetrySummaryPayload{
		Contract: registryRef(contract.ContractID, contract.Version, contract.Digest), ProjectRevision: contract.ProjectRevision,
		TaskID: contract.TaskID, TaskRecordID: contract.TaskRecordID, TaskRevision: contract.TaskRevision, TaskDigest: contract.TaskDigest,
		GraphRecordID: contract.GraphRecordID, GraphRevision: contract.GraphRevision, GraphDigest: contract.GraphDigest,
		WorkPackageID: contract.WorkPackageID, WorkPackageRecordID: contract.WorkPackageRecordID, WorkPackageDigest: contract.WorkPackageDigest,
		Trade: contract.Trade, Worker: contract.Worker, Instruction: bindingRef(contract.Instruction), ContextDigest: contract.ContextDigest,
		Runtime: bindingRef(contract.Runtime), Provider: bindingRef(contract.Provider), Model: bindingRef(contract.Model), Node: bindingRef(contract.Node),
		Observations: observations, Evidence: []project.TelemetryEvidenceReference{{ID: "evidence:" + localHash("runtime", contract.Digest, snapshot.Attempt.AttemptID, observation.SessionID)[:40], Digest: localHash("runtime", contract.Digest, snapshot.Attempt.AttemptID, string(observation.Status), observation.UpdatedAt.UTC().Format(time.RFC3339Nano)), Kind: "runtime", Source: "locally_measured"}},
		FinalOutcome: outcome,
	}, nil
}

func (recorder localLifecycleTelemetry) ingest(ctx context.Context, contract executioncontract.Contract, summary project.TelemetrySummaryPayload, createdAt time.Time, phase string) error {
	if createdAt.IsZero() {
		createdAt = recorder.now()
	}
	key := localHash("telemetry", phase, contract.Digest, summary.FinalOutcome)
	_, raw, err := telemetry.BuildEnvelope(telemetry.Envelope{TelemetryID: "telemetry:" + key[:40], IdempotencyKeyDigest: key, Summary: summary, CreatedAt: createdAt.UTC()})
	if err != nil {
		return err
	}
	if _, err := (telemetry.Service{Contracts: recorder.Contracts, Telemetry: recorder.Telemetry}).Ingest(ctx, telemetry.Request{EnvelopeJSON: raw, ExpectedProjectID: contract.ProjectID, ExpectedExecutionID: contract.ExecutionID}); err != nil {
		return err
	}
	return nil
}

type measuredLifecycleTimes struct{ queue, active, gate, review *int64 }

func lifecycleTimes(events []storage.OrchestrationAuditEvent, attemptID string, terminal time.Time) measuredLifecycleTimes {
	var planned, running, collecting, awaiting, accepted time.Time
	for _, event := range events {
		if event.AttemptID != attemptID {
			continue
		}
		switch event.ToState {
		case storage.AssignmentPlanned:
			if planned.IsZero() {
				planned = event.OccurredAt
			}
		case storage.AssignmentRunning:
			if running.IsZero() {
				running = event.OccurredAt
			}
		case storage.AssignmentCollecting:
			if collecting.IsZero() {
				collecting = event.OccurredAt
			}
		case storage.AssignmentAwaitingGates:
			if awaiting.IsZero() {
				awaiting = event.OccurredAt
			}
		case storage.AssignmentAccepted:
			if accepted.IsZero() {
				accepted = event.OccurredAt
			}
		}
	}
	return measuredLifecycleTimes{queue: elapsed(planned, running), active: elapsed(running, terminal), gate: elapsed(collecting, awaiting), review: elapsed(awaiting, accepted)}
}

func usageMeasurements(usage *runtimecontract.UsageEvidence) []project.TelemetryObservation {
	if usage == nil {
		return []project.TelemetryObservation{
			measurement("input_tokens", nil, "provider_reported"), measurement("cached_input_tokens", nil, "provider_reported"),
			measurement("output_tokens", nil, "provider_reported"), measurement("reasoning_output_tokens", nil, "provider_reported"), measurement("tool_calls", nil, "locally_measured"),
		}
	}
	return []project.TelemetryObservation{
		measurement("input_tokens", usage.InputTokens, "provider_reported"), measurement("cached_input_tokens", usage.CachedInputTokens, "provider_reported"),
		measurement("output_tokens", usage.OutputTokens, "provider_reported"), measurement("reasoning_output_tokens", usage.ReasoningOutputTokens, "provider_reported"), measurement("tool_calls", integer(usage.ToolCalls), "locally_measured"),
	}
}

func measurement(name string, value *int64, source string) project.TelemetryObservation {
	return project.TelemetryObservation{Name: name, Value: value, Source: source}
}
func integer(value int64) *int64 { return &value }
func nonnegative(value int64) *int64 {
	if value < 0 {
		value = 0
	}
	return &value
}
func elapsed(start, end time.Time) *int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil
	}
	value := end.Sub(start).Milliseconds()
	return &value
}
func registryRef(id string, version int64, digest string) project.RegistryReference {
	return project.RegistryReference{ID: id, Version: version, Digest: digest}
}
func bindingRef(value executioncontract.BindingReference) project.TelemetryBindingReference {
	return project.TelemetryBindingReference{ID: value.ID, Version: value.Version, Digest: value.Digest}
}
func (recorder localLifecycleTelemetry) now() time.Time {
	if recorder.Now != nil {
		return recorder.Now().UTC()
	}
	return time.Now().UTC()
}

var _ scheduler.LifecycleRecorder = localLifecycleTelemetry{}
