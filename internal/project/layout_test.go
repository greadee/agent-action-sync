package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLayoutBuildsCanonicalPortablePaths(t *testing.T) {
	root := t.TempDir()
	layout, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Root() != filepath.Clean(root) || layout.ManifestPath() != filepath.Join(root, ".agent-project", "manifest.json") {
		t.Fatalf("layout paths = root %q manifest %q", layout.Root(), layout.ManifestPath())
	}

	tests := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{name: "work package case folded", got: func() (string, error) { return WorkPackageDefinitionRelativePath("WP-Alpha") }, want: ".agent-project/work-packages/wp-alpha/definition.json"},
		{name: "execution manifest", got: func() (string, error) { return ExecutionManifestRelativePath("EX-001") }, want: ".agent-project/executions/ex-001/manifest.json"},
		{name: "namespaced execution manifest", got: func() (string, error) { return ExecutionManifestRelativePath("execution:one") }, want: ".agent-project/executions/execution%3aone/manifest.json"},
		{name: "handoff", got: func() (string, error) { return HandoffRelativePath("EX-001") }, want: ".agent-project/executions/ex-001/handoff.json"},
		{name: "result", got: func() (string, error) { return ExecutionResultRelativePath("EX-001", "reports/test.json") }, want: ".agent-project/executions/ex-001/results/reports/test.json"},
		{name: "event date", got: func() (string, error) {
			return WorkEventRelativePath("EVENT-A", time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC))
		}, want: ".agent-project/history/events/2026/08/13/event-a.json"},
		{name: "artifact", got: func() (string, error) { return ArtifactManifestRelativePath("ARTIFACT-A") }, want: ".agent-project/artifacts/manifests/artifact-a.json"},
		{name: "blob", got: func() (string, error) { return ArtifactBlobRelativePath(strings.Repeat("a", 64)) }, want: ".agent-project/artifacts/blobs/sha256/" + strings.Repeat("a", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.got()
			if err != nil || got != test.want {
				t.Fatalf("path = %q, %v, want %q", got, err, test.want)
			}
			resolved, err := layout.ResolvePortable(got)
			if err != nil || !strings.HasPrefix(filepath.Clean(resolved), filepath.Clean(root)+string(filepath.Separator)) {
				t.Fatalf("resolved path = %q, %v", resolved, err)
			}
		})
	}
}

func TestProjectRelativePathRejectsWindowsAliasesTraversalAndNoncanonicalForms(t *testing.T) {
	unsafe := []string{
		"", ".", "../manifest.json", "/etc/passwd", `C:\Users\owner\secret.txt`,
		`\\server\share\secret.txt`, `workspace\file.txt`, "workspace/../file.txt",
		"workspace/CON", "workspace/NUL.txt", "workspace/name.", "workspace/name ",
		"workspace/file.txt:stream", " workspace/file.txt ",
	}
	for _, value := range unsafe {
		if err := ValidateProjectRelativePath(value); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("ValidateProjectRelativePath(%q) error = %v, want ErrUnsafePath", value, err)
		}
	}
	for _, value := range []string{"workspace/file.txt", ".agent-project/manifest.json", ".agent-project/history/events/2026/08/13/event.json"} {
		if err := ValidateProjectRelativePath(value); err != nil {
			t.Errorf("ValidateProjectRelativePath(%q): %v", value, err)
		}
	}
	if _, err := WorkPackageDefinitionRelativePath("CON"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("reserved path-bearing ID error = %v", err)
	}
}

func TestLayoutManagedAndPortableContainment(t *testing.T) {
	layout, err := NewLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := layout.ResolveManaged("workspace/file.txt"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("workspace managed resolve error = %v", err)
	}
	if _, err := layout.ResolvePortable(".agent-project/local/quarantine/bad.json"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("local portable resolve error = %v", err)
	}
	if _, err := layout.ResolvePortable(ManifestRelativePath); err != nil {
		t.Fatalf("manifest portable resolve: %v", err)
	}
}

func TestNewLayoutAndManagedInspectionRejectSymlinks(t *testing.T) {
	realRoot := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewLayout(linkedRoot); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("symlink root error = %v", err)
	}

	layout, err := NewLayout(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(realRoot, ControlDirectory)); err != nil {
		t.Skipf("control symlink unavailable: %v", err)
	}
	if _, _, _, err := layout.InspectManaged(ManifestRelativePath); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("managed symlink error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside destination unexpectedly changed: %v", err)
	}
}

func TestDomainErrorsAreClassifiableAndDoNotPrintAbsolutePaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "private", "missing")
	_, err := NewLayout(missing)
	if !errors.Is(err, ErrInvalidRoot) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NewLayout error = %v", err)
	}
	if strings.Contains(err.Error(), missing) || strings.Contains(err.Error(), filepath.Dir(missing)) {
		t.Fatalf("domain error exposed absolute path: %v", err)
	}
	var domain *DomainError
	if !errors.As(err, &domain) || domain.Op != "validate project root" {
		t.Fatalf("domain error = %#v", domain)
	}
}

func TestRecordRelativePathMatchesRecordKinds(t *testing.T) {
	event := sampleEvent(EventTestRecorded, []byte(`{"name":"test","outcome":"passed"}`))
	event.WorkPackageID, event.ExecutionID = "WP-001", "EX-001"
	tests := []struct {
		record any
		want   string
	}{
		{sampleProjectManifest(), ManifestRelativePath},
		{sampleWorkPackage(), ".agent-project/work-packages/wp-001/definition.json"},
		{sampleExecution(), ".agent-project/executions/ex-001/manifest.json"},
		{event, ".agent-project/history/events/2026/08/12/event-one.json"},
		{sampleHandoff(), ".agent-project/executions/ex-001/handoff.json"},
		{sampleArtifact(), ".agent-project/artifacts/manifests/artifact-one.json"},
	}
	for _, test := range tests {
		got, err := RecordRelativePath(test.record)
		if err != nil || got != test.want {
			t.Errorf("RecordRelativePath(%T) = %q, %v, want %q", test.record, got, err, test.want)
		}
	}
	if runtime.GOOS == "windows" {
		upper, _ := WorkPackageDefinitionRelativePath("WP-CASE")
		lower, _ := WorkPackageDefinitionRelativePath("wp-case")
		if upper != lower {
			t.Fatalf("case variants map to different portable paths: %q %q", upper, lower)
		}
	}
}
