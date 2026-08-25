// Package projector builds rebuildable local database projections from the
// canonical Agent Project filesystem records. It intentionally depends on the
// project and storage packages, while neither sync nor project depends on it.
package projector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/filesystem"
	"syncgate/internal/project"
	"syncgate/internal/storage"
)

const CheckpointStream = "portable-record-set-v1"

var (
	ErrInvalidManifest = errors.New("project ingestion manifest is invalid")
	ErrShareMismatch   = errors.New("project ingestion share identity mismatch")
)

type Trigger string

const (
	TriggerLocalWrite Trigger = "local_write"
	TriggerShareScan  Trigger = "share_scan"
	TriggerRebuild    Trigger = "rebuild"
)

type Diagnostic struct {
	RelativePath   string
	Code           string
	Severity       string
	Message        string
	QuarantinePath string
}

type Report struct {
	ProjectID          string
	Trigger            Trigger
	DiscoveredRecords  int
	ProjectedEvents    int
	ProjectedArtifacts int
	ProjectedTasks     int
	ProjectedTaskNodes int
	PendingEvents      int
	RejectedRecords    int
	AlreadyPresent     int
	Unchanged          bool
	CheckpointHash     string
	Diagnostics        []Diagnostic
}

// Hooks exist for deterministic crash-window testing and embedding. They run
// outside database transactions. Returning an error simulates interruption.
type Hooks struct {
	AfterDiscovery        func() error
	AfterProjectionCommit func() error
}

type Projector struct {
	Store storage.Store
	Now   func() time.Time
	Hooks Hooks

	locks sync.Map
}

func (service *Projector) Ingest(ctx context.Context, rootPath string, trigger Trigger) (Report, error) {
	return service.run(ctx, rootPath, "", trigger, false)
}

func (service *Projector) IngestShare(ctx context.Context, shareID core.ShareID, rootPath string, trigger Trigger) (Report, error) {
	return service.run(ctx, rootPath, shareID, trigger, false)
}

func (service *Projector) Rebuild(ctx context.Context, rootPath string) (Report, error) {
	return service.run(ctx, rootPath, "", TriggerRebuild, true)
}

func (service *Projector) run(ctx context.Context, rootPath string, expectedShareID core.ShareID, trigger Trigger, rebuild bool) (Report, error) {
	if ctx == nil {
		return Report{}, errors.New("project ingestion context is required")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if service == nil || service.Store == nil {
		return Report{}, errors.New("project ingestion store is required")
	}
	layout, err := project.NewLayout(rootPath)
	if err != nil {
		return Report{}, err
	}
	gate := service.projectGate(layout.Root())
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return Report{}, ctx.Err()
	}

	manifestRecord, err := project.ReadPortableRecord(layout, project.ManifestRelativePath)
	if err != nil {
		if errors.Is(err, project.ErrRecordNotFound) {
			return Report{}, err
		}
		return Report{}, ErrInvalidManifest
	}
	manifest, ok := manifestRecord.Value.(*project.ProjectManifest)
	if !ok {
		return Report{}, ErrInvalidManifest
	}
	if expectedShareID != "" && core.ShareID(manifest.Authority.ShareID) != expectedShareID {
		return Report{ProjectID: manifest.ProjectID, Trigger: trigger}, ErrShareMismatch
	}

	candidates, err := discoverCandidates(ctx, layout, manifest.ProjectID)
	if err != nil {
		return Report{ProjectID: manifest.ProjectID, Trigger: trigger}, err
	}
	prepareArtifactCandidates(layout, candidates)
	taskAggregates, err := prepareTaskAggregates(ctx, candidates)
	if err != nil {
		return Report{ProjectID: manifest.ProjectID, Trigger: trigger}, err
	}
	report := Report{ProjectID: manifest.ProjectID, Trigger: trigger, DiscoveredRecords: len(candidates)}
	if service.Hooks.AfterDiscovery != nil {
		if err := service.Hooks.AfterDiscovery(); err != nil {
			return report, err
		}
	}

	registration := storage.ProjectRegistration{
		ProjectID: manifest.ProjectID, ShareID: core.ShareID(manifest.Authority.ShareID), RootPath: layout.Root(),
		Name: manifest.Name, AuthorityDeviceID: core.DeviceID(manifest.Authority.DeviceID),
		ManifestRecordID: manifest.RecordID, ManifestRecordHash: manifestRecord.Digest,
		ManifestPath: project.ManifestRelativePath, RegisteredAt: manifest.CreatedAt,
	}
	if _, err := service.Store.ProjectRegistrations().RegisterProject(ctx, registration); err != nil {
		return report, fmt.Errorf("register project projection: %w", err)
	}

	fingerprint, lastPath := candidateFingerprint(candidates)
	report.CheckpointHash = fingerprint
	if !rebuild {
		checkpoint, checkpointErr := service.Store.ProjectCheckpoints().GetProjectCheckpoint(ctx, manifest.ProjectID, CheckpointStream)
		if checkpointErr == nil && checkpoint.LastRecordHash == fingerprint && checkpoint.LastRecordPath == lastPath {
			report.Unchanged = true
			return report, nil
		}
		if checkpointErr != nil && !errors.Is(checkpointErr, storage.ErrNotFound) {
			return report, fmt.Errorf("read project ingestion checkpoint: %w", checkpointErr)
		}
	}

	state := buildProjectionState(candidates, manifest.RecordID)
	type projectedTask struct {
		task  storage.ProjectTaskProjection
		nodes []storage.ProjectTaskNodeProjection
	}
	projectedTasks := make([]projectedTask, 0, len(taskAggregates))
	for _, aggregate := range taskAggregates {
		snapshot, reduceErr := aggregate.reduce(ctx, candidates, state)
		if reduceErr != nil {
			return report, reduceErr
		}
		taskProjection, nodeProjections := aggregate.project(snapshot)
		projectedTasks = append(projectedTasks, projectedTask{task: taskProjection, nodes: nodeProjections})
	}
	for _, candidate := range candidates {
		if candidate.valid {
			continue
		}
		report.RejectedRecords++
		quarantinePath := quarantineCandidate(layout, candidate)
		report.Diagnostics = append(report.Diagnostics, Diagnostic{
			RelativePath: candidate.relativePath, Code: candidate.reasonCode, Severity: "warning",
			Message: diagnosticMessage(candidate.reasonCode), QuarantinePath: quarantinePath,
		})
		candidate.quarantinePath = quarantinePath
	}

	apply := func(writer storage.ProjectProjectionWriter) error {
		for _, candidate := range candidates {
			if !candidate.valid {
				if candidate.relativePath == project.ManifestRelativePath {
					continue
				}
				if err := writer.RecordProjectRejection(ctx, storage.ProjectProjectionRejection{
					ProjectID: manifest.ProjectID, RecordPath: candidate.relativePath, ObservedHash: candidate.observedHash,
					ReasonCode: candidate.reasonCode, QuarantinePath: candidate.quarantinePath, RejectedAt: service.currentTime(),
				}); err != nil {
					return err
				}
				continue
			}
			switch record := candidate.decoded.Value.(type) {
			case *project.WorkEvent:
				status := eventProjectionStatus(*record, state)
				result, err := writer.SaveProjectEvent(ctx, mapProjectEvent(*record, candidate.relativePath, candidate.decoded.Digest, status))
				if err != nil {
					return err
				}
				report.ProjectedEvents++
				if result.AlreadyPresent {
					report.AlreadyPresent++
				}
				if status == "pending" {
					report.PendingEvents++
					report.Diagnostics = append(report.Diagnostics, Diagnostic{
						RelativePath: candidate.relativePath, Code: "unresolved_dependencies", Severity: "info",
						Message: diagnosticMessage("unresolved_dependencies"),
					})
				}
			case *project.ArtifactManifest:
				result, err := writer.SaveProjectArtifact(ctx, mapProjectArtifact(*record, candidate.relativePath, candidate.decoded.Digest))
				if err != nil {
					return err
				}
				report.ProjectedArtifacts++
				if result.AlreadyPresent {
					report.AlreadyPresent++
				}
			}
		}
		for _, projected := range projectedTasks {
			if _, err := writer.SaveProjectTask(ctx, projected.task); err != nil {
				return err
			}
			report.ProjectedTasks++
			for _, node := range projected.nodes {
				if _, err := writer.SaveProjectTaskNode(ctx, node); err != nil {
					return err
				}
				report.ProjectedTaskNodes++
			}
		}
		return nil
	}
	// A changed candidate-set fingerprint replaces the entire local projection
	// transactionally. This removes rows for records that disappeared or became
	// invalid and guarantees equivalence with an explicit clean rebuild.
	err = service.Store.ProjectProjections().RebuildProjectProjection(ctx, manifest.ProjectID, apply)
	if err != nil {
		return report, fmt.Errorf("commit project projection: %w", err)
	}
	if service.Hooks.AfterProjectionCommit != nil {
		if err := service.Hooks.AfterProjectionCommit(); err != nil {
			return report, err
		}
	}
	if err := service.Store.ProjectCheckpoints().SetProjectCheckpoint(ctx, storage.ProjectProjectionCheckpoint{
		ProjectID: manifest.ProjectID, Stream: CheckpointStream, LastRecordPath: lastPath,
		LastRecordHash: fingerprint, UpdatedAt: service.currentTime(),
	}); err != nil {
		return report, fmt.Errorf("advance project ingestion checkpoint: %w", err)
	}
	sort.Slice(report.Diagnostics, func(i, j int) bool {
		if report.Diagnostics[i].RelativePath == report.Diagnostics[j].RelativePath {
			return report.Diagnostics[i].Code < report.Diagnostics[j].Code
		}
		return report.Diagnostics[i].RelativePath < report.Diagnostics[j].RelativePath
	})
	return report, nil
}

func (service *Projector) projectGate(root string) chan struct{} {
	value, _ := service.locks.LoadOrStore(strings.ToLower(filepath.Clean(root)), make(chan struct{}, 1))
	return value.(chan struct{})
}

func (service *Projector) currentTime() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

type candidateRecord struct {
	relativePath   string
	decoded        project.DecodedRecord
	valid          bool
	reasonCode     string
	observedHash   string
	quarantinePath string
	raw            []byte
}

func discoverCandidates(ctx context.Context, layout project.Layout, projectID string) ([]*candidateRecord, error) {
	controlPath := layout.ControlPath()
	paths := make([]string, 0)
	err := filepath.WalkDir(controlPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return errors.New("project candidate discovery failed")
		}
		relative, err := filepath.Rel(layout.Root(), path)
		if err != nil {
			return errors.New("project candidate discovery failed")
		}
		relative = filepath.ToSlash(relative)
		if relative == project.LocalDirectory && entry.IsDir() {
			return filepath.SkipDir
		}
		if entry.IsDir() || !isCanonicalCandidateShape(relative) {
			return nil
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	candidates := make([]*candidateRecord, 0, len(paths))
	for _, relativePath := range paths {
		candidate := &candidateRecord{relativePath: relativePath}
		decoded, err := project.ReadPortableRecord(layout, relativePath)
		if err == nil {
			if recordProjectID(decoded.Value) != projectID {
				err = errors.New("project identity mismatch")
				candidate.reasonCode = "project_identity_mismatch"
			} else if expected, pathErr := project.RecordRelativePath(decoded.Value); pathErr != nil || expected != relativePath {
				err = errors.New("record path mismatch")
				candidate.reasonCode = "record_path_mismatch"
			}
		}
		if err == nil {
			candidate.decoded = decoded
			candidate.valid = true
			candidate.observedHash = decoded.Digest
			candidate.raw = append([]byte(nil), decoded.Canonical...)
		} else {
			if candidate.reasonCode == "" {
				candidate.reasonCode = "invalid_record"
			}
			candidate.raw, candidate.observedHash = readRejectedCandidate(layout, relativePath)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func isCanonicalCandidateShape(relativePath string) bool {
	if relativePath == project.ManifestRelativePath {
		return true
	}
	parts := strings.Split(relativePath, "/")
	if len(parts) == 7 && parts[0] == project.ControlDirectory && parts[1] == "history" && parts[2] == "events" &&
		len(parts[3]) == 4 && len(parts[4]) == 2 && len(parts[5]) == 2 && strings.HasSuffix(parts[6], ".json") {
		return true
	}
	if len(parts) == 4 && parts[0] == project.ControlDirectory && parts[1] == "work-packages" && parts[3] == "definition.json" {
		return true
	}
	if len(parts) == 6 && parts[0] == project.ControlDirectory && parts[1] == "tasks" && parts[3] == "revisions" && parts[5] == "task.json" {
		return true
	}
	if len(parts) == 7 && parts[0] == project.ControlDirectory && parts[1] == "tasks" && parts[3] == "revisions" && parts[5] == "graphs" && strings.HasSuffix(parts[6], ".json") {
		return true
	}
	if len(parts) == 4 && parts[0] == project.ControlDirectory && parts[1] == "executions" && (parts[3] == "manifest.json" || parts[3] == "handoff.json") {
		return true
	}
	return len(parts) == 4 && parts[0] == project.ControlDirectory && parts[1] == "artifacts" && parts[2] == "manifests" && strings.HasSuffix(parts[3], ".json")
}

func recordProjectID(value any) string {
	switch record := value.(type) {
	case *project.ProjectManifest:
		return record.ProjectID
	case *project.TaskRevision:
		return record.ProjectID
	case *project.DependencyGraphRevision:
		return record.ProjectID
	case *project.WorkPackageDefinition:
		return record.ProjectID
	case *project.ExecutionManifest:
		return record.ProjectID
	case *project.WorkEvent:
		return record.ProjectID
	case *project.Handoff:
		return record.ProjectID
	case *project.ArtifactManifest:
		return record.ProjectID
	default:
		return ""
	}
}

func prepareArtifactCandidates(layout project.Layout, candidates []*candidateRecord) {
	for _, candidate := range candidates {
		if !candidate.valid {
			continue
		}
		artifact, ok := candidate.decoded.Value.(*project.ArtifactManifest)
		if !ok || artifact.BlobRelativePath == "" {
			continue
		}
		blobPath := project.ControlDirectory + "/" + artifact.BlobRelativePath
		err := project.VerifyProjectFile(layout, blobPath, project.ContentIdentity{
			Size: artifact.Size, HashAlgorithm: artifact.HashAlgorithm, ContentHash: artifact.ContentHash,
		})
		if err != nil {
			candidate.valid = false
			candidate.reasonCode = "artifact_content_mismatch"
		}
	}
}

func readRejectedCandidate(layout project.Layout, relativePath string) ([]byte, string) {
	resolved, info, exists, err := layout.InspectManaged(relativePath)
	if err != nil || !exists || !info.Mode().IsRegular() || info.Size() > project.MaxRecordBytes {
		return nil, ""
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, ""
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, ""
	}
	raw, err := io.ReadAll(io.LimitReader(file, project.MaxRecordBytes+1))
	if err != nil || len(raw) > project.MaxRecordBytes {
		return nil, ""
	}
	digest := sha256.Sum256(raw)
	return raw, hex.EncodeToString(digest[:])
}

func candidateFingerprint(candidates []*candidateRecord) (string, string) {
	hasher := sha256.New()
	lastPath := project.ManifestRelativePath
	for _, candidate := range candidates {
		lastPath = candidate.relativePath
		_, _ = io.WriteString(hasher, candidate.relativePath)
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, candidate.observedHash)
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, candidate.reasonCode)
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil)), lastPath
}

type projectionState struct {
	workPackages map[string]bool
	executions   map[string]bool
	artifacts    map[string]bool
	recordIDs    map[string]bool
}

func buildProjectionState(candidates []*candidateRecord, manifestRecordID string) projectionState {
	state := projectionState{
		workPackages: make(map[string]bool), executions: make(map[string]bool),
		artifacts: make(map[string]bool), recordIDs: map[string]bool{manifestRecordID: true},
	}
	for _, candidate := range candidates {
		if !candidate.valid {
			continue
		}
		switch record := candidate.decoded.Value.(type) {
		case *project.WorkPackageDefinition:
			state.workPackages[record.WorkPackageID] = true
			state.recordIDs[record.RecordID] = true
		case *project.ExecutionManifest:
			state.executions[record.ExecutionID] = true
			state.recordIDs[record.RecordID] = true
		case *project.Handoff:
			state.recordIDs[record.RecordID] = true
		case *project.ArtifactManifest:
			state.artifacts[record.ArtifactID] = true
			state.recordIDs[record.RecordID] = true
		}
	}
	return state
}

func eventProjectionStatus(event project.WorkEvent, state projectionState) string {
	if event.WorkPackageID != "" && !state.workPackages[event.WorkPackageID] {
		return "pending"
	}
	if event.ExecutionID != "" && !state.executions[event.ExecutionID] {
		return "pending"
	}
	var referencedRecordID string
	switch event.EventType {
	case project.EventProjectRegistered:
		var payload project.ProjectRegisteredPayload
		_ = json.Unmarshal(event.Payload, &payload)
		referencedRecordID = payload.ManifestRecordID
	case project.EventWorkPackageCreated:
		var payload project.WorkPackageCreatedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		referencedRecordID = payload.DefinitionRecordID
	case project.EventExecutionStarted:
		var payload project.ExecutionStartedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		referencedRecordID = payload.ManifestRecordID
	case project.EventHandoffCreated:
		var payload project.HandoffCreatedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		referencedRecordID = payload.HandoffRecordID
	case project.EventArtifactRecorded:
		var payload project.ArtifactRecordedPayload
		_ = json.Unmarshal(event.Payload, &payload)
		if !state.artifacts[payload.ArtifactID] {
			return "pending"
		}
		referencedRecordID = payload.ArtifactRecordID
	}
	if referencedRecordID != "" && !state.recordIDs[referencedRecordID] {
		return "pending"
	}
	return "accepted"
}

func mapProjectEvent(event project.WorkEvent, relativePath, digest, status string) storage.ProjectEventProjection {
	return storage.ProjectEventProjection{
		ProjectID: event.ProjectID, EventID: event.RecordID, RecordHash: digest, EventType: string(event.EventType),
		OccurredAt: event.OccurredAt, WorkPackageID: event.WorkPackageID, ExecutionID: event.ExecutionID,
		ProducerWorkerID: event.Producer.WorkerID, ProducerDeviceID: core.DeviceID(event.Producer.DeviceID),
		ProducerTrade: event.Producer.Trade, ProducerProvider: event.Producer.Provider, ProducerModel: event.Producer.Model,
		Status: status, RecordPath: relativePath, PayloadJSON: append([]byte(nil), event.Payload...),
	}
}

func mapProjectArtifact(artifact project.ArtifactManifest, relativePath, digest string) storage.ProjectArtifactProjection {
	return storage.ProjectArtifactProjection{
		ProjectID: artifact.ProjectID, ArtifactID: artifact.ArtifactID, RecordID: artifact.RecordID,
		RecordHash: digest, Name: artifact.Name, MediaType: artifact.MediaType, Size: artifact.Size,
		ContentHash: artifact.ContentHash, BlobPath: artifact.BlobRelativePath,
		WorkPackageID: artifact.Provenance.WorkPackageID, ExecutionID: artifact.Provenance.ExecutionID,
		ProducerWorkerID: artifact.Provenance.Producer.WorkerID,
		ProducerDeviceID: core.DeviceID(artifact.Provenance.Producer.DeviceID), ProducerModel: artifact.Provenance.Producer.Model,
		CreatedAt: artifact.CreatedAt, RecordPath: relativePath,
	}
}

func quarantineCandidate(layout project.Layout, candidate *candidateRecord) string {
	if len(candidate.raw) == 0 || candidate.relativePath == project.ManifestRelativePath {
		return ""
	}
	digest := sha256.Sum256(candidate.raw)
	relativePath := project.QuarantineDirectory + "/rejected-records/" + hex.EncodeToString(digest[:]) + ".json"
	destination, err := filesystem.EnsureParentDirectoriesInsideShare(layout.Root(), relativePath, 0o700)
	if err != nil {
		return ""
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := readExistingQuarantine(layout, relativePath)
		if readErr == nil && bytes.Equal(existing, candidate.raw) {
			return relativePath
		}
		return ""
	}
	if err != nil {
		return ""
	}
	if _, err := file.Write(candidate.raw); err != nil {
		_ = file.Close()
		_ = os.Remove(destination)
		return ""
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(destination)
		return ""
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(destination)
		return ""
	}
	return relativePath
}

func readExistingQuarantine(layout project.Layout, relativePath string) ([]byte, error) {
	resolved, info, exists, err := filesystem.InspectInsideShare(layout.Root(), relativePath)
	if err != nil || !exists || !info.Mode().IsRegular() {
		return nil, errors.New("quarantine record is unsafe")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("quarantine record changed during inspection")
	}
	return io.ReadAll(io.LimitReader(file, project.MaxRecordBytes+1))
}

func diagnosticMessage(code string) string {
	switch code {
	case "invalid_record":
		return "candidate record failed canonical validation"
	case "project_identity_mismatch":
		return "candidate record belongs to a different project"
	case "record_path_mismatch":
		return "candidate record is not stored at its canonical derived path"
	case "artifact_content_mismatch":
		return "artifact content is missing or does not match its manifest"
	case "unresolved_dependencies":
		return "event is preserved pending referenced project records"
	case "missing_task_graph":
		return "task revision is preserved pending its dependency graph"
	case "missing_task_revision":
		return "dependency graph is preserved pending its task revision"
	case "invalid_task_graph":
		return "task graph failed deterministic membership or dependency validation"
	default:
		return "project record was not accepted"
	}
}
