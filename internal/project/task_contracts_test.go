package project

import (
	"strings"
	"testing"
	"time"
)

func TestTaskAndDependencyGraphContractsRoundTripAtMinorOne(t *testing.T) {
	when := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	producer := Producer{DeviceID: "device-one", WorkerID: "worker-one"}
	provenance := Provenance{Producer: producer, CreatedAt: when}
	task := TaskRevision{
		RecordHeader: NewRecordHeader(RecordTaskRevision, "task-record-one", testProjectID),
		TaskID:       "task:release", Revision: 1, Objective: "prepare release", Priority: TaskPriorityHigh,
		Risk:          []RiskDimension{{Name: "security", Level: RiskHigh}},
		Resources:     &ResourceConstraints{RequiredCapabilities: []string{"write"}, RequiredTools: []string{"go"}},
		GraphRevision: 1,
		QualityGates:  []QualityGateReference{{GateID: "gate:review", Version: 1, Digest: strings.Repeat("a", 64), Required: true}},
		CreatedAt:     when, Provenance: provenance,
	}
	graph := DependencyGraphRevision{
		RecordHeader: NewRecordHeader(RecordDependencyGraph, "graph-record-one", testProjectID),
		TaskID:       "task:release", TaskRevision: 1, Revision: 1,
		Members:             []DependencyGraphMember{{WorkPackageID: "wp-one", DefinitionRecordID: "wp-record-one", DefinitionDigest: strings.Repeat("b", 64)}},
		DependencySetDigest: strings.Repeat("c", 64), Barriers: []string{"wp-one"}, CreatedAt: when, Provenance: provenance,
	}
	for _, test := range []struct {
		name   string
		record any
		path   string
	}{
		{name: "task", record: task, path: ".agent-project/tasks/task%3arelease/revisions/1/task.json"},
		{name: "graph", record: graph, path: ".agent-project/tasks/task%3arelease/revisions/1/graphs/1.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := MarshalRecord(test.record)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeRecord(raw)
			if err != nil {
				t.Fatal(err)
			}
			path, err := RecordRelativePath(decoded.Value)
			if err != nil || path != test.path {
				t.Fatalf("path=%q err=%v, want %q", path, err, test.path)
			}
		})
	}
}

func TestMinorZeroWorkPackagesRemainReadableAndTaskFieldsRequireMinorOne(t *testing.T) {
	when := time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC)
	legacy := WorkPackageDefinition{
		RecordHeader:  RecordHeader{Schema: SchemaVersion{Family: SchemaFamily, Major: SchemaMajor, Minor: 0}, RecordKind: RecordWorkPackage, RecordID: "legacy-record", ProjectID: testProjectID},
		WorkPackageID: "legacy-work", Objective: "legacy objective", Trade: "engineering", Scope: WorkScope{},
		Deliverables: []string{"output"}, AcceptanceCriteria: []string{"accepted"}, CreatedAt: when,
		Provenance: Provenance{Producer: Producer{DeviceID: "device-one"}, WorkPackageID: "legacy-work", CreatedAt: when},
	}
	raw, err := MarshalRecord(legacy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Value.(*WorkPackageDefinition).TaskID != "" {
		t.Fatal("legacy work package acquired task metadata")
	}

	legacy.TaskID = "task:invalid-old"
	legacy.TaskRevision = 1
	legacy.GraphRevision = 1
	legacy.Priority = TaskPriorityNormal
	if _, err := MarshalRecord(legacy); err == nil || !strings.Contains(err.Error(), "minor version 1") {
		t.Fatalf("task-bound minor-zero error = %v", err)
	}
}

func TestTaskPathKeyEncodingIsPortableAndCollisionFree(t *testing.T) {
	left, err := TaskRevisionRelativePath("task:a:b", 1)
	if err != nil {
		t.Fatal(err)
	}
	right, err := TaskRevisionRelativePath("task:a--b", 1)
	if err != nil {
		t.Fatal(err)
	}
	if left == right || left != ".agent-project/tasks/task%3aa%3ab/revisions/1/task.json" {
		t.Fatalf("task paths left=%q right=%q", left, right)
	}
}

func TestTaskContractsRejectUnsortedConflictingAndInvalidFields(t *testing.T) {
	when := time.Date(2026, time.August, 17, 10, 0, 0, 0, time.UTC)
	base := TaskRevision{
		RecordHeader: NewRecordHeader(RecordTaskRevision, "task-record", testProjectID),
		TaskID:       "task:valid", Revision: 1, Objective: "objective", Priority: TaskPriorityNormal,
		GraphRevision: 1, CreatedAt: when, Provenance: Provenance{Producer: Producer{DeviceID: "device-one"}, CreatedAt: when},
	}
	tests := []struct {
		name   string
		mutate func(*TaskRevision)
	}{
		{name: "unnamespaced task", mutate: func(task *TaskRevision) { task.TaskID = "task-one" }},
		{name: "zero revision", mutate: func(task *TaskRevision) { task.Revision = 0 }},
		{name: "revision predecessor missing", mutate: func(task *TaskRevision) { task.Revision = 2 }},
		{name: "invalid priority", mutate: func(task *TaskRevision) { task.Priority = "urgent" }},
		{name: "unsorted risk", mutate: func(task *TaskRevision) {
			task.Risk = []RiskDimension{{Name: "security", Level: RiskHigh}, {Name: "data", Level: RiskLow}}
		}},
		{name: "unsorted resource", mutate: func(task *TaskRevision) {
			task.Resources = &ResourceConstraints{RequiredTools: []string{"z", "a"}}
		}},
		{name: "invalid gate", mutate: func(task *TaskRevision) {
			task.QualityGates = []QualityGateReference{{GateID: "review", Version: 1, Digest: strings.Repeat("a", 64)}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := base
			test.mutate(&record)
			if _, err := MarshalRecord(record); err == nil {
				t.Fatal("expected invalid task record")
			}
		})
	}

	graph := DependencyGraphRevision{
		RecordHeader: NewRecordHeader(RecordDependencyGraph, "graph-record", testProjectID),
		TaskID:       "task:valid", TaskRevision: 1, Revision: 1,
		Members: []DependencyGraphMember{
			{WorkPackageID: "wp-b", DefinitionRecordID: "record-b", DefinitionDigest: strings.Repeat("b", 64)},
			{WorkPackageID: "wp-a", DefinitionRecordID: "record-a", DefinitionDigest: strings.Repeat("a", 64)},
		},
		DependencySetDigest: strings.Repeat("c", 64), CreatedAt: when,
		Provenance: Provenance{Producer: Producer{DeviceID: "device-one"}, CreatedAt: when},
	}
	if _, err := MarshalRecord(graph); err == nil || !strings.Contains(err.Error(), "sorted") {
		t.Fatalf("unsorted graph error = %v", err)
	}
}
