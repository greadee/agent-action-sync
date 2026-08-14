package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHashAndVerifyProjectFile(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	path := filepath.Join(root, "workspace", "report.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("verified project content")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(content)
	want := ContentIdentity{Size: int64(len(content)), HashAlgorithm: HashAlgorithmSHA256, ContentHash: hex.EncodeToString(wantHash[:])}
	got, err := HashProjectFile(layout, "workspace/report.txt")
	if err != nil || got != want {
		t.Fatalf("HashProjectFile = %#v, %v, want %#v", got, err, want)
	}
	if err := VerifyProjectFile(layout, "workspace/report.txt", want); err != nil {
		t.Fatalf("VerifyProjectFile: %v", err)
	}
	want.Size++
	if err := VerifyProjectFile(layout, "workspace/report.txt", want); !errors.Is(err, ErrRecordIntegrity) {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestHashProjectFileRejectsSymlinkAndNoncanonicalPath(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "workspace", "linked.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := HashProjectFile(layout, "workspace/linked.txt"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink hash error = %v", err)
	}
	if _, err := HashProjectFile(layout, `workspace\linked.txt`); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("noncanonical hash error = %v", err)
	}
}
