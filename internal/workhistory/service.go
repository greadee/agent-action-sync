package workhistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"syncgate/internal/project"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
)

var projectWriteLocks syncMap

// syncMap is deliberately tiny so all Service instances in this process share
// one per-project writer gate.
type syncMap struct{ values sync.Map }

func (values *syncMap) gate(key string) chan struct{} {
	created := make(chan struct{}, 1)
	actual, _ := values.values.LoadOrStore(key, created)
	return actual.(chan struct{})
}

type operationSpec struct {
	records       []any
	validateState func(historyState) error
	beforePublish func() error
}

func (service *Service) operate(ctx context.Context, metadata Metadata, build func(project.ProjectManifest, project.Layout) (operationSpec, error)) (OperationResult, error) {
	if ctx == nil {
		return OperationResult{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := validateMetadata(metadata); err != nil {
		return OperationResult{}, err
	}
	if service == nil || service.Store == nil || (service.Projector == nil && service.Project == nil) {
		return OperationResult{}, fmt.Errorf("%w: service is not configured", ErrInvalidRequest)
	}
	layout, err := project.NewLayout(metadata.RootPath)
	if err != nil {
		return OperationResult{}, err
	}
	gate := projectWriteLocks.gate(layout.Root())
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	}

	decoded, err := project.ReadPortableRecord(layout, project.ManifestRelativePath)
	if err != nil {
		return OperationResult{}, err
	}
	manifest, ok := decoded.Value.(*project.ProjectManifest)
	if !ok {
		return OperationResult{}, project.ErrRecordIntegrity
	}
	spec, err := build(*manifest, layout)
	if err != nil {
		return OperationResult{}, err
	}
	if len(spec.records) == 0 {
		return OperationResult{}, fmt.Errorf("%w: operation has no records", ErrInvalidRequest)
	}
	for _, record := range spec.records {
		if _, err := project.MarshalRecord(record); err != nil {
			return OperationResult{}, fmt.Errorf("%w: typed record validation failed", ErrInvalidRequest)
		}
	}

	result := OperationResult{ProjectID: manifest.ProjectID}
	allPresent := true
	for _, record := range spec.records {
		verified, verifyErr := project.VerifyRecord(layout, record)
		if verifyErr == nil {
			path, _ := project.RecordRelativePath(record)
			result.Records = append(result.Records, RecordResult{RecordID: recordIDOf(record), RelativePath: path, AlreadyPresent: true})
			_ = verified
			continue
		}
		if errors.Is(verifyErr, project.ErrRecordNotFound) {
			allPresent = false
			continue
		}
		return OperationResult{}, verifyErr
	}
	if allPresent {
		if spec.beforePublish != nil {
			if err := spec.beforePublish(); err != nil {
				return OperationResult{}, err
			}
		}
		return service.projectCanonical(ctx, layout.Root(), result)
	}

	if _, err := service.runProjector(ctx, layout.Root()); err != nil {
		return OperationResult{}, err
	}
	if err := service.rejectProjectedIdentityConflict(ctx, manifest.ProjectID, spec.records); err != nil {
		return OperationResult{}, err
	}
	state, err := service.loadState(ctx, manifest.ProjectID)
	if err != nil {
		return OperationResult{}, err
	}
	if spec.validateState != nil {
		if err := spec.validateState(state); err != nil {
			return OperationResult{}, err
		}
	}
	if spec.beforePublish != nil {
		if err := spec.beforePublish(); err != nil {
			return OperationResult{}, err
		}
	}

	result.Records = result.Records[:0]
	for _, record := range spec.records {
		published, err := project.PublishRecord(layout, record)
		if err != nil {
			return OperationResult{}, err
		}
		result.Records = append(result.Records, RecordResult{
			RecordID: recordIDOf(record), RelativePath: published.RelativePath,
			Created: published.Created, AlreadyPresent: published.AlreadyPresent,
		})
	}
	return service.projectCanonical(ctx, layout.Root(), result)
}

func (service *Service) rejectProjectedIdentityConflict(ctx context.Context, projectID string, records []any) error {
	for _, record := range records {
		eventRecord, ok := record.(project.WorkEvent)
		if !ok {
			continue
		}
		canonical, err := project.MarshalRecord(eventRecord)
		if err != nil {
			return err
		}
		decoded, err := project.DecodeRecord(canonical)
		if err != nil {
			return err
		}
		existing, err := service.Store.ProjectEvents().GetProjectEvent(ctx, projectID, eventRecord.RecordID)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if existing.RecordHash != decoded.Digest {
			return project.ErrRecordConflict
		}
	}
	return nil
}

func (service *Service) projectCanonical(ctx context.Context, root string, result OperationResult) (OperationResult, error) {
	if _, err := service.runProjector(ctx, root); err != nil {
		return result, &RecoverableProjectionError{Err: err}
	}
	return result, nil
}

func (service *Service) runProjector(ctx context.Context, root string) (projector.Report, error) {
	if service.Project != nil {
		return service.Project(ctx, root, projector.TriggerLocalWrite)
	}
	return service.Projector.Ingest(ctx, root, projector.TriggerLocalWrite)
}

func deterministicID(prefix, projectID, operation, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(projectID + "\x00" + operation + "\x00" + idempotencyKey))
	return prefix + hex.EncodeToString(digest[:16])
}

func payload(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func event(manifest project.ProjectManifest, metadata Metadata, operation string, eventType project.EventType, workPackageID, executionID string, value any) (project.WorkEvent, error) {
	raw, err := payload(value)
	if err != nil {
		return project.WorkEvent{}, err
	}
	return project.WorkEvent{
		RecordHeader: project.NewRecordHeader(project.RecordWorkEvent, deterministicID("evt-", manifest.ProjectID, operation, metadata.IdempotencyKey), manifest.ProjectID),
		EventType:    eventType, OccurredAt: metadata.OccurredAt, WorkPackageID: workPackageID, ExecutionID: executionID,
		Producer: sanitizeProducer(metadata.Producer), CausationID: metadata.CausationID, Correlation: metadata.Correlation, Payload: raw,
	}, nil
}

func provenance(metadata Metadata, workPackageID, executionID string, sourceArtifactIDs []string) project.Provenance {
	return project.Provenance{
		Producer: sanitizeProducer(metadata.Producer), WorkPackageID: workPackageID, ExecutionID: executionID,
		SourceArtifactIDs: append([]string(nil), sourceArtifactIDs...), ProjectVersion: redactText(metadata.ProjectVersion),
		ContextVersion: redactText(metadata.ContextVersion), InstructionVersion: redactText(metadata.InstructionVersion), CreatedAt: metadata.OccurredAt,
	}
}

func recordIDOf(record any) string {
	switch value := record.(type) {
	case project.WorkPackageDefinition:
		return value.RecordID
	case project.ExecutionManifest:
		return value.RecordID
	case project.WorkEvent:
		return value.RecordID
	case project.Handoff:
		return value.RecordID
	case project.ArtifactManifest:
		return value.RecordID
	default:
		return ""
	}
}

func requireState(actual project.WorkPackageState, exists bool, expected project.WorkPackageState) error {
	if !exists {
		return ErrStateNotFound
	}
	if actual != expected {
		return fmt.Errorf("%w: work package is not in the expected state", ErrInvalidTransition)
	}
	return nil
}

func requireExecution(actual project.ExecutionState, exists bool, allowed ...project.ExecutionState) error {
	if !exists {
		return ErrStateNotFound
	}
	for _, state := range allowed {
		if actual == state {
			return nil
		}
	}
	return fmt.Errorf("%w: execution is not in an allowed state", ErrInvalidTransition)
}

func validatePaths(paths []string) error {
	for _, value := range paths {
		if err := project.ValidateProjectRelativePath(value); err != nil {
			return err
		}
	}
	return nil
}

func nonblank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}
