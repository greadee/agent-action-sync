package workhistory

import (
	"context"
	"fmt"
	"strings"

	"syncgate/internal/project"
	"syncgate/internal/storage"
)

func (service *Service) CreateHandoff(ctx context.Context, request CreateHandoffRequest) (OperationResult, error) {
	return service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, _ project.Layout) (operationSpec, error) {
		if !nonblank(request.WorkPackageID, request.ExecutionID, request.HandoffID) || len(request.CompletedWork) == 0 {
			return operationSpec{}, ErrInvalidRequest
		}
		if err := validatePaths(request.ChangedFiles); err != nil {
			return operationSpec{}, err
		}
		handoffID := deterministicID("handoff-", manifest.ProjectID, "create-handoff-record", request.IdempotencyKey)
		handoff := project.Handoff{
			RecordHeader: project.NewRecordHeader(project.RecordHandoff, handoffID, manifest.ProjectID),
			HandoffID:    request.HandoffID, WorkPackageID: request.WorkPackageID, ExecutionID: request.ExecutionID,
			CompletedWork: redactList(request.CompletedWork), ChangedFiles: append([]string(nil), request.ChangedFiles...),
			Decisions: redactList(request.Decisions), Tests: append([]project.TestResult(nil), request.Tests...),
			Limitations: redactList(request.Limitations), UnresolvedIssues: redactList(request.UnresolvedIssues),
			Assumptions: redactList(request.Assumptions), FollowUpWork: redactList(request.FollowUpWork),
			ReviewRequirements: redactList(request.ReviewRequirements), IntegrationConsiderations: redactList(request.IntegrationConsiderations),
			Confidence: request.Confidence, FailureConditions: redactList(request.FailureConditions), CreatedAt: request.OccurredAt,
			Provenance: provenance(request.Metadata, request.WorkPackageID, request.ExecutionID, nil),
		}
		for index := range handoff.Tests {
			handoff.Tests[index].Name = redactText(handoff.Tests[index].Name)
		}
		created, err := event(manifest, request.Metadata, "create-handoff-event", project.EventHandoffCreated, request.WorkPackageID, request.ExecutionID, project.HandoffCreatedPayload{HandoffRecordID: handoffID})
		return operationSpec{records: []any{handoff, created}, validateState: func(state historyState) error {
			return requireExecution(state.executions[request.ExecutionID], state.executions[request.ExecutionID] != "", project.ExecutionCompleted, project.ExecutionFailed)
		}}, err
	})
}

func (service *Service) RegisterArtifact(ctx context.Context, request RegisterArtifactRequest) (OperationResult, error) {
	if err := validateMetadata(request.Metadata); err != nil {
		return OperationResult{}, err
	}
	layout, err := project.NewLayout(request.RootPath)
	if err != nil {
		return OperationResult{}, err
	}
	if !nonblank(request.ArtifactID, request.WorkPackageID, request.Name, request.MediaType, request.SourceRelativePath) {
		return OperationResult{}, ErrInvalidRequest
	}
	identity, err := project.HashProjectFile(layout, request.SourceRelativePath)
	if err != nil {
		return OperationResult{}, err
	}
	blobRelativePath := ""
	if request.EmbedBlob {
		full, pathErr := project.ArtifactBlobRelativePath(identity.ContentHash)
		if pathErr != nil {
			return OperationResult{}, pathErr
		}
		blobRelativePath = strings.TrimPrefix(full, project.ControlDirectory+"/")
	}
	var blob project.BlobPublishResult
	result, operationErr := service.operate(ctx, request.Metadata, func(manifest project.ProjectManifest, operationLayout project.Layout) (operationSpec, error) {
		recordID := deterministicID("artifact-", manifest.ProjectID, "register-artifact-record", request.IdempotencyKey)
		artifact := project.ArtifactManifest{
			RecordHeader: project.NewRecordHeader(project.RecordArtifact, recordID, manifest.ProjectID),
			ArtifactID:   request.ArtifactID, Name: redactText(request.Name), MediaType: request.MediaType,
			Size: identity.Size, HashAlgorithm: identity.HashAlgorithm, ContentHash: identity.ContentHash,
			BlobRelativePath: blobRelativePath, CreatedAt: request.OccurredAt,
			Provenance: provenance(request.Metadata, request.WorkPackageID, request.ExecutionID, request.SourceArtifactIDs),
		}
		recorded, eventErr := event(manifest, request.Metadata, "register-artifact-event", project.EventArtifactRecorded, request.WorkPackageID, request.ExecutionID, project.ArtifactRecordedPayload{ArtifactRecordID: recordID, ArtifactID: request.ArtifactID})
		return operationSpec{
			records: []any{artifact, recorded},
			validateState: func(state historyState) error {
				if _, exists := state.workPackages[request.WorkPackageID]; !exists {
					return ErrStateNotFound
				}
				if request.ExecutionID != "" {
					if _, exists := state.executions[request.ExecutionID]; !exists {
						return ErrStateNotFound
					}
				}
				return nil
			},
			beforePublish: func() error {
				if !request.EmbedBlob {
					return project.VerifyProjectFile(operationLayout, request.SourceRelativePath, identity)
				}
				published, publishErr := project.PublishArtifactBlob(operationLayout, request.SourceRelativePath)
				if publishErr != nil {
					return publishErr
				}
				if published.ContentHash != identity.ContentHash || published.Size != identity.Size {
					return project.ErrRecordConflict
				}
				blob = published
				return nil
			},
		}, eventErr
	})
	if request.EmbedBlob {
		if blob.RelativePath == "" {
			full, _ := project.ArtifactBlobRelativePath(identity.ContentHash)
			blob = project.BlobPublishResult{RelativePath: full, ContentHash: identity.ContentHash, Size: identity.Size, AlreadyPresent: true}
		}
		result.Records = append([]RecordResult{{RecordID: blob.ContentHash, RelativePath: blob.RelativePath, Created: blob.Created, AlreadyPresent: blob.AlreadyPresent}}, result.Records...)
	}
	return result, operationErr
}

func (service *Service) GetEvent(ctx context.Context, projectID, eventID string) (storage.ProjectEventProjection, error) {
	if service == nil || service.Store == nil {
		return storage.ProjectEventProjection{}, ErrInvalidRequest
	}
	return service.Store.ProjectEvents().GetProjectEvent(ctx, projectID, eventID)
}

func (service *Service) ListEvents(ctx context.Context, query storage.ProjectEventQuery) (storage.Page[storage.ProjectEventProjection], error) {
	if service == nil || service.Store == nil || query.Page.Limit < 1 || query.Page.Limit > storage.MaxAdminPageLimit {
		return storage.Page[storage.ProjectEventProjection]{}, fmt.Errorf("%w: event query limit must be between 1 and %d", ErrInvalidRequest, storage.MaxAdminPageLimit)
	}
	return service.Store.ProjectEvents().ListProjectEvents(ctx, query)
}

func (service *Service) GetArtifact(ctx context.Context, projectID, artifactID string) (storage.ProjectArtifactProjection, error) {
	if service == nil || service.Store == nil {
		return storage.ProjectArtifactProjection{}, ErrInvalidRequest
	}
	return service.Store.ProjectArtifacts().GetProjectArtifact(ctx, projectID, artifactID)
}

func (service *Service) ListArtifacts(ctx context.Context, query storage.ProjectArtifactQuery) (storage.Page[storage.ProjectArtifactProjection], error) {
	if service == nil || service.Store == nil || query.Page.Limit < 1 || query.Page.Limit > storage.MaxAdminPageLimit {
		return storage.Page[storage.ProjectArtifactProjection]{}, fmt.Errorf("%w: artifact query limit must be between 1 and %d", ErrInvalidRequest, storage.MaxAdminPageLimit)
	}
	return service.Store.ProjectArtifacts().ListProjectArtifacts(ctx, query)
}
