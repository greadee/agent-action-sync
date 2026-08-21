package insights

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/project"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestRebuildConvergesForLateTiedAndPendingEvents(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "insights.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: core.ShareID("share-insight"), Name: "insight", RootPath: t.TempDir(), Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	registration := storage.ProjectRegistration{ProjectID: "project-insight", ShareID: "share-insight", RootPath: "safe-root", Name: "insight", AuthorityDeviceID: "device-one", ManifestRecordID: "manifest-one", ManifestRecordHash: hash("manifest"), ManifestPath: ".agent-project/manifest.json", RegisteredAt: time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)}
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, registration); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	saveEvent(t, store, event("created", project.EventWorkPackageCreated, when, "wp-one", "", project.WorkPackageCreatedPayload{DefinitionRecordID: "wp-record"}))
	saveEvent(t, store, event("ready", project.EventWorkPackageStateChanged, when, "wp-one", "", project.WorkPackageStateChangedPayload{From: project.WorkPackagePlanned, To: project.WorkPackageReady}))
	saveEvent(t, store, event("active", project.EventWorkPackageStateChanged, when.Add(time.Minute), "wp-one", "", project.WorkPackageStateChangedPayload{From: project.WorkPackageReady, To: project.WorkPackageInProgress}))
	saveEvent(t, store, event("start", project.EventExecutionStarted, when.Add(2*time.Minute), "wp-one", "exec-one", project.ExecutionStartedPayload{ManifestRecordID: "exec-record"}))
	saveEvent(t, store, event("complete", project.EventExecutionCompleted, when.Add(7*time.Minute), "wp-one", "exec-one", project.ExecutionCompletedPayload{}))
	saveEvent(t, store, event("pending", project.EventTestRecorded, when.Add(3*time.Minute), "wp-one", "exec-one", project.TestRecordedPayload{Name: "ignored", Outcome: project.TestPassed}), "pending")

	calculator := &Calculator{Store: store, Now: func() time.Time { return when.Add(time.Hour) }}
	first, err := calculator.Rebuild(ctx, "project-insight")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(Definitions) {
		t.Fatalf("definitions=%d results=%d", len(Definitions), len(first))
	}
	assertInsightValue(t, first, "work_package_state_counts", map[string]any{"accepted": float64(0), "active": float64(1), "blocked": float64(0), "completed": float64(1), "failed": float64(0)})
	assertQuality(t, first, "projection_freshness", "partial", "weak")

	// A late event has an earlier timestamp than the last seen record; a full
	// deterministic replacement must converge without relying on arrival order.
	saveEvent(t, store, event("test-late", project.EventTestRecorded, when.Add(3*time.Minute), "wp-one", "exec-one", project.TestRecordedPayload{Name: "unit", Outcome: project.TestPassed}))
	second, err := calculator.Rebuild(ctx, "project-insight")
	if err != nil {
		t.Fatal(err)
	}
	third, err := calculator.Rebuild(ctx, "project-insight")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, third) {
		t.Fatal("incremental and clean rebuild outputs differ")
	}
	assertInsightValue(t, second, "test_outcomes", map[string]any{"failed": float64(0), "passed": float64(1), "skipped": float64(0)})
	assertQuality(t, second, "test_outcomes", "partial", "weak")
}

func TestDefinitionsAreVersionedAndEmptySamplesAreExplicit(t *testing.T) {
	seen := map[string]bool{}
	for _, definition := range Definitions {
		if definition.Name == "" || definition.Version < 1 || seen[definition.Name] {
			t.Fatalf("invalid definition: %+v", definition)
		}
		seen[definition.Name] = true
	}
	// Empty input has no hidden zero-success assumption.
	results, err := calculate("project-empty", nil, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Completeness != "insufficient" || result.Evidence != "none" {
			t.Fatalf("%s quality=%s/%s", result.MetricName, result.Completeness, result.Evidence)
		}
	}
}

func TestTelemetryInsightsKeepMissingMeasurementsUnknown(t *testing.T) {
	value := int64(42)
	when := time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)
	results, err := calculate("project-insight", []storage.ProjectEventProjection{
		event("telemetry-one", project.EventTelemetryRecorded, when, "wp-one", "exec-one", project.TelemetrySummaryPayload{FinalOutcome: "succeeded", Observations: []project.TelemetryObservation{{Name: "input_tokens", Value: &value, Source: "provider_reported"}, {Name: "provider_cost_micros", Source: "provider_reported"}}}),
	}, when)
	if err != nil {
		t.Fatal(err)
	}
	var resources map[string]any
	for _, result := range results {
		if result.MetricName == "telemetry_resource_observations" {
			if err := json.Unmarshal(result.ValueJSON, &resources); err != nil {
				t.Fatal(err)
			}
			if result.Completeness != "partial" {
				t.Fatalf("quality=%s", result.Completeness)
			}
		}
	}
	cost, ok := resources["provider_cost_micros"].(map[string]any)
	if !ok || cost["known"] != false || cost["unknown_count"] != float64(1) {
		t.Fatalf("missing cost was treated as a value: %#v", resources["provider_cost_micros"])
	}
}

func event(id string, kind project.EventType, when time.Time, workPackageID, executionID string, payload any) storage.ProjectEventProjection {
	raw, _ := json.Marshal(payload)
	return storage.ProjectEventProjection{ProjectID: "project-insight", EventID: id, RecordHash: hash(id), EventType: string(kind), OccurredAt: when, WorkPackageID: workPackageID, ExecutionID: executionID, ProducerWorkerID: "worker-one", ProducerDeviceID: "device-one", ProducerProvider: "provider-one", ProducerModel: "model-one", Status: "accepted", RecordPath: ".agent-project/history/events/2026/08/14/" + id + ".json", PayloadJSON: raw}
}
func saveEvent(t *testing.T, store storage.Store, value storage.ProjectEventProjection, status ...string) {
	t.Helper()
	if len(status) > 0 {
		value.Status = status[0]
	}
	if _, err := store.ProjectEvents().SaveProjectEvent(context.Background(), value); err != nil {
		t.Fatal(err)
	}
}
func hash(value string) string {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}
func assertInsightValue(t *testing.T, values []storage.ProjectInsightProjection, name string, want any) {
	t.Helper()
	for _, value := range values {
		if value.MetricName == name {
			var got any
			if err := json.Unmarshal(value.ValueJSON, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s got=%#v want=%#v", name, got, want)
			}
			return
		}
	}
	t.Fatalf("missing %s", name)
}
func assertQuality(t *testing.T, values []storage.ProjectInsightProjection, name, completeness, evidence string) {
	t.Helper()
	for _, value := range values {
		if value.MetricName == name {
			if value.Completeness != completeness || value.Evidence != evidence {
				t.Fatalf("%s quality=%s/%s", name, value.Completeness, value.Evidence)
			}
			return
		}
	}
	t.Fatalf("missing %s", name)
}
