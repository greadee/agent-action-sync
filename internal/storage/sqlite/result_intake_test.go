package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/storage"
)

func TestResultIntakeIsIdempotentAndConflictsOnDigestReuse(t *testing.T) {
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-result")
	record := storage.ResultIntakeRecord{
		ResultID: "result:one", EnvelopeDigest: strings.Repeat("a", 64), IdempotencyKeyDigest: strings.Repeat("e", 64), ProjectID: "project-result", ExecutionID: "execution:one",
		ContractID: "contract:one", ContractVersion: 1, ContractDigest: strings.Repeat("b", 64),
		AssignmentID: "assignment:one", AssignmentDigest: strings.Repeat("c", 64), Decision: "accepted", ReasonCode: "validated_untrusted_result",
		EnvelopeJSON: []byte(`{"schema":"syncgate.result-envelope.v1"}`), DecidedAt: time.Date(2026, time.August, 17, 20, 0, 0, 0, time.UTC), DecidedBy: "actor:intake",
	}
	result, err := store.ResultIntake().SaveResultIntake(context.Background(), record)
	if err != nil || result.AlreadyPresent {
		t.Fatalf("save=%+v err=%v", result, err)
	}
	result, err = store.ResultIntake().SaveResultIntake(context.Background(), record)
	if err != nil || !result.AlreadyPresent {
		t.Fatalf("replay=%+v err=%v", result, err)
	}
	changed := record
	changed.EnvelopeDigest = strings.Repeat("d", 64)
	if _, err := store.ResultIntake().SaveResultIntake(context.Background(), changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	otherID := record
	otherID.ResultID = "result:other"
	otherID.EnvelopeDigest = strings.Repeat("f", 64)
	if _, err := store.ResultIntake().SaveResultIntake(context.Background(), otherID); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("idempotency conflict error=%v", err)
	}
	loaded, err := store.ResultIntake().GetResultIntake(context.Background(), record.ResultID)
	if err != nil || loaded.EnvelopeDigest != record.EnvelopeDigest || loaded.Decision != record.Decision {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestResultIntakeSurvivesRestartAndPreservesReplayDecision(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "result-intake.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	saveProjectRegistration(t, store, "project-result-restart")
	record := storage.ResultIntakeRecord{
		ResultID: "result:restart", EnvelopeDigest: strings.Repeat("a", 64), IdempotencyKeyDigest: strings.Repeat("b", 64), ProjectID: "project-result-restart", ExecutionID: "execution:restart",
		ContractID: "contract:restart", ContractVersion: 1, ContractDigest: strings.Repeat("c", 64), AssignmentID: "assignment:restart", AssignmentDigest: strings.Repeat("d", 64),
		Decision: "accepted", ReasonCode: "validated_untrusted_result", EnvelopeJSON: []byte(`{"schema":"syncgate.result-envelope.v1"}`), DecidedAt: time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC), DecidedBy: "actor:intake",
	}
	if _, err := store.ResultIntake().SaveResultIntake(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.ResultIntake().GetResultIntake(ctx, record.ResultID)
	if err != nil || loaded.Decision != record.Decision || loaded.EnvelopeDigest != record.EnvelopeDigest {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if replay, err := store.ResultIntake().SaveResultIntake(ctx, record); err != nil || !replay.AlreadyPresent {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}
