package buildinfo

import "testing"

func TestCurrentManifestIsValid(t *testing.T) {
	manifest := Current()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate current manifest: %v", err)
	}
	if manifest.GOOS == "" || manifest.GOARCH == "" {
		t.Fatalf("target is incomplete: %#v", manifest)
	}
}

func TestManifestRejectsInvalidCompatibilityWindow(t *testing.T) {
	manifest := Current()
	manifest.MinimumControlLayoutVersion = manifest.ControlLayoutVersion + 1
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected invalid compatibility window to fail")
	}
}
