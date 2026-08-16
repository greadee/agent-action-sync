package projectmigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/project"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestMigrationPreflightAndApplyAreExplicitIdempotentAndNonDestructive(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspaceFile := filepath.Join(root, project.WorkspaceDirectory, "existing.txt")
	for path, content := range map[string]string{
		workspaceFile:                          "keep me",
		filepath.Join(root, ".git", "config"):  "git-private",
		filepath.Join(root, ".env"):            "TOKEN=private",
		filepath.Join(root, ".secrets", "key"): "private-key",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := migrationTestStore(t, root)
	scans := 0
	when := time.Date(2026, time.August, 14, 15, 0, 0, 0, time.UTC)
	service := &Service{
		Store: store, Now: func() time.Time { return when },
		Bootstrapper: project.ProjectBootstrapper{Now: func() time.Time { return when }, NewRecordID: func() (string, error) { return "manifest-migration", nil }},
		RequestScan:  func(context.Context, core.ShareID) error { scans++; return nil },
	}
	request := migrationTestRequest(root)
	preflight, err := service.Preflight(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Status != StatusReady || preflight.Confirmation == "" || preflight.RootIdentity == "" || preflight.ConfigurationChangeRequired {
		t.Fatalf("preflight=%+v", preflight)
	}
	for _, expected := range []string{".git", ".env", ".secrets"} {
		if !contains(preflight.ExcludedPaths, expected) {
			t.Fatalf("preflight exclusions=%v, missing %s", preflight.ExcludedPaths, expected)
		}
	}
	result, err := service.Apply(ctx, request, preflight.Confirmation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.CreatedRecords != 2 || result.ProjectedEvents < 1 || result.InsightCount == 0 || !result.ScanRequested || scans != 1 {
		t.Fatalf("apply=%+v scans=%d", result, scans)
	}
	if got, err := os.ReadFile(workspaceFile); err != nil || string(got) != "keep me" {
		t.Fatalf("workspace changed: %q err=%v", got, err)
	}
	retryPreflight, err := service.Preflight(ctx, request)
	if err != nil || retryPreflight.Status != StatusAlreadyComplete {
		t.Fatalf("retry preflight=%+v err=%v", retryPreflight, err)
	}
	retry, err := service.Apply(ctx, request, retryPreflight.Confirmation)
	if err != nil || retry.Status != "already_applied" || retry.CreatedRecords != 0 || scans != 2 {
		t.Fatalf("retry=%+v scans=%d err=%v", retry, scans, err)
	}
	events, err := store.Audit().ListRecent(ctx, 10)
	if err != nil || len(events) != 1 || events[0].EventName != "project_migration_applied" {
		t.Fatalf("audit=%+v err=%v", events, err)
	}
}

func TestMigrationApplyRevalidatesAndBlockedRootsRemainUnchanged(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, project.WorkspaceDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	store := migrationTestStore(t, root)
	service := &Service{Store: store, Bootstrapper: project.ProjectBootstrapper{NewRecordID: func() (string, error) { return "manifest-revalidate", nil }}}
	request := migrationTestRequest(root)
	preflight, err := service.Preflight(ctx, request)
	if err != nil || preflight.Status != StatusReady {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	outsideWorkspace := filepath.Join(root, "do-not-move.txt")
	if err := os.WriteFile(outsideWorkspace, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(ctx, request, preflight.Confirmation); !errors.Is(err, ErrPreflightChanged) {
		t.Fatalf("apply after root change error=%v", err)
	}
	blocked, err := service.Preflight(ctx, request)
	if err != nil || blocked.Status != StatusBlocked || len(blocked.Issues) == 0 {
		t.Fatalf("blocked=%+v err=%v", blocked, err)
	}
	if _, err := service.Apply(ctx, request, blocked.Confirmation); !errors.Is(err, ErrBlocked) {
		t.Fatalf("blocked apply error=%v", err)
	}
	if got, err := os.ReadFile(outsideWorkspace); err != nil || string(got) != "untouched" {
		t.Fatalf("blocked content changed: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, project.ManifestRelativePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked migration created a manifest: %v", err)
	}
}

func migrationTestStore(t *testing.T, root string) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Shares().SaveShare(context.Background(), storage.Share{ID: "share-migration", Name: "existing", RootPath: root, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	return store
}

func migrationTestRequest(root string) Request {
	return Request{
		ShareID: "share-migration", RootPath: root, ShareMode: storage.ShareOneWaySource,
		ProjectID: "project-migration", Name: "Migrated Project", AuthorityDeviceID: "device-source",
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
