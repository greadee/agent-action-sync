package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/resultintake"
)

func TestLocalResultStoreRoundTripsBoundedEnvelope(t *testing.T) {
	store := LocalResultStore{Root: filepath.Join(t.TempDir(), "results")}
	envelope := localResultEnvelope(t)
	_, raw, err := resultintake.BuildEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.PutEnvelope(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetEnvelope(context.Background(), stored.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, canonical, err := resultintake.DecodeEnvelope(got)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ResultID != envelope.ResultID || string(got) != string(canonical) {
		t.Fatalf("stored envelope was not canonical: %s", got)
	}
}

func TestLocalResultStoreRejectsOversizedExistingEnvelope(t *testing.T) {
	store := LocalResultStore{Root: filepath.Join(t.TempDir(), "results")}
	envelope := localResultEnvelope(t)
	path, err := store.envelopePath(envelope.ResultID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, resultintake.MaxEnvelopeBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, raw, err := resultintake.BuildEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutEnvelope(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized existing envelope error = %v", err)
	}
}

func localResultEnvelope(t *testing.T) resultintake.Envelope {
	t.Helper()
	ref := func(id, digit string) executioncontract.BindingReference {
		return executioncontract.BindingReference{ID: id, Version: 1, Digest: strings.Repeat(digit, 64)}
	}
	registryRef := func(id, digit string) project.RegistryReference {
		return project.RegistryReference{ID: id, Version: 1, Digest: strings.Repeat(digit, 64)}
	}
	return resultintake.Envelope{
		ResultID: "result:local-one", IdempotencyKeyDigest: strings.Repeat("1", 64), ProjectID: "project:local", TaskID: "task:local", TaskRevision: 1, GraphRevision: 1,
		WorkPackageID: "work:local", ExecutionID: "execution:local", Contract: executioncontract.ContractReference{ContractID: "contract:local", Version: 1, Digest: strings.Repeat("2", 64)},
		Assignment: resultintake.AssignmentReference{AssignmentID: "assignment:local", Version: 1, Digest: strings.Repeat("3", 64)}, Worker: registryRef("worker:local", "4"), Runtime: ref("runtime:local", "5"), Node: ref("node:local", "6"),
		Provenance:  resultintake.Provenance{Trade: registryRef("trade:local", "7"), Instruction: ref("instruction:local", "8"), ContextDigest: strings.Repeat("9", 64), Provider: ref("provider:local", "a"), Model: ref("model:local", "b")},
		WorkspaceID: "workspace:local", ClaimedOutcome: "succeeded", CreatedAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
	}
}
