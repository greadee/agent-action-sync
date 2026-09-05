package integrationgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/executioncontract"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/projector"
	"syncgate/internal/resultintake"
	"syncgate/internal/runtimecontract"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	"syncgate/internal/telemetry"
	"syncgate/internal/workhistory"
	"syncgate/internal/workspace"
)

type controlFake struct{ snapshot storage.OrchestrationSnapshot }

func (fake *controlFake) GetAssignment(context.Context, string) (storage.OrchestrationSnapshot, error) {
	out := fake.snapshot
	out.Gates = append([]storage.OrchestrationGateStatus(nil), fake.snapshot.Gates...)
	return out, nil
}
func (fake *controlFake) Transition(_ context.Context, request orchestration.TransitionRequest) (storage.OrchestrationWriteResult, error) {
	fake.snapshot.Assignment.State = request.TargetState
	fake.snapshot.Attempt.State = request.TargetState
	return storage.OrchestrationWriteResult{Snapshot: fake.snapshot}, nil
}
func (fake *controlFake) RecordGateStatus(_ context.Context, request orchestration.GateStatusRequest) (storage.RegistryWriteResult, error) {
	for i := range fake.snapshot.Gates {
		if fake.snapshot.Gates[i].GateID == request.GateID {
			fake.snapshot.Gates[i].Status = request.Status
			fake.snapshot.Gates[i].EvidenceID = request.EvidenceID
			fake.snapshot.Gates[i].ReasonCode = request.ReasonCode
			return storage.RegistryWriteResult{}, nil
		}
	}
	fake.snapshot.Gates = append(fake.snapshot.Gates, storage.OrchestrationGateStatus{AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, GateID: request.GateID, GateVersion: request.GateVersion, GateDigest: request.GateDigest, Status: request.Status, EvidenceID: request.EvidenceID, ReasonCode: request.ReasonCode})
	return storage.RegistryWriteResult{}, nil
}
func (fake *controlFake) RecordOperatorDecision(_ context.Context, request orchestration.OperatorDecisionRequest) (storage.RegistryWriteResult, error) {
	fake.snapshot.Decisions = append(fake.snapshot.Decisions, storage.OrchestrationOperatorDecision{DecisionID: request.DecisionID, AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, Decision: request.Decision, ReasonCode: request.ReasonCode, ActorID: request.ActorID, IdempotencyDigest: request.IdempotencyDigest})
	return storage.RegistryWriteResult{}, nil
}

type runtimeStub struct {
	runtimecontract.Adapter
	result runtimecontract.CollectedResult
}

func (stub runtimeStub) CollectResult(context.Context, runtimecontract.ActionRequest) (runtimecontract.CollectedResult, error) {
	return stub.result, nil
}

type runtimeResolver struct{ adapter runtimecontract.Adapter }

func (value runtimeResolver) ResolveRuntime(executioncontract.BindingReference) (runtimecontract.Adapter, error) {
	return value.adapter, nil
}

type telemetryRecorderFake struct{ calls int }

func (recorder *telemetryRecorderFake) RecordAccepted(_ context.Context, contract executioncontract.Contract, snapshot storage.OrchestrationSnapshot) (project.TelemetrySummaryPayload, error) {
	recorder.calls++
	value := int64(5)
	summary := project.TelemetrySummaryPayload{
		ProjectRevision: contract.ProjectRevision, TaskID: contract.TaskID, TaskRecordID: contract.TaskRecordID, TaskRevision: contract.TaskRevision, TaskDigest: contract.TaskDigest,
		GraphRecordID: contract.GraphRecordID, GraphRevision: contract.GraphRevision, GraphDigest: contract.GraphDigest,
		WorkPackageID: contract.WorkPackageID, WorkPackageRecordID: contract.WorkPackageRecordID, WorkPackageDigest: contract.WorkPackageDigest,
		Contract: project.RegistryReference{ID: contract.ContractID, Version: contract.Version, Digest: contract.Digest}, Trade: contract.Trade, Worker: contract.Worker,
		Instruction: telemetryBinding(contract.Instruction), ContextDigest: contract.ContextDigest, Runtime: telemetryBinding(contract.Runtime), Provider: telemetryBinding(contract.Provider), Model: telemetryBinding(contract.Model), Node: telemetryBinding(contract.Node),
		Observations: []project.TelemetryObservation{{Name: "active_runtime_milliseconds", Value: &value, Source: "locally_measured"}}, FinalOutcome: "succeeded",
	}
	createdAt := snapshot.Attempt.UpdatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	}
	envelope, _, err := telemetry.BuildEnvelope(telemetry.Envelope{TelemetryID: "telemetry:accepted-gate", IdempotencyKeyDigest: testHash([]byte("telemetry-accepted-gate")), Summary: summary, CreatedAt: createdAt})
	return envelope.Summary, err
}

func telemetryBinding(value executioncontract.BindingReference) project.TelemetryBindingReference {
	return project.TelemetryBindingReference{ID: value.ID, Version: value.Version, Digest: value.Digest}
}

type resultSource []byte

func (value resultSource) GetEnvelope(context.Context, string) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

type contentSource map[string]Content

func (value contentSource) GetContent(_ context.Context, ref resultintake.ContentReference) (Content, error) {
	content, ok := value[ref.ID]
	if !ok {
		return Content{}, storage.ErrNotFound
	}
	return content, nil
}

type workspaceFake struct {
	manifest workspace.ChangeManifest
	preview  workspace.IntegrationPreview
}

func (value *workspaceFake) InspectChanges(context.Context, string, executioncontract.Contract) (workspace.ChangeManifest, error) {
	return value.manifest, nil
}
func (value *workspaceFake) PreviewIntegration(context.Context, string, string) (workspace.IntegrationPreview, error) {
	return value.preview, nil
}

type runnerFake struct{ result TestResult }

func (value runnerFake) RunAuthorized(context.Context, executioncontract.Contract, TestCommand) (TestResult, error) {
	return value.result, nil
}

type reviewerFake struct{ result ReviewResult }

func (value reviewerFake) Review(context.Context, executioncontract.Contract, IntegrationSummary) (ReviewResult, error) {
	return value.result, nil
}

type rootsFake string

func (value rootsFake) HistoryRoot(string) (string, error) { return string(value), nil }

type gateFixture struct {
	root     string
	store    *sqlite.Store
	history  *workhistory.Service
	service  Service
	control  *controlFake
	contract executioncontract.Contract
	envelope resultintake.Envelope
	raw      []byte
	request  EvaluateRequest
	work     *workspaceFake
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	when := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := project.BootstrapProject(project.ProjectBootstrapRequest{RootPath: root, ProjectID: "project-gate", Name: "Gate Project", Authority: project.Authority{DeviceID: "device-local", ShareID: "share-gate"}}); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Shares().SaveShare(ctx, storage.Share{ID: core.ShareID("share-gate"), Name: "Gate Project", RootPath: root, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	history, err := workhistory.New(store, &projector.Projector{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	meta := workhistory.Metadata{RootPath: root, IdempotencyKey: "create-work", OccurredAt: when, Producer: project.Producer{WorkerID: "worker:one", DeviceID: "device-local", Trade: "trade:go"}}
	if _, err = history.CreateWorkPackage(ctx, workhistory.CreateWorkPackageRequest{Metadata: meta, WorkPackageID: "wp-one", Objective: "validate result", Trade: "engineering", Scope: project.WorkScope{Allowed: []string{"internal"}}, Deliverables: []string{"code"}, AcceptanceCriteria: []string{"tests pass"}, ReviewRequired: true}); err != nil {
		t.Fatal(err)
	}
	d := strings.Repeat("a", 64)
	gateDigest := strings.Repeat("b", 64)
	contract := executioncontract.Contract{Schema: executioncontract.Schema, ContractID: "contract:one", Version: 1, ProjectID: "project-gate", ProjectRevision: d, TaskID: "task:one", TaskRecordID: "task-record", TaskRevision: 1, TaskDigest: d, GraphRecordID: "graph-record", GraphRevision: 1, GraphDigest: d, WorkPackageID: "wp-one", WorkPackageRecordID: "work-record", WorkPackageDigest: d, ExecutionID: "execution:one", Trade: project.RegistryReference{ID: "trade:go", Version: 1, Digest: d}, Worker: project.RegistryReference{ID: "worker:one", Version: 1, Digest: d}, Instruction: executioncontract.BindingReference{ID: "instruction:one", Version: 1, Digest: d}, ContextDigest: d, Runtime: executioncontract.BindingReference{ID: "runtime:one", Version: 1, Digest: d}, Provider: executioncontract.BindingReference{ID: "provider:one", Version: 1, Digest: d}, Model: executioncontract.BindingReference{ID: "model:one", Version: 1, Digest: d}, Node: executioncontract.BindingReference{ID: "node:one", Version: 1, Digest: d}, ReviewRequired: true, RequiredGates: []executioncontract.GateRequirement{{GateID: "gate:tests", Version: 1, Digest: gateDigest}, {GateID: "gate:human-review", Version: 1, Digest: strings.Repeat("c", 64)}}, CreatedAt: when, CreatedBy: "actor:planner"}
	contract = signContract(t, contract)
	contractJSON, _ := json.Marshal(contract)
	if _, err = store.ExecutionContracts().SaveExecutionContract(ctx, storage.ExecutionContractRecord{ContractID: contract.ContractID, Version: 1, ProjectID: contract.ProjectID, TaskID: contract.TaskID, TaskRevision: 1, GraphRevision: 1, WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID, Digest: contract.Digest, ContractJSON: contractJSON, CreatedAt: when}); err != nil {
		t.Fatal(err)
	}
	assignment := resultintake.AssignmentReference{AssignmentID: "assignment:one", Version: 1, Digest: strings.Repeat("d", 64)}
	artifactBytes := []byte("artifact-safe")
	handoffBytes := []byte(`{"handoff_id":"handoff:one","completed_work":["implemented gate"],"limitations":["password=example-value"],"unresolved_issues":["inspect C:\\private\\worker\\notes.txt"],"confidence":"high"}`)
	artifactRef := resultintake.ContentReference{ID: "artifact:one", Digest: testHash(artifactBytes), Size: int64(len(artifactBytes))}
	handoffRef := resultintake.ContentReference{ID: "handoff:one", Digest: testHash(handoffBytes), Size: int64(len(handoffBytes))}
	envelope, raw, err := resultintake.BuildEnvelope(resultintake.Envelope{ResultID: "result:one", IdempotencyKeyDigest: strings.Repeat("e", 64), ProjectID: contract.ProjectID, TaskID: contract.TaskID, TaskRevision: 1, GraphRevision: 1, WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID, Contract: executioncontract.ContractReference{ContractID: contract.ContractID, Version: 1, Digest: contract.Digest}, Assignment: assignment, Worker: contract.Worker, Runtime: contract.Runtime, Node: contract.Node, Provenance: resultintake.Provenance{Trade: contract.Trade, Instruction: contract.Instruction, ContextDigest: contract.ContextDigest, Provider: contract.Provider, Model: contract.Model}, WorkspaceID: "workspace:one", Artifacts: []resultintake.ContentReference{artifactRef}, Handoff: &handoffRef, ClaimedOutcome: "succeeded", CreatedAt: when.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	manifest := workspace.ChangeManifest{WorkspaceID: "workspace:one", BaseCommit: strings.Repeat("1", 40), HeadCommit: strings.Repeat("2", 40), Files: []workspace.ChangedFile{{RelativePath: "internal/gate.go", Status: "modified", Digest: strings.Repeat("3", 64), Size: 12}}}
	manifest.Digest = testJSONDigest(manifest)
	preview := workspace.IntegrationPreview{BaseCommit: manifest.BaseCommit, CurrentCommit: manifest.BaseCommit, HeadCommit: manifest.HeadCommit}
	preview.Digest = testJSONDigest(preview)
	control := &controlFake{snapshot: storage.OrchestrationSnapshot{Assignment: storage.OrchestrationAssignment{AssignmentID: assignment.AssignmentID, ProjectID: contract.ProjectID, WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID, ContractID: contract.ContractID, ContractVersion: 1, ContractDigest: contract.Digest, CurrentAttemptID: "attempt:one", State: storage.AssignmentCollecting}, Attempt: storage.OrchestrationAttempt{AttemptID: "attempt:one", AssignmentID: assignment.AssignmentID, State: storage.AssignmentCollecting, LeaseGeneration: 1}, Lease: &storage.OrchestrationLease{State: storage.LeaseActive, FencingDigest: strings.Repeat("f", 64)}, Resources: &storage.OrchestrationResourceBinding{RuntimeSessionID: "session:private", WorkspaceID: "workspace:one"}}}
	work := &workspaceFake{manifest: manifest, preview: preview}
	runner := runnerFake{result: TestResult{Outcome: project.TestPassed, ExitCode: 0, DurationMilliseconds: 25, EvidenceID: "evidence:test", EvidenceDigest: strings.Repeat("7", 64)}}
	service := Service{Contracts: store.ExecutionContracts(), Intake: resultintake.Service{Contracts: store.ExecutionContracts(), Intake: store.ResultIntake(), Now: func() time.Time { return when.Add(2 * time.Minute) }}, Control: control, History: history, HistoryRoots: rootsFake(root), Runtimes: runtimeResolver{adapter: runtimeStub{result: runtimecontract.CollectedResult{ResultID: envelope.ResultID, EnvelopeDigest: envelope.Digest, ClaimedOutcome: envelope.ClaimedOutcome}}}, Results: resultSource(raw), Contents: contentSource{artifactRef.ID: {Name: `C:\private\report.json`, MediaType: "application/json", Bytes: artifactBytes}, handoffRef.ID: {Bytes: handoffBytes}}, Workspaces: work, Tests: runner, Reviewer: reviewerFake{result: ReviewResult{Outcome: project.ReviewApproved, ReviewerID: "reviewer:automated", Summary: "no findings"}}, TestPlans: map[string]TestCommand{"gate:tests": {GateID: "gate:tests", GateVersion: 1, GateDigest: gateDigest, CommandID: "command:go-test", CommandDigest: strings.Repeat("8", 64)}}}
	request := EvaluateRequest{AssignmentID: assignment.AssignmentID, AttemptID: "attempt:one", Assignment: assignment, FencingDigest: strings.Repeat("f", 64), ActorID: "actor:authority", CollectDigest: strings.Repeat("9", 64)}
	return &gateFixture{root: root, store: store, history: history, service: service, control: control, contract: contract, envelope: envelope, raw: raw, request: request, work: work}
}

func TestEvaluateApproveReplayAndCleanProjection(t *testing.T) {
	fixture := newGateFixture(t)
	recorder := &telemetryRecorderFake{}
	fixture.service.Telemetry = recorder
	summary, err := fixture.service.Evaluate(context.Background(), fixture.request)
	if !errors.Is(err, ErrHumanRequired) || !summary.ReadyForDecision {
		t.Fatalf("evaluate: summary=%+v err=%v", summary, err)
	}
	raw, _ := json.Marshal(summary)
	for _, secret := range []string{fixture.root, "session:private", "artifact-safe", "example-value", `C:\private`} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("summary leaked %q", secret)
		}
	}
	decision := DecisionRequest{AssignmentID: fixture.request.AssignmentID, AttemptID: fixture.request.AttemptID, FencingDigest: fixture.request.FencingDigest, ActorID: "actor:human", Decision: DecisionApprove, ReasonCode: "reviewed", Summary: summary}
	if _, err = fixture.service.Decide(context.Background(), decision); err != nil {
		t.Fatal(err)
	}
	if fixture.control.snapshot.Attempt.State != storage.AssignmentAccepted {
		t.Fatalf("state=%s", fixture.control.snapshot.Attempt.State)
	}
	if recorder.calls != 1 {
		t.Fatalf("accepted telemetry calls=%d", recorder.calls)
	}
	if _, err = fixture.service.Decide(context.Background(), decision); err != nil {
		t.Fatalf("accepted decision replay: %v", err)
	}
	events, err := fixture.history.ListEvents(context.Background(), storage.ProjectEventQuery{Page: storage.PageRequest{Limit: 100}, ProjectID: fixture.contract.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events.Items {
		seen[event.EventType] = true
		if event.EventType == string(project.EventTestRecorded) {
			var evidence project.TestRecordedPayload
			if err := json.Unmarshal(event.PayloadJSON, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.CommandID != "command:go-test" || evidence.CommandDigest != strings.Repeat("8", 64) || evidence.ExitCode == nil || *evidence.ExitCode != 0 || evidence.DurationMilliseconds != 25 || evidence.EvidenceID != "evidence:test" || evidence.EvidenceDigest != strings.Repeat("7", 64) {
				t.Fatalf("incomplete test evidence: %+v", evidence)
			}
		}
	}
	for _, want := range []string{string(project.EventTestRecorded), string(project.EventHandoffCreated), string(project.EventArtifactRecorded), string(project.EventReviewRecorded), string(project.EventWorkAccepted), string(project.EventTelemetryRecorded)} {
		if !seen[want] {
			t.Fatalf("missing %s", want)
		}
	}
	rebuilt, err := sqlite.Open(filepath.Join(t.TempDir(), "rebuilt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	if err = rebuilt.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = rebuilt.Shares().SaveShare(context.Background(), storage.Share{ID: core.ShareID("share-gate"), Name: "Gate Project", RootPath: fixture.root, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	if _, err = (&projector.Projector{Store: rebuilt}).Ingest(context.Background(), fixture.root, projector.TriggerRebuild); err != nil {
		t.Fatal(err)
	}
	rebuiltEvents, err := rebuilt.ProjectEvents().ListProjectEvents(context.Background(), storage.ProjectEventQuery{Page: storage.PageRequest{Limit: 100}, ProjectID: fixture.contract.ProjectID, EventType: string(project.EventWorkAccepted)})
	if err != nil || len(rebuiltEvents.Items) != 1 {
		t.Fatalf("rebuild accepted events=%d err=%v", len(rebuiltEvents.Items), err)
	}
}

func TestFailuresAndHumanRejectionNeverAccept(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*gateFixture)
		decision bool
	}{
		{name: "failed test", mutate: func(f *gateFixture) {
			f.service.Tests = runnerFake{result: TestResult{Outcome: project.TestFailed, ExitCode: 1, DurationMilliseconds: 3, EvidenceID: "evidence:test", EvidenceDigest: strings.Repeat("7", 64)}}
		}},
		{name: "changes requested", mutate: func(f *gateFixture) {
			f.service.Reviewer = reviewerFake{result: ReviewResult{Outcome: project.ReviewChangesRequested, ReviewerID: "reviewer:automated", Summary: "fix issue"}}
		}},
		{name: "stale base", mutate: func(f *gateFixture) {
			f.work.preview.StaleBase = true
			f.work.preview.Digest = testJSONDigest(f.work.preview)
		}},
		{name: "merge conflict", mutate: func(f *gateFixture) {
			f.work.preview.HasConflicts = true
			f.work.preview.Digest = testJSONDigest(f.work.preview)
		}},
		{name: "unexpected binary", mutate: func(f *gateFixture) {
			f.work.manifest.Files[0].Binary = true
			f.work.manifest.Digest = testJSONDigest(f.work.manifest)
		}},
		{name: "human rejection", decision: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newGateFixture(t)
			if test.mutate != nil {
				test.mutate(f)
			}
			summary, err := f.service.Evaluate(context.Background(), f.request)
			if test.decision {
				if !errors.Is(err, ErrHumanRequired) {
					t.Fatal(err)
				}
				_, err = f.service.Decide(context.Background(), DecisionRequest{AssignmentID: f.request.AssignmentID, AttemptID: f.request.AttemptID, FencingDigest: f.request.FencingDigest, ActorID: "actor:human", Decision: DecisionReject, ReasonCode: "not approved", Summary: summary})
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrGateFailed) {
				t.Fatalf("expected gate failure, got %v", err)
			}
			events, e := f.history.ListEvents(context.Background(), storage.ProjectEventQuery{Page: storage.PageRequest{Limit: 100}, ProjectID: f.contract.ProjectID, EventType: string(project.EventWorkAccepted)})
			if e != nil || len(events.Items) != 0 {
				t.Fatalf("accepted after rejection: %d %v", len(events.Items), e)
			}
		})
	}
}

func TestHumanReviewStopsForDecisionWithoutAutomatedReviewer(t *testing.T) {
	fixture := newGateFixture(t)
	fixture.service.Reviewer = nil
	summary, err := fixture.service.Evaluate(context.Background(), fixture.request)
	if !errors.Is(err, ErrHumanRequired) || !summary.ReadyForDecision || summary.Review != nil {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if fixture.control.snapshot.Attempt.State != storage.AssignmentAwaitingGates {
		t.Fatalf("state=%s", fixture.control.snapshot.Attempt.State)
	}
}

func TestForgedEnvelopeAndStaleAttemptFailClosed(t *testing.T) {
	for _, name := range []string{"forged", "stale"} {
		t.Run(name, func(t *testing.T) {
			f := newGateFixture(t)
			if name == "forged" {
				raw := append([]byte(nil), f.raw...)
				raw[len(raw)-2] ^= 1
				f.service.Results = resultSource(raw)
			} else {
				f.control.snapshot.Attempt.AttemptID = "attempt:new"
			}
			_, err := f.service.Evaluate(context.Background(), f.request)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if f.control.snapshot.Assignment.State == storage.AssignmentAccepted {
				t.Fatal("stale or forged result accepted")
			}
		})
	}
}

func signContract(t *testing.T, value executioncontract.Contract) executioncontract.Contract {
	t.Helper()
	value.Digest = ""
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	value.Digest = testHash(raw)
	return value
}
func testHash(value []byte) string    { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func testJSONDigest(value any) string { raw, _ := json.Marshal(value); return testHash(raw) }
