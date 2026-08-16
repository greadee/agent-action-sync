package project

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"syncgate/internal/filesystem"
)

type BootstrapStatus string

const (
	BootstrapReady              BootstrapStatus = "ready"
	BootstrapAlreadyInitialized BootstrapStatus = "already_initialized"
	BootstrapBlocked            BootstrapStatus = "blocked"
)

// ProjectBootstrapRequest describes a caller-selected project identity for an
// already-existing share root. Stage 4 deliberately does not bind this to a
// CLI, HTTP API, or persistent configuration migration flow.
type ProjectBootstrapRequest struct {
	RootPath  string
	ProjectID string
	Name      string
	Authority Authority
}

type BootstrapIssue struct {
	RelativePath string
	Reason       string
}

// ProjectBootstrapPreflight is read-only. A blocked preflight reports safe
// reasons for migration tooling without exposing absolute local paths.
type ProjectBootstrapPreflight struct {
	Layout                  Layout
	Status                  BootstrapStatus
	Issues                  []BootstrapIssue
	RequiredDirectories     []string
	ExistingDirectories     []string
	MissingDirectories      []string
	ScanPolicy              ProjectScanPolicy
	ExistingManifest        *ProjectManifest
	RegistrationEventExists bool
}

type ProjectBootstrapResult struct {
	Preflight           ProjectBootstrapPreflight
	CreatedDirectories  []string
	ExistingDirectories []string
	Manifest            PublishResult
	RegistrationEvent   PublishResult
}

// ProjectBootstrapper is the reusable application operation for creating a
// portable Agent Project. Callers may inject a clock and record ID source for
// deterministic tests; production defaults use crypto/rand and UTC time.
type ProjectBootstrapper struct {
	Now         func() time.Time
	NewRecordID func() (string, error)
}

func PreflightProjectBootstrap(request ProjectBootstrapRequest) (ProjectBootstrapPreflight, error) {
	return (ProjectBootstrapper{}).Preflight(request)
}

func BootstrapProject(request ProjectBootstrapRequest) (ProjectBootstrapResult, error) {
	return (ProjectBootstrapper{}).Bootstrap(request)
}

func (bootstrapper ProjectBootstrapper) Preflight(request ProjectBootstrapRequest) (ProjectBootstrapPreflight, error) {
	request, layout, err := normalizeBootstrapRequest(request)
	if err != nil {
		return ProjectBootstrapPreflight{}, err
	}
	preflight := ProjectBootstrapPreflight{
		Layout:              layout,
		Status:              BootstrapReady,
		RequiredDirectories: append([]string{}, bootstrapDirectories...),
		ScanPolicy:          NewProjectScanPolicy(nil),
	}

	for _, relativePath := range bootstrapDirectories {
		info, exists, inspectErr := inspectBootstrapDirectory(layout, relativePath)
		if inspectErr != nil {
			preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: relativePath, Reason: "cannot safely inspect required directory"})
			continue
		}
		if exists {
			if !info.IsDir() {
				preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: relativePath, Reason: "required directory path is not a directory"})
				continue
			}
			preflight.ExistingDirectories = append(preflight.ExistingDirectories, relativePath)
			continue
		}
		preflight.MissingDirectories = append(preflight.MissingDirectories, relativePath)
	}

	manifest, manifestExists := inspectBootstrapManifest(layout, request, &preflight)
	if !manifestExists {
		inspectUnclaimedRoot(layout, &preflight)
	} else if manifest != nil {
		preflight.ExistingManifest = manifest
		inspectRegistrationEvent(layout, *manifest, &preflight)
	}
	if len(preflight.Issues) > 0 {
		preflight.Status = BootstrapBlocked
	} else if preflight.ExistingManifest != nil && preflight.RegistrationEventExists {
		preflight.Status = BootstrapAlreadyInitialized
	}
	return preflight, nil
}

func (bootstrapper ProjectBootstrapper) Bootstrap(request ProjectBootstrapRequest) (ProjectBootstrapResult, error) {
	preflight, err := bootstrapper.Preflight(request)
	if err != nil {
		return ProjectBootstrapResult{}, err
	}
	if preflight.Status == BootstrapBlocked {
		return ProjectBootstrapResult{Preflight: preflight}, domainError(ErrRecordConflict, "bootstrap project", errors.New("project root is not eligible for bootstrap"))
	}
	result := ProjectBootstrapResult{Preflight: preflight}
	for _, relativePath := range bootstrapDirectories {
		created, ensureErr := ensureBootstrapDirectory(preflight.Layout, relativePath)
		if ensureErr != nil {
			return result, ensureErr
		}
		if created {
			result.CreatedDirectories = append(result.CreatedDirectories, relativePath)
		} else {
			result.ExistingDirectories = append(result.ExistingDirectories, relativePath)
		}
	}

	manifest := preflight.ExistingManifest
	if manifest == nil {
		recordID, idErr := bootstrapper.recordID()
		if idErr != nil {
			return result, idErr
		}
		manifest = &ProjectManifest{
			RecordHeader: NewRecordHeader(RecordProjectManifest, recordID, request.ProjectID),
			Name:         request.Name,
			Authority:    request.Authority,
			CreatedAt:    bootstrapper.now().UTC(),
		}
	}
	manifestResult, publishErr := PublishRecord(preflight.Layout, manifest)
	if publishErr != nil && errors.Is(publishErr, ErrRecordConflict) && preflight.ExistingManifest == nil {
		// Another bootstrap may have won the immutable manifest path after this
		// preflight. Re-read it before declaring a conflict: a matching project
		// identity is an idempotent concurrent initialization, even though its
		// random manifest record ID differs from ours.
		retry, retryErr := bootstrapper.Preflight(request)
		if retryErr == nil && retry.Status != BootstrapBlocked && retry.ExistingManifest != nil {
			preflight = retry
			result.Preflight = retry
			manifest = retry.ExistingManifest
			manifestResult, publishErr = PublishRecord(preflight.Layout, manifest)
		}
	}
	if publishErr != nil {
		return result, publishErr
	}
	result.Manifest = manifestResult

	event, eventErr := projectRegisteredEvent(*manifest)
	if eventErr != nil {
		return result, eventErr
	}
	eventResult, publishErr := PublishRecord(preflight.Layout, event)
	if publishErr != nil {
		return result, publishErr
	}
	result.RegistrationEvent = eventResult
	return result, nil
}

var bootstrapDirectories = []string{
	WorkspaceDirectory,
	ControlDirectory,
	ControlDirectory + "/history",
	HistoryEventsDirectory,
	WorkPackagesDirectory,
	ExecutionsDirectory,
	ControlDirectory + "/artifacts",
	ArtifactManifestsDirectory,
	ControlDirectory + "/artifacts/blobs",
	ArtifactBlobsDirectory,
	LocalDirectory,
	QuarantineDirectory,
}

func normalizeBootstrapRequest(request ProjectBootstrapRequest) (ProjectBootstrapRequest, Layout, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.Name = strings.TrimSpace(request.Name)
	request.Authority.DeviceID = strings.TrimSpace(request.Authority.DeviceID)
	request.Authority.ShareID = strings.TrimSpace(request.Authority.ShareID)
	layout, err := NewLayout(request.RootPath)
	if err != nil {
		return ProjectBootstrapRequest{}, Layout{}, err
	}
	if err := validateIdentifier("project_id", request.ProjectID, true); err != nil {
		return ProjectBootstrapRequest{}, Layout{}, domainError(ErrRecordIntegrity, "validate project bootstrap", err)
	}
	if err := validateText("name", request.Name, MaxNameBytes, true); err != nil {
		return ProjectBootstrapRequest{}, Layout{}, domainError(ErrRecordIntegrity, "validate project bootstrap", err)
	}
	if err := validateAuthority(request.Authority); err != nil {
		return ProjectBootstrapRequest{}, Layout{}, domainError(ErrRecordIntegrity, "validate project bootstrap", err)
	}
	return request, layout, nil
}

func inspectBootstrapDirectory(layout Layout, relativePath string) (fs.FileInfo, bool, error) {
	if relativePath == WorkspaceDirectory {
		_, info, exists, err := filesystem.InspectInsideShare(layout.root, relativePath)
		return info, exists, err
	}
	_, info, exists, err := layout.InspectManaged(relativePath)
	return info, exists, err
}

func ensureBootstrapDirectory(layout Layout, relativePath string) (bool, error) {
	info, exists, err := inspectBootstrapDirectory(layout, relativePath)
	if err != nil {
		return false, domainError(ErrUnsafePath, "create project directory", err)
	}
	if exists {
		if !info.IsDir() {
			return false, domainError(ErrRecordConflict, "create project directory", errors.New("required directory path is not a directory"))
		}
		return false, nil
	}
	if _, err := filesystem.EnsureParentDirectoriesInsideShare(layout.root, relativePath+"/.bootstrap", 0o700); err != nil {
		return false, domainError(ErrUnsafePath, "create project directory", err)
	}
	info, exists, err = inspectBootstrapDirectory(layout, relativePath)
	if err != nil || !exists || !info.IsDir() {
		if err == nil {
			err = errors.New("directory was not created")
		}
		return false, domainError(ErrFilesystem, "create project directory", err)
	}
	return true, nil
}

func inspectBootstrapManifest(layout Layout, request ProjectBootstrapRequest, preflight *ProjectBootstrapPreflight) (*ProjectManifest, bool) {
	_, info, exists, err := layout.InspectManaged(ManifestRelativePath)
	if err != nil {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: ManifestRelativePath, Reason: "cannot safely inspect manifest"})
		return nil, false
	}
	if !exists {
		return nil, false
	}
	if !info.Mode().IsRegular() {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: ManifestRelativePath, Reason: "manifest is not a regular file"})
		return nil, false
	}
	decoded, err := ReadPortableRecord(layout, ManifestRelativePath)
	if err != nil {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: ManifestRelativePath, Reason: "manifest is invalid or not canonical"})
		return nil, false
	}
	manifest, ok := decoded.Value.(*ProjectManifest)
	if !ok {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: ManifestRelativePath, Reason: "manifest path contains a different record type"})
		return nil, false
	}
	if manifest.ProjectID != request.ProjectID || manifest.Name != request.Name || manifest.Authority != request.Authority {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: ManifestRelativePath, Reason: "existing project identity does not match request"})
		return nil, false
	}
	return manifest, true
}

func inspectUnclaimedRoot(layout Layout, preflight *ProjectBootstrapPreflight) {
	entries, err := os.ReadDir(layout.root)
	if err != nil {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: ".", Reason: "cannot inspect share root"})
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == WorkspaceDirectory || name == ControlDirectory || name == ".sync-history" || name == ".sync-incoming" || migrationLocalRootEntry(name) || strings.HasSuffix(name, TemporaryRecordSuffix) {
			continue
		}
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: name, Reason: "unclaimed share root contains data outside workspace"})
	}
	if _, info, exists, err := layout.InspectManaged(ControlDirectory); err == nil && exists && info.IsDir() {
		entries, readErr := os.ReadDir(filepath.Join(layout.root, ControlDirectory))
		if readErr != nil {
			preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: ControlDirectory, Reason: "cannot inspect existing control directory"})
		} else if len(entries) > 0 {
			preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: ControlDirectory, Reason: "existing control directory has no valid manifest"})
		}
	}
}

func migrationLocalRootEntry(name string) bool {
	return name == ".git" || name == ".env" || strings.HasPrefix(name, ".env.") || name == ".secrets"
}

func inspectRegistrationEvent(layout Layout, manifest ProjectManifest, preflight *ProjectBootstrapPreflight) {
	event, err := projectRegisteredEvent(manifest)
	if err != nil {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: HistoryEventsDirectory, Reason: "cannot build registration event"})
		return
	}
	relativePath, err := RecordRelativePath(event)
	if err != nil {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: HistoryEventsDirectory, Reason: "cannot resolve registration event"})
		return
	}
	actual, err := ReadPortableRecord(layout, relativePath)
	if errors.Is(err, ErrRecordNotFound) {
		return
	}
	if err != nil {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: relativePath, Reason: "registration event is invalid or not canonical"})
		return
	}
	expected, err := MarshalRecord(event)
	if err != nil || !bytes.Equal(actual.Canonical, expected) {
		preflight.Issues = append(preflight.Issues, BootstrapIssue{RelativePath: relativePath, Reason: "registration event conflicts with manifest"})
		return
	}
	preflight.RegistrationEventExists = true
}

func (bootstrapper ProjectBootstrapper) now() time.Time {
	if bootstrapper.Now != nil {
		return bootstrapper.Now()
	}
	return time.Now().UTC()
}

func (bootstrapper ProjectBootstrapper) recordID() (string, error) {
	if bootstrapper.NewRecordID != nil {
		recordID, err := bootstrapper.NewRecordID()
		if err != nil {
			return "", domainError(ErrFilesystem, "allocate manifest record ID", err)
		}
		if err := validateIdentifier("record_id", recordID, true); err != nil {
			return "", domainError(ErrRecordIntegrity, "allocate manifest record ID", err)
		}
		return recordID, nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", domainError(ErrFilesystem, "allocate manifest record ID", err)
	}
	return "project-manifest-" + hex.EncodeToString(raw[:]), nil
}

func projectRegisteredEvent(manifest ProjectManifest) (WorkEvent, error) {
	payload, err := json.Marshal(ProjectRegisteredPayload{ManifestRecordID: manifest.RecordID, Authority: manifest.Authority})
	if err != nil {
		return WorkEvent{}, domainError(ErrRecordIntegrity, "build registration event", err)
	}
	digest := sha256.Sum256([]byte(manifest.RecordID))
	return WorkEvent{
		RecordHeader: NewRecordHeader(RecordWorkEvent, "project-registered-"+hex.EncodeToString(digest[:]), manifest.ProjectID),
		EventType:    EventProjectRegistered,
		OccurredAt:   manifest.CreatedAt.UTC(),
		Producer:     Producer{DeviceID: manifest.Authority.DeviceID},
		Payload:      payload,
	}, nil
}
