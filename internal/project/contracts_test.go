package project

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testProjectID = "project-alpha"

var testTime = time.Date(2026, 8, 12, 12, 34, 56, 123000000, time.UTC)

func TestGoldenPortableRecords(t *testing.T) {
	tests := []struct {
		name   string
		kind   RecordKind
		typeOf any
	}{
		{name: "project_manifest.json", kind: RecordProjectManifest, typeOf: &ProjectManifest{}},
		{name: "work_package_definition.json", kind: RecordWorkPackage, typeOf: &WorkPackageDefinition{}},
		{name: "execution_manifest.json", kind: RecordExecution, typeOf: &ExecutionManifest{}},
		{name: "work_event.json", kind: RecordWorkEvent, typeOf: &WorkEvent{}},
		{name: "handoff.json", kind: RecordHandoff, typeOf: &Handoff{}},
		{name: "artifact_manifest.json", kind: RecordArtifact, typeOf: &ArtifactManifest{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", test.name))
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.TrimSpace(raw)
			decoded, err := DecodeRecordForProject(raw, testProjectID)
			if err != nil {
				t.Fatalf("DecodeRecordForProject: %v", err)
			}
			if reflect.TypeOf(decoded.Value) != reflect.TypeOf(test.typeOf) {
				t.Fatalf("decoded type = %T, want %T", decoded.Value, test.typeOf)
			}
			if !bytes.Equal(decoded.Canonical, raw) {
				t.Fatalf("fixture is not canonical\ngot:  %s\nwant: %s", decoded.Canonical, raw)
			}
			reencoded, err := MarshalRecord(decoded.Value)
			if err != nil {
				t.Fatalf("MarshalRecord: %v", err)
			}
			if !bytes.Equal(reencoded, raw) {
				t.Fatalf("round trip mismatch\ngot:  %s\nwant: %s", reencoded, raw)
			}
			var header RecordHeader
			if err := json.Unmarshal(raw, &header); err != nil {
				t.Fatal(err)
			}
			if header.RecordKind != test.kind || decoded.Digest != header.Integrity.Digest {
				t.Fatalf("decoded header/digest = %#v %q", header, decoded.Digest)
			}
		})
	}
}

func TestEveryInitialEventContractRoundTrips(t *testing.T) {
	tests := []struct {
		eventType   EventType
		payload     any
		workID      string
		executionID string
	}{
		{EventProjectRegistered, ProjectRegisteredPayload{ManifestRecordID: "record-project", Authority: Authority{DeviceID: "device-one", ShareID: "share-one"}}, "", ""},
		{EventWorkPackageCreated, WorkPackageCreatedPayload{DefinitionRecordID: "record-work-package"}, "WP-001", ""},
		{EventWorkPackageStateChanged, WorkPackageStateChangedPayload{From: WorkPackageReady, To: WorkPackageInProgress, ReasonCode: "work-started"}, "WP-001", ""},
		{EventExecutionStarted, ExecutionStartedPayload{ManifestRecordID: "record-execution"}, "WP-001", "EX-001"},
		{EventExecutionPaused, ExecutionPausedPayload{ReasonCode: "awaiting-input"}, "WP-001", "EX-001"},
		{EventExecutionFailed, ExecutionFailedPayload{FailureCode: "tests-failed", Summary: "Two validation tests failed."}, "WP-001", "EX-001"},
		{EventExecutionCompleted, ExecutionCompletedPayload{Summary: "Contract implementation completed."}, "WP-001", "EX-001"},
		{EventTestRecorded, TestRecordedPayload{Name: "go test ./internal/project", Outcome: TestPassed, DurationMilliseconds: 1200}, "WP-001", "EX-001"},
		{EventHandoffCreated, HandoffCreatedPayload{HandoffRecordID: "record-handoff"}, "WP-001", "EX-001"},
		{EventReviewRecorded, ReviewRecordedPayload{Outcome: ReviewApproved, ReviewerID: "reviewer-one", Summary: "Contract approved."}, "WP-001", "EX-001"},
		{EventArtifactRecorded, ArtifactRecordedPayload{ArtifactRecordID: "record-artifact", ArtifactID: "artifact-one"}, "WP-001", "EX-001"},
		{EventWorkAccepted, WorkAcceptedPayload{AcceptedBy: "owner-one", Summary: "Acceptance criteria met."}, "WP-001", ""},
	}
	for index, test := range tests {
		t.Run(string(test.eventType), func(t *testing.T) {
			payload, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			event := sampleEvent(test.eventType, payload)
			event.RecordID = "event-" + string(rune('a'+index))
			event.WorkPackageID = test.workID
			event.ExecutionID = test.executionID
			raw, err := MarshalRecord(event)
			if err != nil {
				t.Fatalf("MarshalRecord: %v", err)
			}
			if _, err := DecodeRecord(raw); err != nil {
				t.Fatalf("DecodeRecord: %v", err)
			}
		})
	}
}

func TestCompatibleAdditiveFieldsAndNewMinorVersionArePreserved(t *testing.T) {
	raw := mustMarshalRecord(t, sampleProjectManifest())
	object := decodeObjectForTest(t, raw)
	schema := object["schema"].(map[string]any)
	schema["minor"] = json.Number("1")
	object["future_metadata"] = map[string]any{"classification": "internal", "count": json.Number("2")}
	raw = signObjectForTest(t, object)

	decoded, err := DecodeRecord(raw)
	if err != nil {
		t.Fatalf("DecodeRecord compatible additive field: %v", err)
	}
	if !bytes.Contains(decoded.Canonical, []byte(`"future_metadata"`)) {
		t.Fatalf("canonical record lost additive field: %s", decoded.Canonical)
	}
	manifest := decoded.Value.(*ProjectManifest)
	if manifest.Schema.Minor != 1 {
		t.Fatalf("minor version = %d, want 1", manifest.Schema.Minor)
	}
}

func TestDecodeRejectsMalformedAmbiguousUnsupportedAndOversizedRecords(t *testing.T) {
	valid := mustMarshalRecord(t, sampleProjectManifest())
	unknownKind := decodeObjectForTest(t, valid)
	unknownKind["record_kind"] = "future_record"
	unsupportedMajor := decodeObjectForTest(t, valid)
	unsupportedMajor["schema"].(map[string]any)["major"] = json.Number("2")
	nonCanonicalTimestamp := decodeObjectForTest(t, valid)
	nonCanonicalTimestamp["created_at"] = "2026-08-12T12:34:56.123+00:00"
	nonCanonicalNumber := decodeObjectForTest(t, valid)
	nonCanonicalNumber["future_count"] = json.Number("1.0")

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "empty", raw: nil},
		{name: "malformed", raw: []byte(`{"record_kind":`)},
		{name: "trailing", raw: append(append([]byte{}, valid...), []byte(` {}`)...)},
		{name: "duplicate top level", raw: []byte(`{"record_id":"one","record_id":"two"}`)},
		{name: "duplicate nested payload", raw: duplicatePayloadEvent(t)},
		{name: "unknown record kind", raw: signObjectForTest(t, unknownKind)},
		{name: "unsupported major", raw: signObjectForTest(t, unsupportedMajor)},
		{name: "noncanonical timestamp", raw: signObjectForTest(t, nonCanonicalTimestamp)},
		{name: "noncanonical number", raw: signObjectForTest(t, nonCanonicalNumber)},
		{name: "oversized", raw: bytes.Repeat([]byte("x"), MaxRecordBytes+1)},
		{name: "integrity mismatch", raw: bytes.Replace(valid, []byte("Project Alpha"), []byte("Project Bravo"), 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeRecord(test.raw); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestDecodeRejectsProjectMismatchAndClosedPayloadExtensions(t *testing.T) {
	payload, err := json.Marshal(TestRecordedPayload{Name: "go test ./internal/project", Outcome: TestPassed})
	if err != nil {
		t.Fatal(err)
	}
	event := sampleEvent(EventTestRecorded, payload)
	event.WorkPackageID, event.ExecutionID = "WP-001", "EX-001"
	raw := mustMarshalRecord(t, event)
	if _, err := DecodeRecordForProject(raw, "project-other"); err == nil {
		t.Fatal("expected event/project mismatch rejection")
	}

	closedPayload := json.RawMessage(`{"manifest_record_id":"record-execution","raw_prompt":"secret"}`)
	event = sampleEvent(EventExecutionStarted, closedPayload)
	event.WorkPackageID = "WP-001"
	event.ExecutionID = "EX-001"
	if _, err := MarshalRecord(event); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("closed payload error = %v", err)
	}
}

func TestDecodeRejectsOmittedRequiredZeroValuedFields(t *testing.T) {
	manifest := decodeObjectForTest(t, mustMarshalRecord(t, sampleProjectManifest()))
	delete(manifest["schema"].(map[string]any), "minor")
	workPackage := decodeObjectForTest(t, mustMarshalRecord(t, sampleWorkPackage()))
	delete(workPackage, "review_required")
	artifact := decodeObjectForTest(t, mustMarshalRecord(t, sampleArtifact()))
	delete(artifact, "size")

	for name, object := range map[string]map[string]any{
		"schema minor": manifest, "review required": workPackage, "artifact size": artifact,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRecord(signObjectForTest(t, object)); err == nil {
				t.Fatal("expected missing required field rejection")
			}
		})
	}
}

func TestRecordValidationRejectsInvalidReferencesHashesTimesAndBounds(t *testing.T) {
	tests := []struct {
		name   string
		record any
	}{
		{name: "absolute scope", record: mutateWorkPackage(func(record *WorkPackageDefinition) { record.Scope.Allowed = []string{`C:\private`} })},
		{name: "noncanonical scope", record: mutateWorkPackage(func(record *WorkPackageDefinition) { record.Scope.Allowed = []string{`workspace\internal`} })},
		{name: "self dependency", record: mutateWorkPackage(func(record *WorkPackageDefinition) { record.Dependencies = []string{record.WorkPackageID} })},
		{name: "invalid provenance", record: mutateWorkPackage(func(record *WorkPackageDefinition) { record.Provenance.WorkPackageID = "WP-other" })},
		{name: "future writer minor", record: mutateProject(func(record *ProjectManifest) { record.Schema.Minor = SchemaMinor + 1 })},
		{name: "non UTC timestamp", record: mutateProject(func(record *ProjectManifest) { record.CreatedAt = testTime.In(time.FixedZone("offset", 3600)) })},
		{name: "unsupported event", record: sampleEvent("FUTURE_EVENT", json.RawMessage(`{}`))},
		{name: "execution producer mismatch", record: mutateExecution(func(record *ExecutionManifest) { record.Producer.WorkerID = "worker-other" })},
		{name: "artifact bad hash", record: mutateArtifact(func(record *ArtifactManifest) { record.ContentHash = "abc" })},
		{name: "artifact bad path", record: mutateArtifact(func(record *ArtifactManifest) { record.BlobRelativePath = "artifacts/blobs/sha256/wrong" })},
		{name: "artifact media parameters", record: mutateArtifact(func(record *ArtifactManifest) { record.MediaType = "application/json;charset=utf-8" })},
		{name: "oversized objective", record: mutateWorkPackage(func(record *WorkPackageDefinition) { record.Objective = strings.Repeat("x", MaxTextBytes+1) })},
		{name: "duplicate provenance reference", record: mutateArtifact(func(record *ArtifactManifest) {
			record.Provenance.SourceArtifactIDs = []string{"artifact-source", "artifact-source"}
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := MarshalRecord(test.record); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMarshalRecordIsDeterministic(t *testing.T) {
	record := sampleHandoff()
	first := mustMarshalRecord(t, record)
	second := mustMarshalRecord(t, record)
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical encoding changed\nfirst:  %s\nsecond: %s", first, second)
	}
}

func sampleProjectManifest() ProjectManifest {
	return ProjectManifest{
		RecordHeader: NewRecordHeader(RecordProjectManifest, "record-project", testProjectID),
		Name:         "Project Alpha", Authority: Authority{DeviceID: "device-one", ShareID: "share-one"}, CreatedAt: testTime,
	}
}

func sampleWorkPackage() WorkPackageDefinition {
	return WorkPackageDefinition{
		RecordHeader:  NewRecordHeader(RecordWorkPackage, "record-work-package", testProjectID),
		WorkPackageID: "WP-001", Objective: "Define portable Agent Project contracts.", Trade: "backend-engineer",
		Specialization: "Go", Scope: WorkScope{Allowed: []string{"workspace/internal/project"}, Inspect: []string{"workspace/docs"}, Forbidden: []string{"workspace/internal/api"}},
		Dependencies: []string{"WP-000"}, Deliverables: []string{"Versioned Go contracts", "Golden fixtures"},
		AcceptanceCriteria: []string{"All supported records round-trip", "Malformed records fail closed"}, ReviewRequired: true,
		CreatedAt: testTime, Provenance: sampleProvenance("WP-001", ""),
	}
}

func sampleExecution() ExecutionManifest {
	return ExecutionManifest{
		RecordHeader: NewRecordHeader(RecordExecution, "record-execution", testProjectID),
		ExecutionID:  "EX-001", WorkPackageID: "WP-001", State: ExecutionRunning,
		Producer: sampleProducer(), CreatedAt: testTime, Provenance: sampleProvenance("WP-001", "EX-001"),
	}
}

func sampleEvent(eventType EventType, payload json.RawMessage) WorkEvent {
	return WorkEvent{
		RecordHeader: NewRecordHeader(RecordWorkEvent, "event-one", testProjectID), EventType: eventType,
		OccurredAt: testTime, Producer: sampleProducer(), Payload: payload,
	}
}

func sampleHandoff() Handoff {
	return Handoff{
		RecordHeader: NewRecordHeader(RecordHandoff, "record-handoff", testProjectID),
		HandoffID:    "handoff-one", WorkPackageID: "WP-001", ExecutionID: "EX-001",
		CompletedWork: []string{"Added versioned record contracts."}, ChangedFiles: []string{"workspace/internal/project/models.go"},
		Decisions: []string{"Portable records use canonical JSON."}, Tests: []TestResult{{Name: "go test ./internal/project", Outcome: TestPassed}},
		Limitations: []string{"Filesystem publication is deferred."}, UnresolvedIssues: []string{"Projection schema is not implemented."},
		Assumptions: []string{"One-way source remains authoritative."}, FollowUpWork: []string{"Implement path-safe publication."},
		ReviewRequirements: []string{"Review compatibility policy."}, IntegrationConsiderations: []string{"Keep sync semantic-blind."},
		Confidence: ConfidenceHigh, FailureConditions: []string{"Unknown event types fail closed."}, CreatedAt: testTime,
		Provenance: sampleProvenance("WP-001", "EX-001"),
	}
}

func sampleArtifact() ArtifactManifest {
	hash := strings.Repeat("a", 64)
	return ArtifactManifest{
		RecordHeader: NewRecordHeader(RecordArtifact, "record-artifact", testProjectID), ArtifactID: "artifact-one",
		Name: "contract-report.json", MediaType: "application/json", Size: 128, HashAlgorithm: HashAlgorithmSHA256,
		ContentHash: hash, BlobRelativePath: "artifacts/blobs/sha256/" + hash, CreatedAt: testTime,
		Provenance: sampleProvenance("WP-001", "EX-001"),
	}
}

func sampleProducer() Producer {
	return Producer{WorkerID: "worker-one", DeviceID: "device-one", Trade: "backend-engineer", Specialization: "Go", Provider: "openai", Model: "gpt-5.6-sol", ModelVersion: "2026-08"}
}

func sampleProvenance(workPackageID, executionID string) Provenance {
	return Provenance{Producer: sampleProducer(), WorkPackageID: workPackageID, ExecutionID: executionID, SourceArtifactIDs: []string{"artifact-source"}, ProjectVersion: "revision-one", ContextVersion: "context-one", InstructionVersion: "instruction-one", CreatedAt: testTime}
}

func mustMarshalRecord(t *testing.T, record any) []byte {
	t.Helper()
	raw, err := MarshalRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeObjectForTest(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	object, err := decodeJSONObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func signObjectForTest(t *testing.T, object map[string]any) []byte {
	t.Helper()
	delete(object, "integrity")
	digest, err := digestJSONObject(object)
	if err != nil {
		t.Fatal(err)
	}
	object["integrity"] = map[string]any{"algorithm": HashAlgorithmSHA256, "digest": digest}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func duplicatePayloadEvent(t *testing.T) []byte {
	t.Helper()
	event := sampleEvent(EventExecutionPaused, json.RawMessage(`{"reason_code":"one"}`))
	event.WorkPackageID, event.ExecutionID = "WP-001", "EX-001"
	raw := mustMarshalRecord(t, event)
	return bytes.Replace(raw, []byte(`"payload":{"reason_code":"one"}`), []byte(`"payload":{"reason_code":"one","reason_code":"two"}`), 1)
}

func mutateProject(change func(*ProjectManifest)) ProjectManifest {
	record := sampleProjectManifest()
	change(&record)
	return record
}

func mutateWorkPackage(change func(*WorkPackageDefinition)) WorkPackageDefinition {
	record := sampleWorkPackage()
	change(&record)
	return record
}

func mutateExecution(change func(*ExecutionManifest)) ExecutionManifest {
	record := sampleExecution()
	change(&record)
	return record
}

func mutateArtifact(change func(*ArtifactManifest)) ArtifactManifest {
	record := sampleArtifact()
	change(&record)
	return record
}
