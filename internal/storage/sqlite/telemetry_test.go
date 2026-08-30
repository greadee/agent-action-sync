package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"syncgate/internal/storage"
)

func TestExecutionTelemetryIsIdempotentConflictSafeAndListable(t *testing.T) {
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-telemetry")
	record := storage.ExecutionTelemetryRecord{TelemetryID: "telemetry:one", TelemetryDigest: strings.Repeat("a", 64), IdempotencyKeyDigest: strings.Repeat("b", 64), ProjectID: "project-telemetry", ExecutionID: "execution:one", ContractID: "contract:one", ContractVersion: 1, ContractDigest: strings.Repeat("c", 64), FinalOutcome: "succeeded", SummaryJSON: []byte(`{"schema":"syncgate.execution-telemetry.v1"}`), CreatedAt: time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)}
	result, err := store.ExecutionTelemetry().SaveExecutionTelemetry(context.Background(), record)
	if err != nil || result.AlreadyPresent {
		t.Fatalf("save=%+v err=%v", result, err)
	}
	result, err = store.ExecutionTelemetry().SaveExecutionTelemetry(context.Background(), record)
	if err != nil || !result.AlreadyPresent {
		t.Fatalf("replay=%+v err=%v", result, err)
	}
	changed := record
	changed.TelemetryDigest = strings.Repeat("d", 64)
	if _, err := store.ExecutionTelemetry().SaveExecutionTelemetry(context.Background(), changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("digest conflict=%v", err)
	}
	other := record
	other.TelemetryID = "telemetry:other"
	other.TelemetryDigest = strings.Repeat("e", 64)
	if _, err := store.ExecutionTelemetry().SaveExecutionTelemetry(context.Background(), other); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("idempotency conflict=%v", err)
	}
	loaded, err := store.ExecutionTelemetry().GetExecutionTelemetry(context.Background(), record.TelemetryID)
	if err != nil || loaded.TelemetryDigest != record.TelemetryDigest || loaded.CreatedAt != record.CreatedAt {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	listed, err := store.ExecutionTelemetry().ListExecutionTelemetry(context.Background(), record.ProjectID, record.ExecutionID)
	if err != nil || len(listed) != 1 || listed[0].TelemetryID != record.TelemetryID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	latestStore, ok := store.ExecutionTelemetry().(storage.LatestExecutionTelemetryStore)
	if !ok {
		t.Fatal("telemetry store does not expose bounded latest telemetry")
	}
	latest, err := latestStore.ListLatestExecutionTelemetry(context.Background(), record.ProjectID, record.ExecutionID, 1)
	if err != nil || len(latest) != 1 || latest[0].TelemetryID != record.TelemetryID {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}
