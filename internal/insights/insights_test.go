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

func TestOrchestrationInsightsSuppressSmallVersionedGroupsAndExcludeRejectedTelemetry(t *testing.T) {
	when := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	value := int64(12)
	payload := telemetryPayload("worker:one")
	payload.Observations = []project.TelemetryObservation{{Name: "input_tokens", Value: &value, Source: "provider_reported"}}
	events := []storage.ProjectEventProjection{
		event("created", project.EventWorkPackageCreated, when, "wp-one", "", project.WorkPackageCreatedPayload{DefinitionRecordID: "record"}),
		event("ready", project.EventWorkPackageStateChanged, when.Add(time.Minute), "wp-one", "", project.WorkPackageStateChangedPayload{From: project.WorkPackagePlanned, To: project.WorkPackageReady}),
		event("start", project.EventExecutionStarted, when.Add(2*time.Minute), "wp-one", "execution-one", project.ExecutionStartedPayload{ManifestRecordID: "manifest"}),
		event("done", project.EventExecutionCompleted, when.Add(5*time.Minute), "wp-one", "execution-one", project.ExecutionCompletedPayload{}),
		event("one", project.EventTelemetryRecorded, when.Add(5*time.Minute), "wp-one", "execution-one", payload),
		event("two", project.EventTelemetryRecorded, when.Add(6*time.Minute), "wp-one", "execution-one", payload),
		event("rejected", project.EventTelemetryRecorded, when.Add(7*time.Minute), "wp-one", "execution-one", payload),
	}
	events[len(events)-1].Status = "rejected"
	results, err := calculate("project-insight", events, when)
	if err != nil {
		t.Fatal(err)
	}
	var grouped map[string]any
	for _, result := range results {
		if result.MetricName == "telemetry_versioned_groups" {
			if err := json.Unmarshal(result.ValueJSON, &grouped); err != nil {
				t.Fatal(err)
			}
			if result.Completeness != "partial" || result.Evidence != "weak" {
				t.Fatalf("small group quality=%s/%s", result.Completeness, result.Evidence)
			}
		}
	}
	if grouped["suppressed_small_groups"] != float64(1) {
		t.Fatalf("small group was not suppressed: %#v", grouped)
	}
	if groups, ok := grouped["groups"].(map[string]any); !ok || len(groups) != 0 {
		t.Fatalf("unsafe small group was exposed: %#v", grouped)
	}
	assertInsightValue(t, results, "orchestration_duration_breakdown", map[string]any{"acceptance": map[string]any{"known": false}, "blocked": map[string]any{"known": false}, "execution": map[string]any{"average_milliseconds": float64(180000), "count": float64(1), "known": true, "total_milliseconds": float64(180000)}, "gate": map[string]any{"known": false}, "parallel_overlap": map[string]any{"execution_count": float64(1), "known": true, "overlap_milliseconds": float64(0)}, "preparation": map[string]any{"known": false}, "queue": map[string]any{"average_milliseconds": float64(60000), "count": float64(1), "known": true, "total_milliseconds": float64(60000)}, "review": map[string]any{"known": false}})
}

func TestOrchestrationInsightsExposeOnlySafeVersionedGroups(t *testing.T) {
	when := time.Date(2026, 8, 20, 2, 0, 0, 0, time.UTC)
	value := int64(7)
	events := make([]storage.ProjectEventProjection, 0, 3)
	for index := 0; index < 3; index++ {
		payload := telemetryPayload("worker:one")
		payload.Observations = []project.TelemetryObservation{{Name: "tool_calls", Value: &value, Source: "provider_reported"}}
		events = append(events, event("safe-"+string(rune('a'+index)), project.EventTelemetryRecorded, when.Add(time.Duration(index)*time.Minute), "wp-one", "execution-one", payload))
	}
	results, err := calculate("project-insight", events, when)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.MetricName == "telemetry_versioned_groups" {
			var got map[string]any
			if err := json.Unmarshal(result.ValueJSON, &got); err != nil {
				t.Fatal(err)
			}
			groups := got["groups"].(map[string]any)
			if len(groups) != 1 || got["suppressed_small_groups"] != float64(0) || result.Evidence != "strong" {
				t.Fatalf("safe groups=%#v quality=%s", got, result.Evidence)
			}
			return
		}
	}
	t.Fatal("missing telemetry_versioned_groups")
}

func telemetryPayload(workerID string) project.TelemetrySummaryPayload {
	ref := func(id string) project.RegistryReference {
		return project.RegistryReference{ID: id, Version: 1, Digest: hash(id)}
	}
	binding := func(id string) project.TelemetryBindingReference {
		return project.TelemetryBindingReference{ID: id, Version: 1, Digest: hash(id)}
	}
	return project.TelemetrySummaryPayload{Worker: ref(workerID), Trade: ref("trade:one"), Provider: binding("provider:one"), Model: binding("model:one"), Runtime: binding("runtime:one"), Instruction: binding("instruction:one"), ContextDigest: hash("context"), FinalOutcome: "succeeded"}
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
