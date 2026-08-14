package project

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"syncgate/internal/filesystem"
)

func TestPublishRecordCreatesVerifiesAndReplaysImmutableRecord(t *testing.T) {
	layout, _ := NewLayout(t.TempDir())
	record := sampleWorkPackage()
	result, err := PublishRecord(layout, record)
	if err != nil {
		t.Fatalf("PublishRecord: %v", err)
	}
	if !result.Created || result.AlreadyPresent || result.RelativePath != ".agent-project/work-packages/wp-001/definition.json" || !validSHA256(result.Digest) {
		t.Fatalf("publish result = %#v", result)
	}
	verified, err := VerifyRecord(layout, record)
	if err != nil || verified.Digest != result.Digest {
		t.Fatalf("VerifyRecord = %q, %v", verified.Digest, err)
	}
	replay, err := PublishRecord(layout, record)
	if err != nil || replay.Created || !replay.AlreadyPresent || replay.Digest != result.Digest {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	assertNoTemporaryRecords(t, layout.Root())
}

func TestPublishRecordRejectsSamePathDifferentContentAndCaseVariant(t *testing.T) {
	layout, _ := NewLayout(t.TempDir())
	first := sampleWorkPackage()
	if _, err := PublishRecord(layout, first); err != nil {
		t.Fatal(err)
	}
	different := sampleWorkPackage()
	different.Objective = "Different immutable content."
	if _, err := PublishRecord(layout, different); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("different content error = %v", err)
	}
	caseVariant := sampleWorkPackage()
	caseVariant.WorkPackageID = "wp-001"
	caseVariant.Provenance.WorkPackageID = "wp-001"
	if _, err := PublishRecord(layout, caseVariant); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("case variant error = %v", err)
	}
	if _, err := VerifyRecord(layout, first); err != nil {
		t.Fatalf("original record changed: %v", err)
	}
}

func TestPublishRecordRejectsMalformedPreexistingDestination(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	record := sampleProjectManifest()
	destination, err := filesystem.EnsureParentDirectoriesInsideShare(root, ManifestRelativePath, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte(`{"malformed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishRecord(layout, record); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("malformed destination error = %v", err)
	}
	raw, err := os.ReadFile(destination)
	if err != nil || string(raw) != `{"malformed":true}` {
		t.Fatalf("preexisting destination changed: %q, %v", raw, err)
	}
}

func TestPublishRecordFlushFailureNeverExposesFinalRecord(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	operations := defaultPublishOperations()
	operations.createTemp = func(directory, pattern string) (temporaryFile, error) {
		file, err := os.CreateTemp(directory, pattern)
		if err != nil {
			return nil, err
		}
		return &failingSyncFile{File: file}, nil
	}
	if _, err := publishRecord(layout, sampleProjectManifest(), operations); !errors.Is(err, ErrFilesystem) {
		t.Fatalf("flush failure error = %v", err)
	}
	if _, err := os.Lstat(layout.ManifestPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final record exposed after flush failure: %v", err)
	}
	assertNoTemporaryRecords(t, root)
}

func TestPublishRecordAtomicCreateFailureLeavesNoFinalRecord(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	operations := defaultPublishOperations()
	operations.publish = func(string, string) error { return errors.New("injected publish failure") }
	if _, err := publishRecord(layout, sampleProjectManifest(), operations); !errors.Is(err, ErrFilesystem) {
		t.Fatalf("publish failure error = %v", err)
	}
	if _, err := os.Lstat(layout.ManifestPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final record exposed after publish failure: %v", err)
	}
	assertNoTemporaryRecords(t, root)
}

func TestPublishRecordRejectsSymlinkControlComponentWithoutOutsideWrite(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ControlDirectory)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := PublishRecord(layout, sampleProjectManifest()); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink publish error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside path changed: %v", err)
	}
}

func TestConcurrentIdenticalPublishHasOneCreator(t *testing.T) {
	layout, _ := NewLayout(t.TempDir())
	record := sampleProjectManifest()
	const publishers = 8
	results := make(chan PublishResult, publishers)
	errorsFound := make(chan error, publishers)
	var wait sync.WaitGroup
	for range publishers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := PublishRecord(layout, record)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent publish: %v", err)
	}
	created, replayed := 0, 0
	for result := range results {
		if result.Created {
			created++
		}
		if result.AlreadyPresent {
			replayed++
		}
	}
	if created != 1 || replayed != publishers-1 {
		t.Fatalf("created=%d replayed=%d", created, replayed)
	}
	assertNoTemporaryRecords(t, layout.Root())
}

func TestConcurrentDifferentPublishNeverOverwritesWinner(t *testing.T) {
	layout, _ := NewLayout(t.TempDir())
	first := sampleProjectManifest()
	second := sampleProjectManifest()
	second.Name = "Project Bravo"
	start := make(chan struct{})
	type outcome struct {
		result PublishResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, record := range []ProjectManifest{first, second} {
		go func(record ProjectManifest) {
			<-start
			result, err := PublishRecord(layout, record)
			outcomes <- outcome{result: result, err: err}
		}(record)
	}
	close(start)
	created, conflicts := 0, 0
	for range 2 {
		outcome := <-outcomes
		if outcome.result.Created {
			created++
		}
		if errors.Is(outcome.err, ErrRecordConflict) {
			conflicts++
		} else if outcome.err != nil {
			t.Errorf("unexpected publish error: %v", outcome.err)
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("created=%d conflicts=%d", created, conflicts)
	}
	decoded, err := ReadPortableRecord(layout, ManifestRelativePath)
	if err != nil {
		t.Fatal(err)
	}
	name := decoded.Value.(*ProjectManifest).Name
	if name != first.Name && name != second.Name {
		t.Fatalf("winner contains unexpected content: %q", name)
	}
}

func TestWriteAllHandlesShortWritesAndRejectsNoProgress(t *testing.T) {
	short := &shortWriter{limit: 2}
	if err := writeAll(short, []byte("abcdef")); err != nil || short.value.String() != "abcdef" {
		t.Fatalf("writeAll short = %q, %v", short.value.String(), err)
	}
	if err := writeAll(zeroWriter{}, []byte("a")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress error = %v", err)
	}
}

type failingSyncFile struct{ *os.File }

func (file *failingSyncFile) Sync() error { return errors.New("injected sync failure") }

type shortWriter struct {
	limit int
	value strings.Builder
}

func (writer *shortWriter) Write(value []byte) (int, error) {
	if len(value) > writer.limit {
		value = value[:writer.limit]
	}
	return writer.value.Write(value)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

func assertNoTemporaryRecords(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), TemporaryRecordSuffix) {
			t.Errorf("temporary record remains: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
