package project

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func FuzzDecodeRecord(f *testing.F) {
	for _, name := range []string{
		"project_manifest.json", "work_package_definition.json", "execution_manifest.json",
		"work_event.json", "handoff.json", "artifact_manifest.json",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatalf("read seed %s: %v", name, err)
		}
		f.Add(bytes.TrimSpace(raw))
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"record_id":"one","record_id":"two"}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		decoded, err := DecodeRecord(raw)
		if err != nil {
			return
		}
		if len(decoded.Canonical) == 0 || len(decoded.Canonical) > MaxRecordBytes {
			t.Fatalf("successful decode returned invalid canonical size %d", len(decoded.Canonical))
		}
		redecoded, err := DecodeRecord(decoded.Canonical)
		if err != nil {
			t.Fatalf("canonical record did not re-decode: %v", err)
		}
		if !bytes.Equal(redecoded.Canonical, decoded.Canonical) || redecoded.Digest != decoded.Digest {
			t.Fatal("canonical decode is not stable")
		}
	})
}
