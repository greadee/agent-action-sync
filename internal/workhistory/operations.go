package workhistory

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"syncgate/internal/orchestration"
	"syncgate/internal/project"
)

func (service *Service) CreateTask(ctx context.Context, request CreateTaskRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.TaskID, request.Objective) || request.TaskRevision != 1 || request.GraphRevision != 1 || len(request.WorkPackages) == 0 {
			return operationSpec{}, ErrInvalidRequest
		}
		risk, resources, gates, barriers := orchestration.NormalizeTaskInputs(request.Risk, request.Resources, request.QualityGates, request.Barriers)
		task := project.TaskRevision{
			RecordHeader: project.NewRecordHeader(project.RecordTaskRevision, deterministicID("task-", manifest.ProjectID, "create-task-revision", request.IdempotencyKey), manifest.ProjectID),
			TaskID:       request.TaskID, Revision: request.TaskRevision, Objective: redactText(request.Objective), Priority: request.Priority,
			Risk: risk, Resources: resources, GraphRevision: request.GraphRevision, QualityGates: gates,
			CreatedAt: request.OccurredAt, Provenance: provenance(request.Metadata, "", "", nil),
		}
		if _, err := portableRecordDigest(task); err != nil {
			return operationSpec{}, fmt.Errorf("%w: task definition is invalid: %v", ErrInvalidRequest, err)
		}

		requests := append([]TaskWorkPackageRequest(nil), request.WorkPackages...)
		sort.Slice(requests, func(i, j int) bool { return requests[i].WorkPackageID < requests[j].WorkPackageID })
		definitions := make(map[string]orchestration.WorkPackageDefinition, len(requests))
		records := make([]any, 0, 2+len(requests)*2)
		records = append(records, task)
		for _, work := range requests {
			if _, duplicate := definitions[work.WorkPackageID]; duplicate {
				return operationSpec{}, fmt.Errorf("%w: duplicate work package %s", ErrInvalidRequest, work.WorkPackageID)
			}
			if !nonblank(work.WorkPackageID, work.Objective, work.Trade) || len(work.Deliverables) == 0 || len(work.AcceptanceCriteria) == 0 {
				return operationSpec{}, ErrInvalidRequest
			}
			if err := validateScopePaths(append(append(append([]string{}, work.Scope.Allowed...), work.Scope.Inspect...), work.Scope.Forbidden...)); err != nil {
				return operationSpec{}, err
			}
			workRisk, workResources, workGates, _ := orchestration.NormalizeTaskInputs(work.Risk, work.Resources, work.QualityGates, nil)
			if work.Priority == "" {
				work.Priority = request.Priority
			}
			if len(workRisk) == 0 {
				workRisk = append([]project.RiskDimension(nil), risk...)
			}
			if workResources == nil {
				workResources = cloneResourceConstraints(resources)
			}
			if len(workGates) == 0 {
				workGates = append([]project.QualityGateReference(nil), gates...)
			}
			dependencies := append([]string(nil), work.Dependencies...)
			sort.Strings(dependencies)
			definitionID := deterministicID("wp-", manifest.ProjectID, "create-task-work-package:"+work.WorkPackageID, request.IdempotencyKey)
			definition := project.WorkPackageDefinition{
				RecordHeader:  project.NewRecordHeader(project.RecordWorkPackage, definitionID, manifest.ProjectID),
				WorkPackageID: work.WorkPackageID, Objective: redactText(work.Objective), Trade: redactText(work.Trade),
				Specialization: redactText(work.Specialization), Scope: work.Scope, Dependencies: dependencies,
				Deliverables: redactList(work.Deliverables), AcceptanceCriteria: redactList(work.AcceptanceCriteria),
				ReviewRequired: work.ReviewRequired, TaskID: request.TaskID, TaskRevision: request.TaskRevision,
				GraphRevision: request.GraphRevision, Priority: work.Priority, Risk: workRisk, Resources: workResources, QualityGates: workGates,
				TradeReference: cloneRegistryReference(work.TradeReference),
				CreatedAt:      request.OccurredAt, Provenance: provenance(request.Metadata, work.WorkPackageID, "", nil),
			}
			digest, err := portableRecordDigest(definition)
			if err != nil {
				return operationSpec{}, fmt.Errorf("%w: work package %s is invalid: %v", ErrInvalidRequest, work.WorkPackageID, err)
			}
			definitions[work.WorkPackageID] = orchestration.WorkPackageDefinition{Record: definition, RecordDigest: digest}
			records = append(records, definition)
		}
		dependencyDigest, err := orchestration.DependencySetDigest(ctx, definitions)
		if err != nil {
			return operationSpec{}, err
		}
		members := make([]project.DependencyGraphMember, 0, len(requests))
		for _, work := range requests {
			definition := definitions[work.WorkPackageID]
			members = append(members, project.DependencyGraphMember{
				WorkPackageID: work.WorkPackageID, DefinitionRecordID: definition.Record.RecordID, DefinitionDigest: definition.RecordDigest,
			})
		}
		graph := project.DependencyGraphRevision{
			RecordHeader: project.NewRecordHeader(project.RecordDependencyGraph, deterministicID("graph-", manifest.ProjectID, "create-task-graph", request.IdempotencyKey), manifest.ProjectID),
			TaskID:       request.TaskID, TaskRevision: request.TaskRevision, Revision: request.GraphRevision,
			Members: members, DependencySetDigest: dependencyDigest, Barriers: barriers,
			CreatedAt: request.OccurredAt, Provenance: provenance(request.Metadata, "", "", nil),
		}
		if _, err := portableRecordDigest(graph); err != nil {
			return operationSpec{}, fmt.Errorf("%w: graph definition is invalid: %v", ErrInvalidRequest, err)
		}
		if err := orchestration.ValidateGraph(ctx, orchestration.Graph{Task: task, Revision: graph, Definitions: definitions}); err != nil {
			return operationSpec{}, err
		}
		records = append(records, graph)
		for _, work := range requests {
			definition := definitions[work.WorkPackageID].Record
			created, err := event(manifest, request.Metadata, "create-task-work-package-event:"+work.WorkPackageID, project.EventWorkPackageCreated, work.WorkPackageID, "", project.WorkPackageCreatedPayload{DefinitionRecordID: definition.RecordID})
			if err != nil {
				return operationSpec{}, err
			}
			records = append(records, created)
		}
		return operationSpec{records: records}, nil
	})
}

func portableRecordDigest(record any) (string, error) {
	raw, err := project.MarshalRecord(record)
	if err != nil {
		return "", err
	}
	decoded, err := project.DecodeRecord(raw)
	if err != nil {
		return "", err
	}
	return decoded.Digest, nil
}

func cloneResourceConstraints(value *project.ResourceConstraints) *project.ResourceConstraints {
	if value == nil {
		return nil
	}
	copy := *value
	copy.RequiredCapabilities = append([]string(nil), value.RequiredCapabilities...)
	copy.RequiredTools = append([]string(nil), value.RequiredTools...)
	copy.AllowedOS = append([]string(nil), value.AllowedOS...)
	copy.AllowedArchitectures = append([]string(nil), value.AllowedArchitectures...)
	return &copy
}

func cloneRegistryReference(value *project.RegistryReference) *project.RegistryReference {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (service *Service) CreateWorkPackage(ctx context.Context, request CreateWorkPackageRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.Objective, request.Trade) || len(request.Deliverables) == 0 || len(request.AcceptanceCriteria) == 0 {
			return operationSpec{}, ErrInvalidRequest
		}
		if err := validateScopePaths(append(append(append([]string{}, request.Scope.Allowed...), request.Scope.Inspect...), request.Scope.Forbidden...)); err != nil {
			return operationSpec{}, err
		}
		definitionID := deterministicID("wp-", manifest.ProjectID, "create-work-package-definition", request.IdempotencyKey)
		definition := project.WorkPackageDefinition{
			RecordHeader:  project.NewRecordHeader(project.RecordWorkPackage, definitionID, manifest.ProjectID),
			WorkPackageID: request.WorkPackageID, Objective: redactText(request.Objective), Trade: redactText(request.Trade),
			Specialization: redactText(request.Specialization), Scope: request.Scope, Dependencies: append([]string(nil), request.Dependencies...),
			Deliverables: redactList(request.Deliverables), AcceptanceCriteria: redactList(request.AcceptanceCriteria),
			ReviewRequired: request.ReviewRequired, TradeReference: cloneRegistryReference(request.TradeReference), CreatedAt: request.OccurredAt,
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
			Producer: sanitizeProducer(request.Producer), TradeReference: cloneRegistryReference(request.TradeReference), WorkerReference: cloneRegistryReference(request.WorkerReference), ContractReference: cloneRegistryReference(request.ContractReference), CreatedAt: request.OccurredAt,
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
		recorded, err := event(manifest, request.Metadata, "record-test", project.EventTestRecorded, request.WorkPackageID, request.ExecutionID, project.TestRecordedPayload{
			Name: redactText(request.Name), Outcome: request.Outcome, DurationMilliseconds: request.DurationMilliseconds,
			CommandID: request.CommandID, CommandDigest: request.CommandDigest, ExitCode: request.ExitCode,
			EvidenceID: request.EvidenceID, EvidenceDigest: request.EvidenceDigest,
		})
		return operationSpec{records: []any{recorded}, validateState: func(state historyState) error {
			return requireExecution(state.executions[request.ExecutionID], state.executions[request.ExecutionID] != "", project.ExecutionRunning)
		}}, err
	})
}

func (service *Service) RecordTelemetry(ctx context.Context, request RecordTelemetryRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.ExecutionID) || request.Summary.WorkPackageID != request.WorkPackageID {
			return operationSpec{}, ErrInvalidRequest
		}
		recorded, err := event(manifest, request.Metadata, "record-telemetry", project.EventTelemetryRecorded, request.WorkPackageID, request.ExecutionID, request.Summary)
		return operationSpec{records: []any{recorded}, validateState: func(state historyState) error {
			_, exists := state.executions[request.ExecutionID]
			if !exists {
				return ErrStateNotFound
			}
			return nil
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
