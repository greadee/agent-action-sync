package workhistory

import (
	"context"
	"fmt"
	"strings"

	"syncgate/internal/project"
)

func (service *Service) CreateWorkPackage(ctx context.Context, request CreateWorkPackageRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.Objective, request.Trade) || len(request.Deliverables) == 0 || len(request.AcceptanceCriteria) == 0 {
			return operationSpec{}, ErrInvalidRequest
		}
		if err := validatePaths(append(append(append([]string{}, request.Scope.Allowed...), request.Scope.Inspect...), request.Scope.Forbidden...)); err != nil {
			return operationSpec{}, err
		}
		definitionID := deterministicID("wp-", manifest.ProjectID, "create-work-package-definition", request.IdempotencyKey)
		definition := project.WorkPackageDefinition{
			RecordHeader:  project.NewRecordHeader(project.RecordWorkPackage, definitionID, manifest.ProjectID),
			WorkPackageID: request.WorkPackageID, Objective: redactText(request.Objective), Trade: redactText(request.Trade),
			Specialization: redactText(request.Specialization), Scope: request.Scope, Dependencies: append([]string(nil), request.Dependencies...),
			Deliverables: redactList(request.Deliverables), AcceptanceCriteria: redactList(request.AcceptanceCriteria),
			ReviewRequired: request.ReviewRequired, CreatedAt: request.OccurredAt,
			Provenance: provenance(request.Metadata, request.WorkPackageID, "", nil),
		}
		created, err := event(manifest, request.Metadata, "create-work-package-event", project.EventWorkPackageCreated, request.WorkPackageID, "", project.WorkPackageCreatedPayload{DefinitionRecordID: definitionID})
		if err != nil {
			return operationSpec{}, err
		}
		return operationSpec{records: []any{definition, created}, validateState: func(state historyState) error {
			if _, exists := state.workPackages[request.WorkPackageID]; exists {
				return project.ErrRecordConflict
			}
			return nil
		}}, nil
	})
}

func (service *Service) TransitionWorkPackage(ctx context.Context, request TransitionWorkPackageRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID) || !allowedWorkTransition(request.From, request.To) {
			return operationSpec{}, ErrInvalidTransition
		}
		changed, err := event(manifest, request.Metadata, "transition-work-package", project.EventWorkPackageStateChanged, request.WorkPackageID, "", project.WorkPackageStateChangedPayload{From: request.From, To: request.To, ReasonCode: request.ReasonCode})
		return operationSpec{records: []any{changed}, validateState: func(state historyState) error {
			actual, exists := state.workPackages[request.WorkPackageID]
			return requireState(actual, exists, request.From)
		}}, err
	})
}

func (service *Service) StartExecution(ctx context.Context, request StartExecutionRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.ExecutionID) {
			return operationSpec{}, ErrInvalidRequest
		}
		manifestID := deterministicID("exe-", manifest.ProjectID, "start-execution-manifest", request.IdempotencyKey)
		execution := project.ExecutionManifest{
			RecordHeader: project.NewRecordHeader(project.RecordExecution, manifestID, manifest.ProjectID),
			ExecutionID:  request.ExecutionID, WorkPackageID: request.WorkPackageID, State: project.ExecutionRunning,
			Producer: sanitizeProducer(request.Producer), CreatedAt: request.OccurredAt,
			Provenance: provenance(request.Metadata, request.WorkPackageID, request.ExecutionID, nil),
		}
		started, err := event(manifest, request.Metadata, "start-execution-event", project.EventExecutionStarted, request.WorkPackageID, request.ExecutionID, project.ExecutionStartedPayload{ManifestRecordID: manifestID})
		return operationSpec{records: []any{execution, started}, validateState: func(state historyState) error {
			if err := requireState(state.workPackages[request.WorkPackageID], state.workPackages[request.WorkPackageID] != "", project.WorkPackageInProgress); err != nil {
				return err
			}
			if _, exists := state.executions[request.ExecutionID]; exists {
				return project.ErrRecordConflict
			}
			return nil
		}}, err
	})
}

func (service *Service) ResumeExecution(ctx context.Context, request ExecutionRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, layout project.Layout) (operationSpec, error) {
		manifestPath, err := project.ExecutionManifestRelativePath(request.ExecutionID)
		if err != nil {
			return operationSpec{}, err
		}
		decoded, err := project.ReadPortableRecord(layout, manifestPath)
		if err != nil {
			return operationSpec{}, err
		}
		execution, ok := decoded.Value.(*project.ExecutionManifest)
		if !ok || execution.WorkPackageID != request.WorkPackageID {
			return operationSpec{}, project.ErrRecordConflict
		}
		record, err := event(manifest, request.Metadata, "resume-execution", project.EventExecutionStarted, request.WorkPackageID, request.ExecutionID, project.ExecutionStartedPayload{ManifestRecordID: execution.RecordID})
		return operationSpec{records: []any{record}, validateState: func(state historyState) error {
			return requireExecution(state.executions[request.ExecutionID], state.executions[request.ExecutionID] != "", project.ExecutionPaused)
		}}, err
	})
}

func (service *Service) PauseExecution(ctx context.Context, request PauseExecutionRequest) (OperationResult, error) {
	return service.executionEvent(ctx, request.ExecutionRequest, "pause-execution", project.EventExecutionPaused, project.ExecutionPausedPayload{ReasonCode: request.ReasonCode}, []project.ExecutionState{project.ExecutionRunning})
}

func (service *Service) FailExecution(ctx context.Context, request FailExecutionRequest) (OperationResult, error) {
	return service.executionEvent(ctx, request.ExecutionRequest, "fail-execution", project.EventExecutionFailed, project.ExecutionFailedPayload{FailureCode: request.FailureCode, Summary: redactText(request.Summary)}, []project.ExecutionState{project.ExecutionRunning, project.ExecutionPaused})
}

func (service *Service) CompleteExecution(ctx context.Context, request CompleteExecutionRequest) (OperationResult, error) {
	return service.executionEvent(ctx, request.ExecutionRequest, "complete-execution", project.EventExecutionCompleted, project.ExecutionCompletedPayload{Summary: redactText(request.Summary)}, []project.ExecutionState{project.ExecutionRunning})
}

func (service *Service) executionEvent(ctx context.Context, request ExecutionRequest, operation string, eventType project.EventType, value any, from []project.ExecutionState) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.ExecutionID) {
			return operationSpec{}, ErrInvalidRequest
		}
		if eventType == project.EventExecutionPaused {
			payload := value.(project.ExecutionPausedPayload)
			if strings.TrimSpace(payload.ReasonCode) == "" {
				return operationSpec{}, ErrInvalidRequest
			}
		}
		if eventType == project.EventExecutionFailed {
			payload := value.(project.ExecutionFailedPayload)
			if strings.TrimSpace(payload.FailureCode) == "" {
				return operationSpec{}, ErrInvalidRequest
			}
		}
		record, err := event(manifest, request.Metadata, operation, eventType, request.WorkPackageID, request.ExecutionID, value)
		return operationSpec{records: []any{record}, validateState: func(state historyState) error {
			return requireExecution(state.executions[request.ExecutionID], state.executions[request.ExecutionID] != "", from...)
		}}, err
	})
}

func (service *Service) RecordTest(ctx context.Context, request RecordTestRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.ExecutionID, request.Name) || request.DurationMilliseconds < 0 {
			return operationSpec{}, ErrInvalidRequest
		}
		recorded, err := event(manifest, request.Metadata, "record-test", project.EventTestRecorded, request.WorkPackageID, request.ExecutionID, project.TestRecordedPayload{Name: redactText(request.Name), Outcome: request.Outcome, DurationMilliseconds: request.DurationMilliseconds})
		return operationSpec{records: []any{recorded}, validateState: func(state historyState) error {
			return requireExecution(state.executions[request.ExecutionID], state.executions[request.ExecutionID] != "", project.ExecutionRunning)
		}}, err
	})
}

func (service *Service) RecordReview(ctx context.Context, request RecordReviewRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.ReviewerID) {
			return operationSpec{}, ErrInvalidRequest
		}
		recorded, err := event(manifest, request.Metadata, "record-review", project.EventReviewRecorded, request.WorkPackageID, request.ExecutionID, project.ReviewRecordedPayload{Outcome: request.Outcome, ReviewerID: request.ReviewerID, Summary: redactText(request.Summary)})
		return operationSpec{records: []any{recorded}, validateState: func(state historyState) error {
			return requireState(state.workPackages[request.WorkPackageID], state.workPackages[request.WorkPackageID] != "", project.WorkPackageReview)
		}}, err
	})
}

func (service *Service) AcceptWork(ctx context.Context, request AcceptWorkRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.AcceptedBy) {
			return operationSpec{}, ErrInvalidRequest
		}
		accepted, err := event(manifest, request.Metadata, "accept-work", project.EventWorkAccepted, request.WorkPackageID, "", project.WorkAcceptedPayload{AcceptedBy: request.AcceptedBy, Summary: redactText(request.Summary)})
		return operationSpec{records: []any{accepted}, validateState: func(state historyState) error {
			if err := requireState(state.workPackages[request.WorkPackageID], state.workPackages[request.WorkPackageID] != "", project.WorkPackageReview); err != nil {
				return err
			}
			if !state.approved[request.WorkPackageID] {
				return fmt.Errorf("%w: work package requires an approved review before acceptance", ErrInvalidTransition)
			}
			return nil
		}}, err
	})
}
