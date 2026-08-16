// Package projectmigration converts an eligible configured source share into
// an Agent Project without moving or rewriting workspace content.
package projectmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/filesystem"
	"syncgate/internal/insights"
	"syncgate/internal/project"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
)

const (
	MaxMigrationEntries = 10000
	MaxReportedPaths    = 64
)

var (
	ErrBlocked          = errors.New("project migration is blocked")
	ErrPreflightChanged = errors.New("project migration preflight changed")
	ErrConflict         = errors.New("project migration identity conflicts with existing state")
)

type Status string

const (
	StatusReady           Status = "ready"
	StatusAlreadyComplete Status = "already_complete"
	StatusBlocked         Status = "blocked"
)

type Request struct {
	ShareID                  core.ShareID
	RootPath                 string
	ShareMode                storage.ShareMode
	ConfiguredIgnorePatterns []string
	ProjectID                string
	Name                     string
	AuthorityDeviceID        core.DeviceID
}

type Issue struct {
	RelativePath string
	Reason       string
}

type Preflight struct {
	Status                          Status
	ShareID                         core.ShareID
	ProjectID                       string
	Name                            string
	RootIdentity                    string
	Issues                          []Issue
	ExcludedPaths                   []string
	RequiredIgnorePatterns          []string
	MissingConfiguredIgnorePatterns []string
	ConfigurationChangeRequired     bool
	ExpectedPortableRecords         []string
	ExistingProjectID               string
	Confirmation                    string
	stateDigest                     string
}

type ApplyResult struct {
	Status             string
	ProjectID          string
	ShareID            core.ShareID
	CreatedRecords     int
	ProjectedEvents    int
	ProjectedArtifacts int
	InsightCount       int
	EventWatermark     string
	AuditEventID       string
	ScanRequested      bool
}

type Service struct {
	Store        storage.Store
	Now          func() time.Time
	Bootstrapper project.ProjectBootstrapper
	Projector    *projector.Projector
	Insights     *insights.Calculator
	RequestScan  func(context.Context, core.ShareID) error
}

func (service *Service) Preflight(ctx context.Context, request Request) (Preflight, error) {
	if ctx == nil {
		return Preflight{}, errors.New("project migration context is required")
	}
	if err := ctx.Err(); err != nil {
		return Preflight{}, err
	}
	if service == nil || service.Store == nil {
		return Preflight{}, errors.New("project migration store is required")
	}
	if request.ShareMode != storage.ShareOneWaySource && request.ShareMode != storage.ShareUploadOnly {
		return blockedPreflight(request, "share must be a one-way source or upload-only authority"), nil
	}
	bootstrapRequest := project.ProjectBootstrapRequest{
		RootPath: request.RootPath, ProjectID: request.ProjectID, Name: request.Name,
		Authority: project.Authority{DeviceID: string(request.AuthorityDeviceID), ShareID: string(request.ShareID)},
	}
	bootstrap, err := service.Bootstrapper.Preflight(bootstrapRequest)
	if err != nil {
		return Preflight{}, err
	}
	stateDigest, excluded, scanIssues, err := inspectRoot(ctx, request.RootPath)
	if err != nil {
		return Preflight{}, err
	}
	result := Preflight{
		Status: StatusReady, ShareID: request.ShareID, ProjectID: strings.TrimSpace(request.ProjectID), Name: strings.TrimSpace(request.Name),
		RootIdentity: rootIdentity(request.RootPath), ExcludedPaths: excluded,
		RequiredIgnorePatterns:  append([]string(nil), project.RequiredProjectIgnorePatterns...),
		ExpectedPortableRecords: []string{project.ManifestRelativePath, "PROJECT_REGISTERED"}, stateDigest: stateDigest,
	}
	for _, issue := range bootstrap.Issues {
		result.Issues = append(result.Issues, Issue{RelativePath: issue.RelativePath, Reason: issue.Reason})
	}
	result.Issues = append(result.Issues, scanIssues...)
	result.MissingConfiguredIgnorePatterns = missingPatterns(request.ConfiguredIgnorePatterns, result.RequiredIgnorePatterns)

	registration, registrationErr := service.Store.ProjectRegistrations().GetProjectByShare(ctx, request.ShareID)
	switch {
	case registrationErr == nil:
		result.ExistingProjectID = registration.ProjectID
		if registration.ProjectID != result.ProjectID || registration.Name != result.Name || registration.AuthorityDeviceID != request.AuthorityDeviceID {
			result.Issues = append(result.Issues, Issue{RelativePath: project.ManifestRelativePath, Reason: "share is registered to a different project identity"})
		} else if bootstrap.Status == project.BootstrapAlreadyInitialized {
			result.Status = StatusAlreadyComplete
		}
	case !errors.Is(registrationErr, storage.ErrNotFound):
		return Preflight{}, registrationErr
	}
	if bootstrap.Status == project.BootstrapBlocked || len(result.Issues) > 0 {
		result.Status = StatusBlocked
	} else if bootstrap.Status == project.BootstrapAlreadyInitialized && result.ExistingProjectID == result.ProjectID {
		result.Status = StatusAlreadyComplete
	}
	sortIssues(result.Issues)
	if len(result.Issues) > MaxReportedPaths {
		result.Issues = result.Issues[:MaxReportedPaths]
	}
	if len(result.ExcludedPaths) > MaxReportedPaths {
		result.ExcludedPaths = result.ExcludedPaths[:MaxReportedPaths]
	}
	result.Confirmation = confirmation(result, request)
	return result, nil
}

func (service *Service) Apply(ctx context.Context, request Request, expectedConfirmation string) (ApplyResult, error) {
	preflight, err := service.Preflight(ctx, request)
	if err != nil {
		return ApplyResult{}, err
	}
	if expectedConfirmation == "" || expectedConfirmation != preflight.Confirmation {
		return ApplyResult{}, ErrPreflightChanged
	}
	if preflight.Status == StatusBlocked {
		return ApplyResult{}, ErrBlocked
	}
	bootstrapRequest := project.ProjectBootstrapRequest{
		RootPath: request.RootPath, ProjectID: request.ProjectID, Name: request.Name,
		Authority: project.Authority{DeviceID: string(request.AuthorityDeviceID), ShareID: string(request.ShareID)},
	}
	bootstrap, err := service.Bootstrapper.Bootstrap(bootstrapRequest)
	if err != nil {
		return ApplyResult{}, err
	}
	projection := service.Projector
	if projection == nil {
		projection = &projector.Projector{Store: service.Store, Now: service.Now}
	}
	report, err := projection.IngestShare(ctx, request.ShareID, request.RootPath, projector.TriggerLocalWrite)
	if err != nil {
		return ApplyResult{}, err
	}
	calculator := service.Insights
	if calculator == nil {
		calculator = &insights.Calculator{Store: service.Store, Now: service.Now}
	}
	snapshots, err := calculator.Rebuild(ctx, request.ProjectID)
	if err != nil {
		return ApplyResult{}, err
	}
	status := "applied"
	if preflight.Status == StatusAlreadyComplete {
		status = "already_applied"
	}
	audit := migrationAudit(request, status, service.now())
	if err := recordAuditIdempotently(ctx, service.Store.Audit(), audit); err != nil {
		return ApplyResult{}, err
	}
	scanRequested := false
	if service.RequestScan != nil {
		if err := service.RequestScan(ctx, request.ShareID); err != nil {
			return ApplyResult{}, err
		}
		scanRequested = true
	}
	created := 0
	if bootstrap.Manifest.Created {
		created++
	}
	if bootstrap.RegistrationEvent.Created {
		created++
	}
	watermark := ""
	if len(snapshots) > 0 {
		watermark = snapshots[0].SourceEventWatermark
	}
	return ApplyResult{
		Status: status, ProjectID: request.ProjectID, ShareID: request.ShareID, CreatedRecords: created,
		ProjectedEvents: report.ProjectedEvents, ProjectedArtifacts: report.ProjectedArtifacts,
		InsightCount: len(snapshots), EventWatermark: watermark, AuditEventID: audit.ID, ScanRequested: scanRequested,
	}, nil
}

func blockedPreflight(request Request, reason string) Preflight {
	result := Preflight{
		Status: StatusBlocked, ShareID: request.ShareID, ProjectID: strings.TrimSpace(request.ProjectID), Name: strings.TrimSpace(request.Name),
		RootIdentity: rootIdentity(request.RootPath), Issues: []Issue{{RelativePath: ".", Reason: reason}},
		RequiredIgnorePatterns:  append([]string(nil), project.RequiredProjectIgnorePatterns...),
		ExpectedPortableRecords: []string{project.ManifestRelativePath, "PROJECT_REGISTERED"},
	}
	result.MissingConfiguredIgnorePatterns = missingPatterns(request.ConfiguredIgnorePatterns, result.RequiredIgnorePatterns)
	result.Confirmation = confirmation(result, request)
	return result
}

func inspectRoot(ctx context.Context, root string) (string, []string, []Issue, error) {
	hasher := sha256.New()
	excludedSet := map[string]bool{}
	issues := make([]Issue, 0)
	entries := 0
	cleanRoot := filepath.Clean(root)
	err := filepath.WalkDir(cleanRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == cleanRoot {
			return nil
		}
		entries++
		if entries > MaxMigrationEntries {
			issues = append(issues, Issue{RelativePath: ".", Reason: "project root exceeds bounded preflight entry limit"})
			return fs.SkipAll
		}
		relative, err := filepath.Rel(cleanRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, err := filesystem.NormalizeRelativePath(relative); err != nil {
			if len(issues) < MaxReportedPaths {
				issues = append(issues, Issue{RelativePath: relative, Reason: "path is not portable"})
			}
		}
		if excludedRoot(relative) {
			top := excludedTop(relative)
			excludedSet[top] = true
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hasher, "%s\x00%s\x00%d\x00%d\n", relative, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode()&fs.ModeSymlink != 0 && len(issues) < MaxReportedPaths {
			issues = append(issues, Issue{RelativePath: relative, Reason: "symbolic links are unsupported in project shares"})
		}
		return nil
	})
	if err != nil {
		return "", nil, nil, err
	}
	excluded := make([]string, 0, len(excludedSet))
	for value := range excludedSet {
		excluded = append(excluded, value)
	}
	sort.Strings(excluded)
	return hex.EncodeToString(hasher.Sum(nil)), excluded, issues, nil
}

func excludedRoot(relative string) bool {
	parts := strings.Split(relative, "/")
	for _, part := range parts {
		if part == ".git" || part == ".secrets" || part == ".env" || strings.HasPrefix(part, ".env.") {
			return true
		}
	}
	return relative == project.LocalDirectory || strings.HasPrefix(relative, project.LocalDirectory+"/") ||
		relative == ".sync-history" || strings.HasPrefix(relative, ".sync-history/") ||
		relative == ".sync-incoming" || strings.HasPrefix(relative, ".sync-incoming/") || strings.HasSuffix(relative, project.TemporaryRecordSuffix)
}

func excludedTop(relative string) string {
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		if part == ".git" || part == ".secrets" || part == ".env" || strings.HasPrefix(part, ".env.") {
			return strings.Join(parts[:index+1], "/")
		}
	}
	if strings.HasPrefix(relative, project.LocalDirectory) {
		return project.LocalDirectory
	}
	return parts[0]
}

func missingPatterns(configured, required []string) []string {
	seen := make(map[string]bool, len(configured))
	for _, value := range configured {
		seen[filepath.ToSlash(strings.TrimSpace(value))] = true
	}
	missing := make([]string, 0)
	for _, value := range required {
		if !seen[value] {
			missing = append(missing, value)
		}
	}
	return missing
}

func rootIdentity(root string) string {
	absolute, _ := filepath.Abs(root)
	digest := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func confirmation(preflight Preflight, request Request) string {
	payload := struct {
		ShareID, ProjectID, Name, DeviceID, RootIdentity, StateDigest string
		Status                                                        Status
		Issues                                                        []Issue
		Excluded                                                      []string
	}{string(request.ShareID), strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.Name), string(request.AuthorityDeviceID), preflight.RootIdentity, preflight.stateDigest, preflight.Status, preflight.Issues, preflight.ExcludedPaths}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func sortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].RelativePath == issues[j].RelativePath {
			return issues[i].Reason < issues[j].Reason
		}
		return issues[i].RelativePath < issues[j].RelativePath
	})
}

func migrationAudit(request Request, status string, occurredAt time.Time) storage.AuditEvent {
	digest := sha256.Sum256([]byte(string(request.ShareID) + "\x00" + request.ProjectID))
	return storage.AuditEvent{
		ID: "project-migration-" + hex.EncodeToString(digest[:16]), EventName: "project_migration_applied",
		DeviceID: request.AuthorityDeviceID, ShareID: request.ShareID, Severity: "info",
		Metadata: map[string]string{"project_id": request.ProjectID, "result": status}, OccurredAt: occurredAt.UTC(),
	}
}

func recordAuditIdempotently(ctx context.Context, store storage.AuditStore, event storage.AuditEvent) error {
	existing, err := store.Get(ctx, event.ID)
	if err == nil {
		if existing.EventName == event.EventName && existing.DeviceID == event.DeviceID && existing.ShareID == event.ShareID && existing.Severity == event.Severity && existing.Metadata["project_id"] == event.Metadata["project_id"] {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return store.Record(ctx, event)
}

func (service *Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}
