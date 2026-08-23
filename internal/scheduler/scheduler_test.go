package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"syncgate/internal/computenode"
	"syncgate/internal/contextcompiler"
	"syncgate/internal/dispatchbinding"
	"syncgate/internal/executioncontract"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/runtimecontract"
	"syncgate/internal/storage"
	"syncgate/internal/workspace"
)

func TestSchedulerOverlapsIndependentNodesAndWaitsForCanonicalAcceptance(t *testing.T) {
	fixture := newSchedulerFixture(t, 2, 2)
	requests := fixture.requests(t, map[string][]string{"work:a": nil, "work:b": nil, "work:c": {"work:a", "work:b"}}, nil)
	if _, _, err := executioncontract.Build(requests[0].Binding.Contract); err != nil {
		t.Fatalf("fixture contract: %v", err)
	}
	report, err := fixture.scheduler.Cycle(context.Background(), requests)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 2 || countOutcome(report, OutcomeDispatched) != 2 || itemFor(report, "work:c").Outcome != OutcomeWaiting {
		t.Fatalf("initial cycle = %+v", report)
	}
	if fixture.control.state("assignment:a") != storage.AssignmentRunning || fixture.control.state("assignment:b") != storage.AssignmentRunning {
		t.Fatalf("independent states = a:%s b:%s", fixture.control.state("assignment:a"), fixture.control.state("assignment:b"))
	}

	fixture.complete(t, "assignment:a")
	fixture.complete(t, "assignment:b")
	report, err = fixture.scheduler.Cycle(context.Background(), requests)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 0 || fixture.control.state("assignment:a") != storage.AssignmentCollecting || itemFor(report, "work:c").Outcome != OutcomeWaiting {
		t.Fatalf("runtime completion cycle = %+v", report)
	}
	if observation := mustNodeObservation(t, fixture.node); observation.ActiveLeases != 0 {
		t.Fatalf("completed work retained %d compute leases", observation.ActiveLeases)
	}
	if fixture.control.heartbeatCount() != 2 {
		t.Fatalf("control heartbeats = %d, want 2", fixture.control.heartbeatCount())
	}

	accepted := append(acceptedEvents("work:a", fixture.now), acceptedEvents("work:b", fixture.now)...)
	for index := range requests {
		requests[index].Events = append(createdEvents(requests[index].Graph, fixture.now), accepted...)
	}
	report, err = fixture.scheduler.Cycle(context.Background(), requests)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 || itemFor(report, "work:c").Outcome != OutcomeDispatched || fixture.control.state("assignment:c") != storage.AssignmentRunning {
		t.Fatalf("accepted dependency cycle = %+v", report)
	}
}

func TestSchedulerBlocksCanonicalFailedDependency(t *testing.T) {
	fixture := newSchedulerFixture(t, 2, 2)
	requests := fixture.requests(t, map[string][]string{"work:a": nil, "work:b": {"work:a"}}, nil)
	for index := range requests {
		requests[index].Events = append(createdEvents(requests[index].Graph, fixture.now), failedEvents("work:a", fixture.now)...)
	}
	report, err := fixture.scheduler.Cycle(context.Background(), requests)
	if err != nil {
		t.Fatal(err)
	}
	if itemFor(report, "work:b").Outcome != OutcomeBlocked || !contains(itemFor(report, "work:b").Reasons, orchestration.ReasonDependencyFailed) || report.Active != 0 {
		t.Fatalf("failed dependency cycle = %+v", report)
	}
}

func TestSchedulerPriorityStableOrderAndLeaseLimit(t *testing.T) {
	t.Run("priority then stable identifier avoids starvation", func(t *testing.T) {
		fixture := newSchedulerFixture(t, 1, 2)
		priorities := map[string]project.TaskPriority{"work:a": project.TaskPriorityLow, "work:b": project.TaskPriorityCritical}
		requests := fixture.requests(t, map[string][]string{"work:a": nil, "work:b": nil}, priorities)
		report, err := fixture.scheduler.Cycle(context.Background(), requests)
		if err != nil {
			t.Fatal(err)
		}
		if itemFor(report, "work:b").Outcome != OutcomeDispatched || itemFor(report, "work:a").Reasons[0] != "concurrency_limit_reached" {
			t.Fatalf("priority cycle = %+v", report)
		}
		fixture.complete(t, "assignment:b")
		report, err = fixture.scheduler.Cycle(context.Background(), requests)
		if err != nil || itemFor(report, "work:a").Outcome != OutcomeDispatched {
			t.Fatalf("starvation cycle = %+v, err=%v", report, err)
		}
	})

	t.Run("provider lease capacity blocks the second assignment", func(t *testing.T) {
		fixture := newSchedulerFixture(t, 2, 1)
		requests := fixture.requests(t, map[string][]string{"work:a": nil, "work:b": nil}, nil)
		report, err := fixture.scheduler.Cycle(context.Background(), requests)
		if err != nil {
			t.Fatal(err)
		}
		if report.Active != 1 || itemFor(report, "work:a").Outcome != OutcomeDispatched || !contains(itemFor(report, "work:b").Reasons, "lease_limit_reached") {
			t.Fatalf("lease-limited cycle = %+v", report)
		}
	})

	t.Run("stable identifier breaks equal-priority ties", func(t *testing.T) {
		fixture := newSchedulerFixture(t, 1, 2)
		requests := fixture.requests(t, map[string][]string{"work:a": nil, "work:b": nil}, nil)
		requests[0], requests[1] = requests[1], requests[0]
		report, err := fixture.scheduler.Cycle(context.Background(), requests)
		if err != nil || itemFor(report, "work:a").Outcome != OutcomeDispatched {
			t.Fatalf("stable-order cycle = %+v, err=%v", report, err)
		}
	})
}

func TestSchedulerSerializesDuplicateDispatch(t *testing.T) {
	fixture := newSchedulerFixture(t, 2, 2)
	request := fixture.requests(t, map[string][]string{"work:a": nil}, nil)
	if report, err := fixture.scheduler.Cycle(context.Background(), []DispatchRequest{request[0], request[0]}); err != nil || countOutcome(report, OutcomeDispatched) != 1 || countOutcome(report, OutcomeDuplicate) != 1 {
		t.Fatalf("same-cycle duplicate report=%+v err=%v", report, err)
	}
	const callers = 8
	var wait sync.WaitGroup
	errorsSeen := make(chan error, callers)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := fixture.scheduler.Cycle(context.Background(), request)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fixture.control.plans != 1 || len(fixture.scheduler.active) != 1 {
		t.Fatalf("plans=%d active=%d", fixture.control.plans, len(fixture.scheduler.active))
	}
}

func TestSchedulerPauseCancelAndShutdownPreserveWorkspaces(t *testing.T) {
	fixture := newSchedulerFixture(t, 2, 2)
	requests := fixture.requests(t, map[string][]string{"work:a": nil, "work:b": nil, "work:c": nil}, nil)
	if report, err := fixture.scheduler.Cycle(context.Background(), requests[:2]); err != nil || report.Active != 2 {
		t.Fatalf("dispatch=%+v err=%v", report, err)
	}
	if err := fixture.scheduler.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.control.state("assignment:a") != storage.AssignmentPaused || fixture.control.state("assignment:b") != storage.AssignmentPaused {
		t.Fatal("pause did not reach both active runtimes")
	}
	if report, err := fixture.scheduler.Cycle(context.Background(), requests[2:]); err != nil || !contains(itemFor(report, "work:c").Reasons, "scheduler_paused") {
		t.Fatalf("paused cycle=%+v err=%v", report, err)
	}
	if err := fixture.scheduler.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.scheduler.Pause(context.Background()); err != nil {
		t.Fatalf("second pause: %v", err)
	}
	if err := fixture.scheduler.Resume(context.Background()); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if err := fixture.scheduler.Cancel(context.Background(), "assignment:a"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.scheduler.Cancel(context.Background(), "assignment:a"); err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if fixture.workspaces.releases != 0 || fixture.control.state("assignment:a") != storage.AssignmentCanceled {
		t.Fatalf("cancel releases=%d state=%s", fixture.workspaces.releases, fixture.control.state("assignment:a"))
	}
	if err := fixture.scheduler.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fixture.scheduler.active) != 0 || fixture.control.state("assignment:b") != storage.AssignmentCanceled || fixture.workspaces.releases != 0 {
		t.Fatalf("shutdown active=%d b=%s releases=%d", len(fixture.scheduler.active), fixture.control.state("assignment:b"), fixture.workspaces.releases)
	}
}

func TestSchedulerReportsStableRuntimeAndBudgetBlocks(t *testing.T) {
	t.Run("runtime unavailable", func(t *testing.T) {
		fixture := newSchedulerFixture(t, 2, 2)
		fixture.runtimeUnavailable = true
		requests := fixture.requests(t, map[string][]string{"work:a": nil}, nil)
		report, err := fixture.scheduler.Cycle(context.Background(), requests)
		if err != nil || !contains(itemFor(report, "work:a").Reasons, "runtime_unavailable") || fixture.control.plans != 0 {
			t.Fatalf("report=%+v plans=%d err=%v", report, fixture.control.plans, err)
		}
	})

	t.Run("budget exhausted", func(t *testing.T) {
		fixture := newSchedulerFixture(t, 2, 2)
		requests := fixture.requests(t, map[string][]string{"work:a": nil}, nil)
		requests[0].Selection.RequestedBudget.MaxTokens = requests[0].Selection.Policy.MaximumBudget.MaxTokens + 1
		report, err := fixture.scheduler.Cycle(context.Background(), requests)
		if err != nil || !contains(itemFor(report, "work:a").Reasons, "budget_tokens_exceeded") || fixture.control.plans != 0 {
			t.Fatalf("report=%+v plans=%d err=%v", report, fixture.control.plans, err)
		}
	})
}

func TestSchedulerReconcilesBeforeDispatchAndDoesNotReplayUncertainRuntime(t *testing.T) {
	fixture := newSchedulerFixtureWithoutStart(t, 2, 2)
	fixture.control.snapshots["assignment:recovered"] = storage.OrchestrationSnapshot{
		Assignment: storage.OrchestrationAssignment{AssignmentID: "assignment:recovered"},
		Attempt:    storage.OrchestrationAttempt{AttemptID: "attempt:recovered", State: storage.AssignmentRunning, RecoveryDisposition: storage.RecoveryNeedsOperator},
		Lease:      &storage.OrchestrationLease{OwnerNodeID: fixture.nodeRef.ID, OwnerRuntime: fixture.runtimeRef.ID},
		Resources:  &storage.OrchestrationResourceBinding{WorkspaceID: "workspace:missing", RuntimeSessionID: "runtime-session:missing"},
	}
	if err := fixture.scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.control.reconciles != 1 || len(fixture.scheduler.Recovery()) != 1 || fixture.scheduler.Recovery()[0].Reason != "workspace_reconciliation_failed" {
		t.Fatalf("reconciles=%d recovery=%+v", fixture.control.reconciles, fixture.scheduler.Recovery())
	}
	if fixture.control.plans != 0 {
		t.Fatal("recovery replayed an uncertain assignment")
	}
}

type schedulerFixture struct {
	now                time.Time
	scheduler          *Scheduler
	control            *fakeControl
	runtime            *runtimecontract.DeterministicFake
	node               *computenode.DeterministicFake
	workspaces         *trackingWorkspace
	runtimeRef         executioncontract.BindingReference
	nodeRef            executioncontract.BindingReference
	runtimeUnavailable bool
}

func newSchedulerFixture(t *testing.T, maxConcurrent int, nodeLeaseLimit int64) *schedulerFixture {
	t.Helper()
	fixture := newSchedulerFixtureWithoutStart(t, maxConcurrent, nodeLeaseLimit)
	if err := fixture.scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newSchedulerFixtureWithoutStart(t *testing.T, maxConcurrent int, nodeLeaseLimit int64) *schedulerFixture {
	t.Helper()
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	runtimeRef := reference("runtime:local", "3")
	node, err := computenode.BuildDefinition(computenode.Definition{
		NodeID: "node:local", Version: 1, Lifecycle: computenode.LifecycleActive, OS: "windows", Architecture: "amd64",
		Capacity: computenode.Capacity{CPUMillis: 4000, MemoryBytes: 8 << 30, DiskBytes: 100 << 30}, RepositoryAccess: computenode.RepositoryWrite,
		Runtimes: []executioncontract.BindingReference{runtimeRef}, MaxActiveLeases: nodeLeaseLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeRef := executioncontract.BindingReference{ID: node.NodeID, Version: node.Version, Digest: node.DefinitionDigest}
	nodeFake, err := computenode.NewDeterministicFake(computenode.FakeConfig{Definition: node, Observation: computenode.Observation{
		Node: nodeRef, Health: computenode.HealthHealthy, Available: node.Capacity, AvailableRuntimes: []executioncontract.BindingReference{runtimeRef}, ObservedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	runtimeFake, err := runtimecontract.NewDeterministicFake(runtimecontract.FakeConfig{Runtime: runtimeRef, Node: nodeRef, Capabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	control := &fakeControl{snapshots: map[string]storage.OrchestrationSnapshot{}}
	workspaces := &trackingWorkspace{Manager: workspace.NewDeterministicFake(func(request workspace.PreflightRequest) (workspace.PreflightReport, error) {
		return workspace.PreflightReport{WorkspaceID: request.WorkspaceID, BranchName: request.BranchName, Ready: true}, nil
	})}
	fixture := &schedulerFixture{now: now, control: control, runtime: runtimeFake, node: nodeFake, workspaces: workspaces, runtimeRef: runtimeRef, nodeRef: nodeRef}
	scheduler, err := New(Config{
		Binder: &fakeBinder{control: control, now: now}, Control: control, Workspace: workspaces, ActorID: "scheduler:test", MaxConcurrent: maxConcurrent,
		ResolveRuntime: func(_ context.Context, id string) (runtimecontract.Adapter, error) {
			if fixture.runtimeUnavailable || id != runtimeRef.ID {
				return nil, errors.New("runtime unavailable")
			}
			return runtimeFake, nil
		},
		ResolveNode: func(_ context.Context, id string) (computenode.Provider, error) {
			if id != nodeRef.ID {
				return nil, errors.New("node unavailable")
			}
			return nodeFake, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.scheduler = scheduler
	return fixture
}

func (fixture *schedulerFixture) requests(t *testing.T, dependencies map[string][]string, priorities map[string]project.TaskPriority) []DispatchRequest {
	t.Helper()
	graph := schedulerGraph(t, dependencies, priorities)
	events := createdEvents(graph, fixture.now)
	ids := make([]string, 0, len(dependencies))
	for id := range dependencies {
		ids = append(ids, id)
	}
	sortStrings(ids)
	requests := make([]DispatchRequest, 0, len(ids))
	for _, id := range ids {
		definition := graph.Definitions[id]
		suffix := strings.TrimPrefix(id, "work:")
		trade := *definition.Record.TradeReference
		workerRef := project.RegistryReference{ID: "worker:go", Version: 1, Digest: repeated("b")}
		worker := storage.WorkerProfile{WorkerID: workerRef.ID, Version: 1, Lifecycle: storage.RegistryActive, TradeID: trade.ID, TradeVersion: trade.Version, ContentHash: workerRef.Digest, RuntimeID: fixture.runtimeRef.ID, RuntimeVersion: fixture.runtimeRef.Version, Provider: "provider", Model: "model", ModelVersion: "2026-08", CapabilityTags: []string{"cap:go"}}
		tradeRecord := storage.TradeDefinition{TradeID: trade.ID, Version: trade.Version, Lifecycle: storage.RegistryActive, ContentHash: trade.Digest, RequiredCapabilities: []string{"cap:go"}}
		budget := executioncontract.BudgetLimits{MaxTokens: 100, MaxCostMicros: 1000, MaxWallClockSeconds: 60, MaxRetries: 1, MaxToolCalls: 10, MaxConcurrentWorkers: 2}
		layers := policyLayers(budget)
		contractRequest := executioncontract.BuildRequest{
			ContractID: "contract:" + suffix, Version: 1, Task: graph.Task, TaskDigest: repeated("7"), Graph: graph.Revision, GraphDigest: repeated("6"),
			WorkPackage: definition.Record, WorkPackageDigest: definition.RecordDigest, ExecutionID: "execution:" + suffix, ProjectRevision: repeated("e"),
			Trade: trade, Worker: workerRef, WorkerProfile: worker, Instruction: reference("instruction:go", "1"), ContextDigest: repeated("f"),
			Runtime: fixture.runtimeRef, Provider: reference("provider:local", "4"), Model: reference("model:test", "5"), Node: fixture.nodeRef,
			RequestedCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite}, PolicyLayers: layers, RequestedBudget: budget,
			NotBefore: fixture.now, Deadline: fixture.now.Add(30 * time.Second), CreatedAt: fixture.now, CreatedBy: "actor:scheduler",
		}
		selectionBudget := orchestration.DispatchBudget{MaxTokens: 100, MaxCostMicros: 1000, MaxWallClock: time.Minute, MaxConcurrentWorker: 1}
		selection := orchestration.SelectionRequest{
			Task:        storage.ProjectTaskProjection{ProjectID: graph.Task.ProjectID, TaskID: graph.Task.TaskID, TaskRevision: 1, GraphRevision: 1, State: string(orchestration.ReadinessReady)},
			Node:        storage.ProjectTaskNodeProjection{ProjectID: graph.Task.ProjectID, TaskID: graph.Task.TaskID, TaskRevision: 1, GraphRevision: 1, WorkPackageID: id, DefinitionRecordID: definition.Record.RecordID, DefinitionHash: definition.RecordDigest, Readiness: string(orchestration.ReadinessReady)},
			WorkPackage: definition.Record, WorkPackageDigest: definition.RecordDigest, Risk: orchestration.RiskModerate, RequestedBudget: selectionBudget,
			RequiredRuntime: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite},
			Policy:          orchestration.SelectionPolicy{AllowedCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite}, MaximumRisk: orchestration.RiskHigh, MaximumBudget: orchestration.DispatchBudget{MaxTokens: 200, MaxCostMicros: 2000, MaxWallClock: 2 * time.Minute, MaxConcurrentWorker: 2}},
			Candidates:      []orchestration.Candidate{{Worker: worker, Trade: tradeRecord, Runtime: fixture.runtimeRef, RuntimeAvailable: true, RuntimeCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite}, Node: mustNodeDefinition(t, fixture.node), Observation: mustNodeObservation(t, fixture.node)}},
			Now:             fixture.now,
		}
		plan := orchestration.PlanRequest{
			AssignmentID: "assignment:" + suffix, AttemptID: "attempt:" + suffix, ProjectID: graph.Task.ProjectID, TaskID: graph.Task.TaskID, TaskRevision: 1, GraphRevision: 1,
			WorkPackageID: id, ExecutionID: "execution:" + suffix, WorkerID: worker.WorkerID, NodeID: fixture.nodeRef.ID,
			IdempotencyDigest: digest("plan", id), OperationID: "operation:plan:" + suffix, OperationDigest: digest("plan-operation", id), AuditID: "audit:plan:" + suffix, ActorID: "scheduler:test",
		}
		requests = append(requests, DispatchRequest{
			Graph: graph, Events: events, Selection: selection,
			Binding:           dispatchbinding.BindAndPlanRequest{Selection: orchestration.SelectionResult{}, Context: contextcompiler.Request{ProjectID: graph.Task.ProjectID, WorkPackageID: id, TradeReference: trade}, Contract: contractRequest, Plan: plan},
			Workspace:         workspace.PreflightRequest{ProjectSyncRoot: `C:\project`, RepositoryRoot: `C:\project\repo`, WorktreeBase: `C:\worktrees`, BaseCommit: repeated("9"), RequiredDiskBytes: 1, AvailableDiskBytes: 2},
			InstructionBundle: []byte("perform bounded work"),
		})
	}
	return requests
}

func (fixture *schedulerFixture) complete(t *testing.T, assignmentID string) {
	t.Helper()
	attempt := fixture.scheduler.active[assignmentID]
	if attempt == nil {
		t.Fatalf("assignment %s is not active", assignmentID)
	}
	if _, err := fixture.runtime.Complete(attempt.session.SessionID, runtimecontract.CollectedResult{ResultID: "result:" + strings.TrimPrefix(assignmentID, "assignment:"), EnvelopeDigest: repeated("8"), ClaimedOutcome: "succeeded"}, true); err != nil {
		t.Fatal(err)
	}
}

type fakeBinder struct {
	control *fakeControl
	now     time.Time
}

func (binder *fakeBinder) BindAndPlan(_ context.Context, request dispatchbinding.BindAndPlanRequest) (dispatchbinding.BindAndPlanResult, error) {
	request.Contract.ContextDigest = repeated("f")
	contract, _, err := executioncontract.Build(request.Contract)
	if err != nil {
		return dispatchbinding.BindAndPlanResult{}, err
	}
	binder.control.plan(request.Plan, contract, binder.now)
	return dispatchbinding.BindAndPlanResult{Context: contextcompiler.Result{Bytes: []byte("bounded context")}, Contract: contract, Planned: storage.OrchestrationWriteResult{Snapshot: binder.control.snapshot(request.Plan.AssignmentID)}}, nil
}

type fakeControl struct {
	mu         sync.Mutex
	snapshots  map[string]storage.OrchestrationSnapshot
	plans      int
	reconciles int
	heartbeats int
}

func (control *fakeControl) plan(request orchestration.PlanRequest, contract executioncontract.Contract, now time.Time) {
	control.mu.Lock()
	defer control.mu.Unlock()
	if _, exists := control.snapshots[request.AssignmentID]; exists {
		return
	}
	control.plans++
	control.snapshots[request.AssignmentID] = storage.OrchestrationSnapshot{
		Assignment: storage.OrchestrationAssignment{AssignmentID: request.AssignmentID, WorkPackageID: request.WorkPackageID, State: storage.AssignmentPlanned, CurrentAttemptID: request.AttemptID, ContractID: contract.ContractID, ContractVersion: contract.Version, ContractDigest: contract.Digest},
		Attempt:    storage.OrchestrationAttempt{AttemptID: request.AttemptID, AssignmentID: request.AssignmentID, WorkPackageID: request.WorkPackageID, State: storage.AssignmentPlanned, CreatedAt: now, UpdatedAt: now},
	}
}

func (control *fakeControl) Claim(_ context.Context, request orchestration.ClaimRequest) (storage.OrchestrationWriteResult, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	snapshot, ok := control.snapshots[request.AssignmentID]
	if !ok || snapshot.Attempt.State != storage.AssignmentPlanned {
		return storage.OrchestrationWriteResult{}, storage.ErrConflict
	}
	snapshot.Assignment.State, snapshot.Attempt.State, snapshot.Attempt.LeaseGeneration = storage.AssignmentLeased, storage.AssignmentLeased, 1
	snapshot.Lease = &storage.OrchestrationLease{LeaseID: request.LeaseID, AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, Generation: 1, OwnerNodeID: request.OwnerNodeID, OwnerRuntime: request.OwnerRuntimeID, FencingDigest: request.FencingDigest, State: storage.LeaseActive}
	control.snapshots[request.AssignmentID] = snapshot
	return storage.OrchestrationWriteResult{Snapshot: snapshot}, nil
}

func (control *fakeControl) BindResources(_ context.Context, request orchestration.BindResourcesRequest) (storage.OrchestrationWriteResult, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	snapshot := control.snapshots[request.AssignmentID]
	if snapshot.Attempt.State != storage.AssignmentLeased || request.LeaseGeneration != snapshot.Attempt.LeaseGeneration {
		return storage.OrchestrationWriteResult{}, storage.ErrConflict
	}
	snapshot.Assignment.State, snapshot.Attempt.State = storage.AssignmentPreparing, storage.AssignmentPreparing
	snapshot.Resources = &storage.OrchestrationResourceBinding{AttemptID: request.AttemptID, LeaseGeneration: request.LeaseGeneration, RuntimeSessionID: request.RuntimeSessionID, RuntimeResumeKey: request.RuntimeResumeKey, WorkspaceID: request.WorkspaceID, WorkspaceGeneration: request.WorkspaceGeneration}
	control.snapshots[request.AssignmentID] = snapshot
	return storage.OrchestrationWriteResult{Snapshot: snapshot}, nil
}

func (control *fakeControl) Transition(_ context.Context, request orchestration.TransitionRequest) (storage.OrchestrationWriteResult, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	snapshot, ok := control.snapshots[request.AssignmentID]
	if !ok || snapshot.Attempt.LeaseGeneration != request.LeaseGeneration || snapshot.Lease == nil || snapshot.Lease.FencingDigest != request.FencingDigest {
		return storage.OrchestrationWriteResult{}, storage.ErrConflict
	}
	if err := orchestration.ReduceAssignmentState(snapshot.Attempt.State, request.TargetState); err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	snapshot.Assignment.State, snapshot.Attempt.State = request.TargetState, request.TargetState
	snapshot.Assignment.FailureCode, snapshot.Attempt.FailureCode = request.FailureCode, request.FailureCode
	control.snapshots[request.AssignmentID] = snapshot
	return storage.OrchestrationWriteResult{Snapshot: snapshot}, nil
}

func (control *fakeControl) Heartbeat(_ context.Context, request orchestration.HeartbeatRequest) (storage.OrchestrationSnapshot, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	snapshot, ok := control.snapshots[request.AssignmentID]
	if !ok || snapshot.Lease == nil || snapshot.Lease.LeaseID != request.LeaseID || snapshot.Attempt.LeaseGeneration != request.LeaseGeneration || snapshot.Lease.FencingDigest != request.FencingDigest {
		return storage.OrchestrationSnapshot{}, storage.ErrConflict
	}
	control.heartbeats++
	return snapshot, nil
}

func (control *fakeControl) Reconcile(_ context.Context, _ string) ([]storage.OrchestrationSnapshot, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.reconciles++
	values := make([]storage.OrchestrationSnapshot, 0, len(control.snapshots))
	for _, snapshot := range control.snapshots {
		values = append(values, snapshot)
	}
	return values, nil
}

func (control *fakeControl) state(assignmentID string) storage.AssignmentState {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.snapshots[assignmentID].Attempt.State
}

func (control *fakeControl) snapshot(assignmentID string) storage.OrchestrationSnapshot {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.snapshots[assignmentID]
}

func (control *fakeControl) heartbeatCount() int {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.heartbeats
}

type trackingWorkspace struct {
	workspace.Manager
	releases int
}

func (manager *trackingWorkspace) Release(ctx context.Context, claim workspace.CleanupClaim) error {
	manager.releases++
	return manager.Manager.Release(ctx, claim)
}

func schedulerGraph(t *testing.T, dependencies map[string][]string, priorities map[string]project.TaskPriority) orchestration.Graph {
	t.Helper()
	ids := make([]string, 0, len(dependencies))
	for id := range dependencies {
		ids = append(ids, id)
	}
	sortStrings(ids)
	trade := project.RegistryReference{ID: "trade:go", Version: 1, Digest: repeated("a")}
	task := project.TaskRevision{RecordHeader: project.NewRecordHeader(project.RecordTaskRevision, "record:task", "project:graph"), TaskID: "task:graph", Revision: 1, GraphRevision: 1, Priority: project.TaskPriorityNormal}
	task.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: repeated("7")}
	definitions := make(map[string]orchestration.WorkPackageDefinition, len(ids))
	members := make([]project.DependencyGraphMember, 0, len(ids))
	for index, id := range ids {
		digestValue := strings.Repeat(fmt.Sprintf("%x", index+1), 64)
		header := project.NewRecordHeader(project.RecordWorkPackage, "record:"+strings.TrimPrefix(id, "work:"), task.ProjectID)
		header.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: digestValue}
		work := project.WorkPackageDefinition{RecordHeader: header, WorkPackageID: id, TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1, Priority: priorities[id], Trade: "go", TradeReference: &trade, Scope: project.WorkScope{Allowed: []string{"src"}, Inspect: []string{"docs"}}, Dependencies: append([]string(nil), dependencies[id]...), Deliverables: []string{"code"}, AcceptanceCriteria: []string{"tests pass"}}
		definitions[id] = orchestration.WorkPackageDefinition{Record: work, RecordDigest: digestValue}
		members = append(members, project.DependencyGraphMember{WorkPackageID: id, DefinitionRecordID: work.RecordID, DefinitionDigest: digestValue})
	}
	graph := orchestration.Graph{Task: task, Revision: project.DependencyGraphRevision{RecordHeader: project.NewRecordHeader(project.RecordDependencyGraph, "record:graph", task.ProjectID), TaskID: task.TaskID, TaskRevision: 1, Revision: 1, Members: members}, Definitions: definitions}
	graph.Revision.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: repeated("6")}
	value, err := orchestration.DependencySetDigest(context.Background(), definitions)
	if err != nil {
		t.Fatal(err)
	}
	graph.Revision.DependencySetDigest = value
	return graph
}

func createdEvents(graph orchestration.Graph, when time.Time) []orchestration.Event {
	result := make([]orchestration.Event, 0, len(graph.Revision.Members))
	for index, member := range graph.Revision.Members {
		result = append(result, orchestration.Event{Record: project.WorkEvent{RecordHeader: project.RecordHeader{RecordID: fmt.Sprintf("create:%02d", index)}, EventType: project.EventWorkPackageCreated, WorkPackageID: member.WorkPackageID, OccurredAt: when}, RecordDigest: repeated("a")})
	}
	return result
}

func acceptedEvents(workPackageID string, when time.Time) []orchestration.Event {
	states := []project.WorkPackageState{project.WorkPackagePlanned, project.WorkPackageReady, project.WorkPackageInProgress, project.WorkPackageReview}
	result := make([]orchestration.Event, 0, 5)
	for index := 0; index+1 < len(states); index++ {
		payload, _ := json.Marshal(project.WorkPackageStateChangedPayload{From: states[index], To: states[index+1]})
		result = append(result, orchestration.Event{Record: project.WorkEvent{RecordHeader: project.RecordHeader{RecordID: fmt.Sprintf("transition:%s:%d", workPackageID, index)}, EventType: project.EventWorkPackageStateChanged, WorkPackageID: workPackageID, OccurredAt: when, Payload: payload}, RecordDigest: repeated("b")})
	}
	review, _ := json.Marshal(project.ReviewRecordedPayload{Outcome: project.ReviewApproved, ReviewerID: "reviewer:test"})
	result = append(result, orchestration.Event{Record: project.WorkEvent{RecordHeader: project.RecordHeader{RecordID: "review:" + workPackageID}, EventType: project.EventReviewRecorded, WorkPackageID: workPackageID, OccurredAt: when, Payload: review}, RecordDigest: repeated("c")})
	result = append(result, orchestration.Event{Record: project.WorkEvent{RecordHeader: project.RecordHeader{RecordID: "accept:" + workPackageID}, EventType: project.EventWorkAccepted, WorkPackageID: workPackageID, OccurredAt: when}, RecordDigest: repeated("d")})
	return result
}

func failedEvents(workPackageID string, when time.Time) []orchestration.Event {
	states := []project.WorkPackageState{project.WorkPackagePlanned, project.WorkPackageReady, project.WorkPackageInProgress, project.WorkPackageFailed}
	result := make([]orchestration.Event, 0, len(states)-1)
	for index := 0; index+1 < len(states); index++ {
		payload, _ := json.Marshal(project.WorkPackageStateChangedPayload{From: states[index], To: states[index+1]})
		result = append(result, orchestration.Event{Record: project.WorkEvent{RecordHeader: project.RecordHeader{RecordID: fmt.Sprintf("failure:%s:%d", workPackageID, index)}, EventType: project.EventWorkPackageStateChanged, WorkPackageID: workPackageID, OccurredAt: when, Payload: payload}, RecordDigest: repeated("e")})
	}
	return result
}

func policyLayers(budget executioncontract.BudgetLimits) []executioncontract.PolicyLayer {
	result := make([]executioncontract.PolicyLayer, 0, 7)
	for _, name := range []string{"project", "runtime", "task", "trade", "user", "work_package", "worker"} {
		result = append(result, executioncontract.PolicyLayer{Name: name, AllowedCapabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite}, InspectPaths: []string{"docs", "src"}, WritePaths: []string{"src"}, BudgetCeiling: budget})
	}
	return result
}

func mustNodeDefinition(t *testing.T, node computenode.Provider) computenode.Definition {
	t.Helper()
	value, err := node.Definition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustNodeObservation(t *testing.T, node computenode.Provider) computenode.Observation {
	t.Helper()
	value, err := node.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func reference(id, seed string) executioncontract.BindingReference {
	return executioncontract.BindingReference{ID: id, Version: 1, Digest: repeated(seed)}
}

func repeated(seed string) string { return strings.Repeat(seed, 64) }

func sortStrings(values []string) {
	for left := 0; left < len(values); left++ {
		for right := left + 1; right < len(values); right++ {
			if values[right] < values[left] {
				values[left], values[right] = values[right], values[left]
			}
		}
	}
}

func countOutcome(report CycleReport, outcome Outcome) int {
	count := 0
	for _, item := range report.Items {
		if item.Outcome == outcome {
			count++
		}
	}
	return count
}

func itemFor(report CycleReport, workPackageID string) ItemReport {
	for index := len(report.Items) - 1; index >= 0; index-- {
		if report.Items[index].WorkPackageID == workPackageID {
			return report.Items[index]
		}
	}
	return ItemReport{}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
