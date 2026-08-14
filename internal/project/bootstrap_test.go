package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapProjectCreatesPortableLayoutAndRegistration(t *testing.T) {
	root := t.TempDir()
	workspaceFile := filepath.Join(root, WorkspaceDirectory, "notes.txt")
	if err := os.MkdirAll(filepath.Dir(workspaceFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceFile, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	registeredAt := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	bootstrapper := ProjectBootstrapper{
		Now:         func() time.Time { return registeredAt },
		NewRecordID: func() (string, error) { return "manifest-1", nil },
	}
	request := bootstrapRequest(root)

	preflight, err := bootstrapper.Preflight(request)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Status != BootstrapReady || len(preflight.Issues) != 0 {
		t.Fatalf("preflight = %#v", preflight)
	}
	if _, err := os.Stat(filepath.Join(root, ControlDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight wrote control state: %v", err)
	}

	result, err := bootstrapper.Bootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Manifest.Created || !result.RegistrationEvent.Created {
		t.Fatalf("bootstrap result = %#v", result)
	}
	if got, err := os.ReadFile(workspaceFile); err != nil || string(got) != "keep me" {
		t.Fatalf("workspace file changed: %q, %v", got, err)
	}
	layout, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	manifestRecord, err := ReadPortableRecord(layout, ManifestRelativePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := manifestRecord.Value.(*ProjectManifest)
	if manifest.ProjectID != request.ProjectID || manifest.Authority != request.Authority || !manifest.CreatedAt.Equal(registeredAt) {
		t.Fatalf("manifest = %#v", manifest)
	}
	event, err := projectRegisteredEvent(*manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRecord(layout, event); err != nil {
		t.Fatal(err)
	}
	for _, relativePath := range []string{HistoryEventsDirectory, WorkPackagesDirectory, ExecutionsDirectory, ArtifactManifestsDirectory, ArtifactBlobsDirectory, QuarantineDirectory} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relativePath)))
		if err != nil || !info.IsDir() {
			t.Fatalf("required directory %s missing or invalid: %v", relativePath, err)
		}
	}
}

func TestBootstrapProjectIsIdempotentWithoutAnotherRegistrationEvent(t *testing.T) {
	root := t.TempDir()
	request := bootstrapRequest(root)
	bootstrapper := ProjectBootstrapper{
		Now:         func() time.Time { return time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC) },
		NewRecordID: func() (string, error) { return "manifest-2", nil },
	}
	first, err := bootstrapper.Bootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := bootstrapper.Bootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Manifest.Created || !first.RegistrationEvent.Created || !second.Manifest.AlreadyPresent || !second.RegistrationEvent.AlreadyPresent {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	layout, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	historyRoot := filepath.Join(layout.Root(), filepath.FromSlash(HistoryEventsDirectory))
	var registrationEvents int
	err = filepath.WalkDir(historyRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, relErr := filepath.Rel(layout.Root(), path)
		if relErr != nil {
			return relErr
		}
		decoded, readErr := ReadPortableRecord(layout, filepath.ToSlash(relative))
		if readErr != nil {
			return readErr
		}
		if event, ok := decoded.Value.(*WorkEvent); ok && event.EventType == EventProjectRegistered {
			registrationEvents++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if registrationEvents != 1 {
		t.Fatalf("registration events = %d, want 1", registrationEvents)
	}
}

func TestBootstrapProjectRepairsMissingRegistrationEventFromManifest(t *testing.T) {
	root := t.TempDir()
	request := bootstrapRequest(root)
	registeredAt := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	bootstrapper := ProjectBootstrapper{
		Now:         func() time.Time { return registeredAt },
		NewRecordID: func() (string, error) { return "manifest-3", nil },
	}
	layout, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, relativePath := range bootstrapDirectories {
		if _, err := ensureBootstrapDirectory(layout, relativePath); err != nil {
			t.Fatal(err)
		}
	}
	manifest := ProjectManifest{
		RecordHeader: NewRecordHeader(RecordProjectManifest, "manifest-3", request.ProjectID),
		Name:         request.Name,
		Authority:    request.Authority,
		CreatedAt:    registeredAt,
	}
	if _, err := PublishRecord(layout, manifest); err != nil {
		t.Fatal(err)
	}
	preflight, err := bootstrapper.Preflight(request)
	if err != nil || preflight.Status != BootstrapReady || preflight.RegistrationEventExists {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	result, err := bootstrapper.Bootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Manifest.AlreadyPresent || !result.RegistrationEvent.Created {
		t.Fatalf("result = %#v", result)
	}
}

func TestBootstrapProjectAdoptsMatchingExistingManifest(t *testing.T) {
	root := t.TempDir()
	request := bootstrapRequest(root)
	registeredAt := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	layout, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, relativePath := range bootstrapDirectories {
		if _, err := ensureBootstrapDirectory(layout, relativePath); err != nil {
			t.Fatal(err)
		}
	}
	manifest := ProjectManifest{
		RecordHeader: NewRecordHeader(RecordProjectManifest, "manifest-from-other-bootstrap", request.ProjectID),
		Name:         request.Name,
		Authority:    request.Authority,
		CreatedAt:    registeredAt,
	}
	if _, err := PublishRecord(layout, manifest); err != nil {
		t.Fatal(err)
	}
	bootstrapper := ProjectBootstrapper{
		NewRecordID: func() (string, error) { return "must-not-be-used", errors.New("existing manifest should be adopted") },
	}
	result, err := bootstrapper.Bootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Manifest.AlreadyPresent || !result.RegistrationEvent.Created {
		t.Fatalf("result = %#v", result)
	}
}

func TestBootstrapPreflightBlocksUnsafeAndMismatchedRootsWithoutWriting(t *testing.T) {
	t.Run("unclaimed root data", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("do not move"), 0o600); err != nil {
			t.Fatal(err)
		}
		preflight, err := PreflightProjectBootstrap(bootstrapRequest(root))
		if err != nil {
			t.Fatal(err)
		}
		if preflight.Status != BootstrapBlocked || len(preflight.Issues) == 0 || preflight.Issues[0].RelativePath != "existing.txt" {
			t.Fatalf("preflight = %#v", preflight)
		}
		if _, err := BootstrapProject(bootstrapRequest(root)); !errors.Is(err, ErrRecordConflict) {
			t.Fatalf("bootstrap error = %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(root, "existing.txt")); err != nil || string(got) != "do not move" {
			t.Fatalf("unclaimed data changed: %q, %v", got, err)
		}
	})

	t.Run("mismatched identity", func(t *testing.T) {
		root := t.TempDir()
		request := bootstrapRequest(root)
		bootstrapper := ProjectBootstrapper{NewRecordID: func() (string, error) { return "manifest-4", nil }}
		if _, err := bootstrapper.Bootstrap(request); err != nil {
			t.Fatal(err)
		}
		request.Name = "different project"
		preflight, err := bootstrapper.Preflight(request)
		if err != nil || preflight.Status != BootstrapBlocked {
			t.Fatalf("preflight=%#v err=%v", preflight, err)
		}
		if !strings.Contains(preflight.Issues[0].Reason, "identity") {
			t.Fatalf("issues = %#v", preflight.Issues)
		}
	})
}

func bootstrapRequest(root string) ProjectBootstrapRequest {
	return ProjectBootstrapRequest{
		RootPath:  root,
		ProjectID: "project-1",
		Name:      "Agent Project",
		Authority: Authority{DeviceID: "device-1", ShareID: "share-1"},
	}
}
