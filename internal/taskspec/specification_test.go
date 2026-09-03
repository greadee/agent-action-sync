package taskspec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/project"
)

func TestFileStoreIsCanonicalIdempotentAndConflictSafe(t *testing.T) {
	store := FileStore{Root: t.TempDir()}
	raw := marshalSpecification(t, validSpecification())
	created, err := store.Put(context.Background(), raw)
	if err != nil || created.AlreadyPresent || len(created.Digest) != 64 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	replay, err := store.Put(context.Background(), raw)
	if err != nil || !replay.AlreadyPresent || replay.Digest != created.Digest {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	loaded, err := store.Get(context.Background(), created.Specification.SpecificationID, created.Digest)
	if err != nil || loaded.Specification.TaskID != "task:remote-pilot" || loaded.Digest != created.Digest {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	changed := validSpecification()
	changed.Objective = "changed authority under the same immutable identity"
	if _, err := store.Put(context.Background(), marshalSpecification(t, changed)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed specification error=%v", err)
	}
	if _, err := store.Get(context.Background(), changed.SpecificationID, strings.Repeat("f", 64)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed digest error=%v", err)
	}
}

func TestFileStoreRejectsSymlinkBackedRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkedRoot := filepath.Join(t.TempDir(), "linked-task-specifications")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	store := FileStore{Root: linkedRoot}
	if _, err := store.Put(context.Background(), marshalSpecification(t, validSpecification())); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink-backed store error=%v", err)
	}
}

func TestPublishedTaskSpecificationExampleIsValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", "task-specification-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	value, canonical, digest, err := Decode(context.Background(), raw)
	if err != nil || value.SpecificationID != "spec:laptop-pilot" || len(canonical) == 0 || len(digest) != 64 {
		t.Fatalf("value=%+v canonical=%d digest=%q err=%v", value, len(canonical), digest, err)
	}
}

func TestDecodeRejectsCyclesDuplicateEdgesDuplicateKeysAndUnknownFields(t *testing.T) {
	cycle := validSpecification()
	cycle.WorkPackages[0].Dependencies = []string{"work:verify"}
	cycle.WorkPackages[1].Dependencies = []string{"work:implement"}
	if _, _, _, err := Decode(context.Background(), marshalSpecification(t, cycle)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle error=%v", err)
	}
	duplicate := validSpecification()
	duplicate.WorkPackages[1].Dependencies = []string{"work:implement", "work:implement"}
	if _, _, _, err := Decode(context.Background(), marshalSpecification(t, duplicate)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate edge error=%v", err)
	}
	duplicateKey := []byte(`{"schema":"syncgate.task-specification.v1","schema":"syncgate.task-specification.v1"}`)
	if _, _, _, err := Decode(context.Background(), duplicateKey); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate key error=%v", err)
	}
	unknown := append(marshalSpecification(t, validSpecification())[:len(marshalSpecification(t, validSpecification()))-1], []byte(`,"credential":"forbidden"}`)...)
	if _, _, _, err := Decode(context.Background(), unknown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown field error=%v", err)
	}
}

func validSpecification() Specification {
	return Specification{
		Schema: Schema, SpecificationID: "spec:remote-pilot", ProjectID: "project:remote-pilot",
		CreatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		TaskID:    "task:remote-pilot", TaskRevision: 1, GraphRevision: 1,
		Objective: "complete a supervised remote pilot", Priority: project.TaskPriorityHigh,
		Risk:         []project.RiskDimension{{Name: "execution", Level: project.RiskHigh}},
		Resources:    &project.ResourceConstraints{RequiredCapabilities: []string{"write", "inspect"}, RequiredTools: []string{"go"}},
		QualityGates: []project.QualityGateReference{{GateID: "gate:review", Version: 1, Digest: strings.Repeat("a", 64), Required: true}},
		Barriers:     []string{"work:verify"},
		WorkPackages: []WorkPackage{
			{WorkPackageID: "work:implement", Objective: "implement the bounded change", Trade: "engineering", Scope: project.WorkScope{Allowed: []string{"internal"}, Inspect: []string{"docs"}}, Deliverables: []string{"implementation"}, AcceptanceCriteria: []string{"focused tests pass"}},
			{WorkPackageID: "work:verify", Objective: "verify the bounded change", Trade: "quality", Scope: project.WorkScope{Allowed: []string{"tests"}, Inspect: []string{"internal"}}, Dependencies: []string{"work:implement"}, Deliverables: []string{"verification"}, AcceptanceCriteria: []string{"full suite passes"}, ReviewRequired: true},
		},
	}
}

func marshalSpecification(t *testing.T, value Specification) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
