package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"syncgate/internal/filesystem"
)

func TestDiscoverProjectFromNestedDirectoryAndFile(t *testing.T) {
	root := t.TempDir()
	layout, err := NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishRecord(layout, sampleProjectManifest()); err != nil {
		t.Fatalf("publish manifest: %v", err)
	}
	nested := filepath.Join(root, "workspace", "internal", "project")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(nested, "models.go")
	if err := os.WriteFile(file, []byte("package project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{nested, file} {
		discovered, err := DiscoverProject(start)
		if err != nil {
			t.Fatalf("DiscoverProject(%q): %v", start, err)
		}
		if discovered.Layout.Root() != filepath.Clean(root) || discovered.Manifest.ProjectID != testProjectID {
			t.Fatalf("discovered = root %q manifest %#v", discovered.Layout.Root(), discovered.Manifest)
		}
	}
}

func TestDiscoverProjectRejectsMissingMalformedAndSymlinkedState(t *testing.T) {
	if _, err := DiscoverProject(t.TempDir()); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("missing manifest error = %v", err)
	}

	malformedRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(malformedRoot, ControlDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedRoot, filepath.FromSlash(ManifestRelativePath)), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverProject(malformedRoot); !errors.Is(err, ErrRecordIntegrity) {
		t.Fatalf("malformed manifest error = %v", err)
	}

	realRoot := t.TempDir()
	layout, _ := NewLayout(realRoot)
	if _, err := PublishRecord(layout, sampleProjectManifest()); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "file.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(realRoot, "workspace-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := DiscoverProject(filepath.Join(link, "file.txt")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlinked discovery error = %v", err)
	}
}

func TestReadPortableRecordRejectsNoncanonicalAndSymlinkLeaf(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	path, err := filesystem.EnsureParentDirectoriesInsideShare(root, ManifestRelativePath, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	canonical := mustMarshalRecord(t, sampleProjectManifest())
	pretty := append([]byte("\n"), canonical...)
	if err := os.WriteFile(path, pretty, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPortableRecord(layout, ManifestRelativePath); !errors.Is(err, ErrRecordIntegrity) {
		t.Fatalf("noncanonical read error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(outside, canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadPortableRecord(layout, ManifestRelativePath); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink leaf read error = %v", err)
	}
}

func TestPortableReadRejectsLocalStateAndDiscoveryIgnoresPartialManifest(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	localPath := filepath.Join(root, filepath.FromSlash(QuarantineDirectory), "record.json")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, mustMarshalRecord(t, sampleProjectManifest()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPortableRecord(layout, QuarantineDirectory+"/record.json"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("local portable read error = %v", err)
	}

	control := filepath.Join(root, ControlDirectory)
	if err := os.MkdirAll(control, 0o700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(control, ".manifest.json-interrupted"+TemporaryRecordSuffix)
	if err := os.WriteFile(partial, mustMarshalRecord(t, sampleProjectManifest()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverProject(root); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("partial-only discovery error = %v", err)
	}
}
