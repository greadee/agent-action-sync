package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/codexruntime"
	"syncgate/internal/contextcompiler"
	"syncgate/internal/core"
	"syncgate/internal/dispatchbinding"
	"syncgate/internal/executioncontract"
	"syncgate/internal/identity"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/runtimecontract"
	"syncgate/internal/scheduler"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	"syncgate/internal/workspace"
)

func TestBuildLocalOrchestrationStartsPausedWithRealNodeOwnedComponents(t *testing.T) {
	ctx := context.Background()
	manager, _, projectRoot, runtimePath, now := executionFixture(t)
	if _, err := manager.MarkDisposable(ctx, projectRoot, MarkDisposableConfirmation); err != nil {
		t.Fatal(err)
	}
	preflight, err := manager.Preflight(ctx, "codex", "gpt-5.6-sol", runtimePath, projectRoot, DisposableConfirmation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(ctx, preflight.Receipt, EnableExecutionConfirmation); err != nil {
		t.Fatal(err)
	}
	cfg, roots, err := (SettingsManager{Base: manager.Base}).Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(roots.DataDir, "slice-3-composition.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	deviceIdentity, err := identity.GenerateDeviceIdentity(bytes.NewReader(bytes.Repeat([]byte{0x35}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	composition, err := BuildLocalOrchestration(ctx, LocalOrchestrationOptions{
		Base: manager.Base, Config: cfg, Store: store, Identity: deviceIdentity, Credentials: manager.Credentials,
		Now: func() time.Time { return now.UTC() },
		Observe: func(_ string, ceiling int, observedAt time.Time) (MachineResources, error) {
			return MachineResources{CPUMillis: 4000, LogicalCPUs: 4, DiskTotalBytes: 100 << 20, DiskAvailableBytes: 80 << 20, ConfiguredConcurrency: ceiling, ObservedAt: observedAt, ExpiresAt: observedAt.Add(30 * time.Second)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if composition.Runtime == nil || composition.Workspace == nil || composition.Node == nil || composition.Results.Root == "" || composition.Administration == nil {
		t.Fatalf("composition is incomplete: %+v", composition)
	}
	if err := composition.Scheduler.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composition.Scheduler.Shutdown(context.Background()) })
	status := composition.Scheduler.Status()
	if !status.Started || !status.Paused || status.Active != 0 {
		t.Fatalf("safe startup status = %+v", status)
	}
	definition, err := composition.Node.Definition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if definition.MaxActiveLeases != int64(cfg.Node.Execution.MaxConcurrent) || composition.ModelRef.ID != "model:gpt-5.6-sol" {
		t.Fatalf("node/model binding mismatch: node=%+v model=%+v", definition, composition.ModelRef)
	}
}

func TestLocalOrchestrationRunsDeterministicPilotToCollecting(t *testing.T) {
	ctx := context.Background()
	manager, _, projectRoot, runtimePath, _ := executionFixture(t)
	pilotNow := time.Now().UTC()
	if _, err := manager.MarkDisposable(ctx, projectRoot, MarkDisposableConfirmation); err != nil {
		t.Fatal(err)
	}
	preflight, err := manager.Preflight(ctx, "codex", "gpt-5.6-sol", runtimePath, projectRoot, DisposableConfirmation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(ctx, preflight.Receipt, EnableExecutionConfirmation); err != nil {
		t.Fatal(err)
	}
	cfg, roots, err := (SettingsManager{Base: manager.Base}).Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(roots.DataDir, "slice-3-pilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	deviceIdentity, err := identity.GenerateDeviceIdentity(bytes.NewReader(bytes.Repeat([]byte{0x46}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	shareID := core.ShareID("share-local-pilot")
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: shareID, Name: "Local Pilot", RootPath: projectRoot, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, storage.ProjectRegistration{ProjectID: "project:local-pilot", ShareID: shareID, RootPath: projectRoot, Name: "Local Pilot", AuthorityDeviceID: deviceIdentity.DeviceID, ManifestRecordID: "manifest:local-pilot", ManifestRecordHash: strings.Repeat("f", 64), ManifestPath: ".agent-project/manifest.json", RegisteredAt: pilotNow}); err != nil {
		t.Fatal(err)
	}
	control := orchestration.ControlService{Store: store.OrchestrationControl(), Now: func() time.Time { return pilotNow }}
	executor := &pilotExecutor{completed: make(chan struct{})}
	composition, err := BuildLocalOrchestration(ctx, LocalOrchestrationOptions{
		Base: manager.Base, Config: cfg, Store: store, Identity: deviceIdentity, Credentials: manager.Credentials,
		Now: func() time.Time { return pilotNow }, Executor: executor,
		Binder: pilotBinder{contracts: executioncontract.Service{Store: store.ExecutionContracts()}, control: control, now: func() time.Time { return pilotNow }},
		Observe: func(_ string, ceiling int, observedAt time.Time) (MachineResources, error) {
			return MachineResources{CPUMillis: 4000, LogicalCPUs: 4, DiskTotalBytes: 100 << 20, DiskAvailableBytes: 80 << 20, ConfiguredConcurrency: ceiling, ObservedAt: observedAt, ExpiresAt: observedAt.Add(30 * time.Second)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := composition.Scheduler.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composition.Scheduler.Shutdown(context.Background()) })
	request := localPilotRequest(t, composition, projectRoot, gitTestCommand(t, projectRoot, "rev-parse", "HEAD"), pilotNow)
	contractCheck := request.Binding.Contract
	contractCheck.ContextDigest = strings.Repeat("e", 64)
	if _, _, err := executioncontract.Build(contractCheck); err != nil {
		t.Fatalf("pilot contract fixture is invalid: %v", err)
	}
	paused, err := composition.Scheduler.Cycle(ctx, []scheduler.DispatchRequest{request})
	if err != nil || len(paused.Items) != 1 || paused.Items[0].Reasons[0] != "scheduler_paused" {
		t.Fatalf("paused pilot cycle = %+v, err=%v", paused, err)
	}
	if err := composition.Scheduler.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	dispatched, err := composition.Scheduler.Cycle(ctx, []scheduler.DispatchRequest{request})
	if err != nil || len(dispatched.Items) != 1 || dispatched.Items[0].Outcome != scheduler.OutcomeDispatched {
		t.Fatalf("pilot dispatch = %+v, err=%v", dispatched, err)
	}
	select {
	case <-executor.completed:
	case <-time.After(5 * time.Second):
		snapshot, snapshotErr := control.GetAssignment(ctx, "assignment:local-pilot")
		var observation runtimecontract.Observation
		var observationErr error
		if snapshotErr == nil && snapshot.Resources != nil {
			observation, observationErr = composition.Runtime.Observe(ctx, snapshot.Resources.RuntimeSessionID)
		}
		t.Fatalf("deterministic runtime did not complete: attempt=%+v snapshot_err=%v observation=%+v observation_err=%v", snapshot.Attempt, snapshotErr, observation, observationErr)
	}
	collected, err := composition.Scheduler.Cycle(ctx, nil)
	if err != nil || len(collected.Items) != 1 || collected.Items[0].Outcome != scheduler.OutcomeCollecting {
		t.Fatalf("pilot collection = %+v, err=%v", collected, err)
	}
	snapshot, err := control.GetAssignment(ctx, "assignment:local-pilot")
	if err != nil || snapshot.Attempt.State != storage.AssignmentCollecting || snapshot.Resources == nil {
		t.Fatalf("pilot snapshot = %+v, err=%v", snapshot, err)
	}
	worktreePath, err := composition.Workspace.ResolveLocalPath(ctx, snapshot.Resources.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(worktreePath, "src", "pilot.txt")); err != nil || string(raw) != "deterministic desktop pilot\n" {
		t.Fatalf("pilot worktree output = %q, err=%v", raw, err)
	}
}

type pilotBinder struct {
	contracts executioncontract.Service
	control   orchestration.ControlService
	now       func() time.Time
}

func (binder pilotBinder) BindAndPlan(ctx context.Context, request dispatchbinding.BindAndPlanRequest) (dispatchbinding.BindAndPlanResult, error) {
	bundle := contextcompiler.Bundle{Manifest: contextcompiler.Manifest{CompilerVersion: contextcompiler.CompilerVersion, ProjectID: request.Contract.Task.ProjectID, WorkPackageID: request.Contract.WorkPackage.WorkPackageID, TradeReference: request.Contract.Trade}, Briefing: "# Deterministic desktop pilot\n"}
	unsigned, _ := json.Marshal(bundle)
	bundle.Manifest.ContextDigest = pilotDigest(unsigned)
	bundleBytes, _ := json.Marshal(bundle)
	request.Contract.ContextDigest = bundle.Manifest.ContextDigest
	contract, _, err := binder.contracts.Create(ctx, request.Contract)
	if err != nil {
		return dispatchbinding.BindAndPlanResult{}, err
	}
	request.Plan.ContractID, request.Plan.ContractVersion, request.Plan.ContractDigest = contract.ContractID, contract.Version, contract.Digest
	bindingJSON := []byte(`{"schema":"syncgate.desktop-pilot-binding.v1"}`)
	request.Plan.Binding = storage.OrchestrationAttemptBinding{
		AttemptID: request.Plan.AttemptID, ContractID: contract.ContractID, ContractVersion: contract.Version, ContractDigest: contract.Digest,
		ContextDigest: contract.ContextDigest, ContextCompilerVersion: contextcompiler.CompilerVersion, BindingDigest: pilotDigest(bindingJSON), BindingJSON: bindingJSON, CreatedAt: binder.now(),
	}
	planned, err := binder.control.Plan(ctx, request.Plan)
	if err != nil {
		return dispatchbinding.BindAndPlanResult{}, err
	}
	return dispatchbinding.BindAndPlanResult{Context: contextcompiler.Result{Bundle: bundle, Bytes: bundleBytes}, Contract: contract, Planned: planned}, nil
}

type pilotExecutor struct{ completed chan struct{} }

func (executor *pilotExecutor) Run(_ context.Context, invocation codexruntime.Invocation, _ codexruntime.EventSink) (codexruntime.Execution, error) {
	var input struct {
		Contract        executioncontract.ContractReference `json:"contract"`
		SessionID       string                              `json:"session_id"`
		AttemptID       string                              `json:"attempt_id"`
		LeaseGeneration int64                               `json:"lease_generation"`
		FencingDigest   string                              `json:"fencing_digest"`
	}
	if err := json.Unmarshal(invocation.Stdin, &input); err != nil {
		return codexruntime.Execution{}, err
	}
	if err := os.MkdirAll(filepath.Join(invocation.Directory, "src"), 0o700); err != nil {
		return codexruntime.Execution{}, err
	}
	if err := os.WriteFile(filepath.Join(invocation.Directory, "src", "pilot.txt"), []byte("deterministic desktop pilot\n"), 0o600); err != nil {
		return codexruntime.Execution{}, err
	}
	final, err := json.Marshal(struct {
		ResultID        string `json:"result_id"`
		EnvelopeDigest  string `json:"envelope_digest"`
		ClaimedOutcome  string `json:"claimed_outcome"`
		ContractDigest  string `json:"contract_digest"`
		SessionID       string `json:"session_id"`
		AttemptID       string `json:"attempt_id"`
		LeaseGeneration int64  `json:"lease_generation"`
		FencingDigest   string `json:"fencing_digest"`
	}{"result:local-pilot", strings.Repeat("9", 64), "succeeded", input.Contract.Digest, input.SessionID, input.AttemptID, input.LeaseGeneration, input.FencingDigest})
	close(executor.completed)
	return codexruntime.Execution{FinalMessage: final}, err
}

func localPilotRequest(t *testing.T, composition *LocalOrchestration, projectRoot, head string, now time.Time) scheduler.DispatchRequest {
	t.Helper()
	trade := project.RegistryReference{ID: "trade:go", Version: 1, Digest: strings.Repeat("a", 64)}
	task := project.TaskRevision{RecordHeader: project.NewRecordHeader(project.RecordTaskRevision, "record:local-task", "project:local-pilot"), TaskID: "task:local-pilot", Revision: 1, GraphRevision: 1, Priority: project.TaskPriorityNormal}
	task.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: strings.Repeat("7", 64)}
	work := project.WorkPackageDefinition{RecordHeader: project.NewRecordHeader(project.RecordWorkPackage, "record:local-work", task.ProjectID), WorkPackageID: "work:local-pilot", TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1, Priority: project.TaskPriorityNormal, Trade: "go", TradeReference: &trade, Scope: project.WorkScope{Allowed: []string{"."}, Inspect: []string{"."}}, Deliverables: []string{"pilot output"}, AcceptanceCriteria: []string{"runtime reaches collecting"}}
	work.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: strings.Repeat("4", 64)}
	revision := project.DependencyGraphRevision{RecordHeader: project.NewRecordHeader(project.RecordDependencyGraph, "record:local-graph", task.ProjectID), TaskID: task.TaskID, TaskRevision: 1, Revision: 1, Members: []project.DependencyGraphMember{{WorkPackageID: work.WorkPackageID, DefinitionRecordID: work.RecordID, DefinitionDigest: work.Integrity.Digest}}}
	revision.Integrity = project.Integrity{Algorithm: project.HashAlgorithmSHA256, Digest: strings.Repeat("6", 64)}
	definitions := map[string]orchestration.WorkPackageDefinition{work.WorkPackageID: {Record: work, RecordDigest: work.Integrity.Digest}}
	setDigest, err := orchestration.DependencySetDigest(context.Background(), definitions)
	if err != nil {
		t.Fatal(err)
	}
	revision.DependencySetDigest = setDigest
	graph := orchestration.Graph{Task: task, Revision: revision, Definitions: definitions}
	events := []orchestration.Event{{Record: project.WorkEvent{RecordHeader: project.RecordHeader{RecordID: "event:local-created"}, EventType: project.EventWorkPackageCreated, WorkPackageID: work.WorkPackageID, OccurredAt: now}, RecordDigest: strings.Repeat("b", 64)}}
	nodeDefinition, err := composition.Node.Definition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	nodeObservation, err := composition.Node.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	workerRef := project.RegistryReference{ID: "worker:local", Version: 1, Digest: strings.Repeat("c", 64)}
	worker := storage.WorkerProfile{WorkerID: workerRef.ID, Version: 1, Lifecycle: storage.RegistryActive, TradeID: trade.ID, TradeVersion: trade.Version, ContentHash: workerRef.Digest, RuntimeID: composition.RuntimeRef.ID, RuntimeVersion: composition.RuntimeRef.Version, Provider: "codex", Model: "gpt-5.6-sol", ModelVersion: "2026-08", CapabilityTags: []string{"cap:go"}}
	tradeDefinition := storage.TradeDefinition{TradeID: trade.ID, Version: trade.Version, Lifecycle: storage.RegistryActive, ContentHash: trade.Digest, RequiredCapabilities: []string{"cap:go"}}
	budget := executioncontract.BudgetLimits{MaxTokens: 100, MaxCostMicros: 1000, MaxWallClockSeconds: 30, MaxRetries: 1, MaxToolCalls: 10, MaxConcurrentWorkers: 1}
	runtimeCapabilities := []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite, executioncontract.CapabilityShell}
	policies := make([]executioncontract.PolicyLayer, 0, 7)
	for _, name := range []string{"project", "runtime", "task", "trade", "user", "work_package", "worker"} {
		policies = append(policies, executioncontract.PolicyLayer{Name: name, AllowedCapabilities: runtimeCapabilities, InspectPaths: []string{"."}, WritePaths: []string{"."}, BudgetCeiling: budget})
	}
	instruction := []byte("write the deterministic desktop pilot output")
	selectionBudget := orchestration.DispatchBudget{MaxTokens: 100, MaxCostMicros: 1000, MaxWallClock: 30 * time.Second, MaxConcurrentWorker: 1}
	return scheduler.DispatchRequest{
		Graph: graph, Events: events,
		Selection: orchestration.SelectionRequest{
			Task:        storage.ProjectTaskProjection{ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1, State: string(orchestration.ReadinessReady)},
			Node:        storage.ProjectTaskNodeProjection{ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1, WorkPackageID: work.WorkPackageID, DefinitionRecordID: work.RecordID, DefinitionHash: work.Integrity.Digest, Readiness: string(orchestration.ReadinessReady)},
			WorkPackage: work, WorkPackageDigest: work.Integrity.Digest, Risk: orchestration.RiskModerate, RequestedBudget: selectionBudget,
			RequiredRuntime: runtimeCapabilities,
			Policy:          orchestration.SelectionPolicy{AllowedCapabilities: runtimeCapabilities, MaximumRisk: orchestration.RiskHigh, MaximumBudget: selectionBudget},
			Candidates:      []orchestration.Candidate{{Worker: worker, Trade: tradeDefinition, Runtime: composition.RuntimeRef, RuntimeAvailable: true, RuntimeCapabilities: runtimeCapabilities, Node: nodeDefinition, Observation: nodeObservation}}, Now: now,
		},
		Binding: dispatchbinding.BindAndPlanRequest{
			Contract: executioncontract.BuildRequest{ContractID: "contract:local-pilot", Version: 1, Task: task, TaskDigest: task.Integrity.Digest, Graph: revision, GraphDigest: revision.Integrity.Digest, WorkPackage: work, WorkPackageDigest: work.Integrity.Digest, ExecutionID: "execution:local-pilot", ProjectRevision: strings.Repeat("d", 64), Trade: trade, Worker: workerRef, WorkerProfile: worker, Instruction: executioncontract.BindingReference{ID: "instruction:local-pilot", Version: 1, Digest: pilotDigest(instruction)}, Runtime: composition.RuntimeRef, Provider: composition.ProviderRef, Model: composition.ModelRef, Node: composition.NodeRef, RequestedCapabilities: runtimeCapabilities, PolicyLayers: policies, RequestedBudget: budget, NotBefore: now, Deadline: now.Add(30 * time.Second), CreatedAt: now, CreatedBy: "actor:desktop-pilot"},
			Plan:     orchestration.PlanRequest{AssignmentID: "assignment:local-pilot", AttemptID: "attempt:local-pilot", ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: 1, GraphRevision: 1, WorkPackageID: work.WorkPackageID, ExecutionID: "execution:local-pilot", WorkerID: worker.WorkerID, NodeID: composition.NodeRef.ID, IdempotencyDigest: pilotDigest([]byte("plan")), OperationID: "operation:local-pilot-plan", OperationDigest: pilotDigest([]byte("plan-operation")), AuditID: "audit:local-pilot-plan", ActorID: "actor:desktop-pilot"},
		},
		Workspace:         workspace.PreflightRequest{ProjectSyncRoot: projectRoot, RepositoryRoot: projectRoot, WorktreeBase: filepath.Join(filepath.Dir(projectRoot), "allocated"), BaseCommit: head, RequiredDiskBytes: 1, AvailableDiskBytes: 2},
		InstructionBundle: instruction,
	}
}

func pilotDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
