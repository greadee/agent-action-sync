// Package scheduler composes deterministic DAG readiness, worker selection,
// immutable attempt binding, compute leases, workspaces, and runtime sessions.
// It deliberately stops at collecting: only result intake and work history may
// turn runtime output into canonical project acceptance.
package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"syncgate/internal/computenode"
	"syncgate/internal/dispatchbinding"
	"syncgate/internal/executioncontract"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/runtimecontract"
	"syncgate/internal/storage"
	"syncgate/internal/workspace"
)

const (
	defaultMaxConcurrent = 2
	defaultLeaseDuration = 5 * time.Minute
	defaultPollInterval  = time.Second
	maximumLeaseDuration = 24 * time.Hour
)

var (
	ErrInvalidConfiguration = errors.New("invalid scheduler configuration")
	ErrNotStarted           = errors.New("scheduler is not started")
	ErrDraining             = errors.New("scheduler is draining")
)

type BindingPlanner interface {
	BindAndPlan(context.Context, dispatchbinding.BindAndPlanRequest) (dispatchbinding.BindAndPlanResult, error)
}

type Control interface {
	Claim(context.Context, orchestration.ClaimRequest) (storage.OrchestrationWriteResult, error)
	BindResources(context.Context, orchestration.BindResourcesRequest) (storage.OrchestrationWriteResult, error)
	Transition(context.Context, orchestration.TransitionRequest) (storage.OrchestrationWriteResult, error)
	Heartbeat(context.Context, orchestration.HeartbeatRequest) (storage.OrchestrationSnapshot, error)
	Reconcile(context.Context, string) ([]storage.OrchestrationSnapshot, error)
}

type RuntimeResolver func(context.Context, string) (runtimecontract.Adapter, error)
type NodeResolver func(context.Context, string) (computenode.Provider, error)

var _ BindingPlanner = dispatchbinding.ContractBindingService{}
var _ Control = orchestration.ControlService{}

// WorkSource is optional. When configured, Start polls it until Shutdown. A
// caller may instead invoke Cycle explicitly, which is useful for deterministic
// tests and API-triggered dispatch.
type WorkSource interface {
	Pending(context.Context) ([]DispatchRequest, error)
}

type Config struct {
	Binder         BindingPlanner
	Control        Control
	Workspace      workspace.Manager
	ResolveRuntime RuntimeResolver
	ResolveNode    NodeResolver
	Source         WorkSource
	ActorID        string
	MaxConcurrent  int
	LeaseDuration  time.Duration
	PollInterval   time.Duration
	StartPaused    bool
}

type DispatchRequest struct {
	Graph             orchestration.Graph
	Events            []orchestration.Event
	Selection         orchestration.SelectionRequest
	Binding           dispatchbinding.BindAndPlanRequest
	Workspace         workspace.PreflightRequest
	InstructionBundle []byte
}

type Outcome string

const (
	OutcomeDispatched Outcome = "dispatched"
	OutcomeWaiting    Outcome = "waiting"
	OutcomeBlocked    Outcome = "blocked"
	OutcomeDuplicate  Outcome = "duplicate"
	OutcomeCollecting Outcome = "collecting"
	OutcomeFailed     Outcome = "failed"
	OutcomeCanceled   Outcome = "canceled"
)

type ItemReport struct {
	WorkPackageID string
	AssignmentID  string
	Outcome       Outcome
	Reasons       []string
}

type CycleReport struct {
	Items  []ItemReport
	Active int
}

type RecoveryReport struct {
	AssignmentID string
	State        storage.AssignmentState
	Disposition  storage.RecoveryDisposition
	Runtime      runtimecontract.Status
	Workspace    workspace.State
	NodeHealth   computenode.Health
	Reason       string
}

type Scheduler struct {
	config Config

	mu       sync.Mutex
	started  bool
	draining bool
	paused   bool
	active   map[string]*activeAttempt
	known    map[string]storage.AssignmentState
	work     map[string]string
	recovery []RecoveryReport
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

type Status struct {
	Started       bool
	Paused        bool
	Draining      bool
	Active        int
	RecoveryCount int
}

type activeAttempt struct {
	assignmentID  string
	attemptID     string
	workPackageID string
	runtimeID     string
	runtime       runtimecontract.Adapter
	node          computenode.Provider
	nodeLease     computenode.Lease
	session       runtimecontract.Session
	fence         string
	state         storage.AssignmentState
}

type queuedRequest struct {
	request  DispatchRequest
	priority int
}

func New(config Config) (*Scheduler, error) {
	if config.Binder == nil || config.Control == nil || config.Workspace == nil || config.ResolveRuntime == nil || config.ResolveNode == nil {
		return nil, ErrInvalidConfiguration
	}
	if config.ActorID == "" {
		config.ActorID = "scheduler:daemon"
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = defaultMaxConcurrent
	}
	if config.MaxConcurrent < 1 || config.MaxConcurrent > 2 || config.LeaseDuration < 0 || config.LeaseDuration > maximumLeaseDuration || config.PollInterval < 0 {
		return nil, ErrInvalidConfiguration
	}
	if config.LeaseDuration == 0 {
		config.LeaseDuration = defaultLeaseDuration
	}
	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.Source != nil && config.PollInterval >= config.LeaseDuration {
		return nil, ErrInvalidConfiguration
	}
	return &Scheduler{config: config, active: map[string]*activeAttempt{}, known: map[string]storage.AssignmentState{}, work: map[string]string{}}, nil
}

// Start reconciles durable control state and externally owned resources before
// any new dispatch is allowed.
func (scheduler *Scheduler) Start(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	scheduler.mu.Lock()
	if scheduler.started {
		scheduler.mu.Unlock()
		return nil
	}
	scheduler.mu.Unlock()

	snapshots, err := scheduler.config.Control.Reconcile(ctx, scheduler.config.ActorID)
	if err != nil {
		return fmt.Errorf("reconcile orchestration control: %w", err)
	}
	reports := scheduler.inspectRecovery(ctx, snapshots)
	runCtx, cancel := context.WithCancel(ctx)
	scheduler.mu.Lock()
	if scheduler.started {
		scheduler.mu.Unlock()
		cancel()
		return nil
	}
	scheduler.started = true
	scheduler.paused = scheduler.config.StartPaused
	scheduler.recovery = reports
	scheduler.cancel = cancel
	for _, snapshot := range snapshots {
		scheduler.known[snapshot.Assignment.AssignmentID] = snapshot.Attempt.State
		scheduler.work[snapshot.Assignment.WorkPackageID] = snapshot.Assignment.AssignmentID
	}
	source := scheduler.config.Source
	if source != nil {
		scheduler.wg.Add(1)
	}
	scheduler.mu.Unlock()
	if source != nil {
		go scheduler.run(runCtx, source)
	}
	return nil
}

func (scheduler *Scheduler) Status() Status {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return Status{
		Started: scheduler.started, Paused: scheduler.paused, Draining: scheduler.draining,
		Active: len(scheduler.active), RecoveryCount: len(scheduler.recovery),
	}
}

// SetMaxConcurrent applies an authority-local project ceiling while dispatch
// is paused. It cannot be changed under a running scheduler, which keeps the
// boundary deterministic across project selection changes.
func (scheduler *Scheduler) SetMaxConcurrent(ctx context.Context, value int) error {
	if ctx == nil || value < 1 || value > 2 {
		return ErrInvalidConfiguration
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if scheduler.started && !scheduler.paused {
		return errors.New("scheduler concurrency can only change while paused")
	}
	scheduler.config.MaxConcurrent = value
	return nil
}

func (scheduler *Scheduler) run(ctx context.Context, source WorkSource) {
	defer scheduler.wg.Done()
	ticker := time.NewTicker(scheduler.config.PollInterval)
	defer ticker.Stop()
	for {
		requests, err := source.Pending(ctx)
		if err == nil {
			_, _ = scheduler.Cycle(ctx, requests)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (scheduler *Scheduler) inspectRecovery(ctx context.Context, snapshots []storage.OrchestrationSnapshot) []RecoveryReport {
	reports := make([]RecoveryReport, 0, len(snapshots))
	for _, snapshot := range snapshots {
		report := RecoveryReport{AssignmentID: snapshot.Assignment.AssignmentID, State: snapshot.Attempt.State, Disposition: snapshot.Attempt.RecoveryDisposition}
		if snapshot.Lease != nil {
			if provider, err := scheduler.config.ResolveNode(ctx, snapshot.Lease.OwnerNodeID); err == nil {
				if value, observeErr := provider.Observe(ctx); observeErr == nil {
					report.NodeHealth = value.Health
				} else {
					report.Reason = "node_reconciliation_failed"
				}
			} else {
				report.Reason = "node_unavailable"
			}
		}
		if snapshot.Resources != nil {
			if value, err := scheduler.config.Workspace.Inspect(ctx, snapshot.Resources.WorkspaceID); err == nil {
				report.Workspace = value.State
			} else {
				report.Reason = "workspace_reconciliation_failed"
			}
			if snapshot.Lease != nil {
				if adapter, err := scheduler.config.ResolveRuntime(ctx, snapshot.Lease.OwnerRuntime); err == nil {
					if value, observeErr := adapter.Observe(ctx, snapshot.Resources.RuntimeSessionID); observeErr == nil {
						report.Runtime = value.Status
					} else if report.Reason == "" {
						report.Reason = "runtime_reconciliation_failed"
					}
				} else if report.Reason == "" {
					report.Reason = "runtime_unavailable"
				}
			}
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].AssignmentID < reports[j].AssignmentID })
	return reports
}

// Cycle is serialized so concurrent API and polling triggers cannot dispatch
// the same assignment twice or overrun a lease/concurrency boundary.
func (scheduler *Scheduler) Cycle(ctx context.Context, requests []DispatchRequest) (CycleReport, error) {
	if ctx == nil {
		return CycleReport{}, ErrInvalidConfiguration
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if !scheduler.started {
		return CycleReport{}, ErrNotStarted
	}
	if scheduler.draining {
		return CycleReport{}, ErrDraining
	}
	if err := ctx.Err(); err != nil {
		return CycleReport{}, err
	}

	report := CycleReport{}
	scheduler.reapLocked(ctx, &report)
	queue := make([]queuedRequest, 0, len(requests))
	seenAssignments := map[string]bool{}
	seenWork := map[string]bool{}
	for _, request := range requests {
		snapshot, err := orchestration.ReduceReadiness(ctx, request.Graph, request.Events)
		if err != nil {
			return report, err
		}
		workPackageID := request.Selection.WorkPackage.WorkPackageID
		node, found := readinessNode(snapshot, workPackageID)
		if !found {
			return report, fmt.Errorf("%w: work package %s is absent from graph", ErrInvalidConfiguration, workPackageID)
		}
		assignmentID := request.Binding.Plan.AssignmentID
		if seenAssignments[assignmentID] || seenWork[workPackageID] {
			report.Items = append(report.Items, ItemReport{WorkPackageID: workPackageID, AssignmentID: assignmentID, Outcome: OutcomeDuplicate, Reasons: []string{"duplicate_dispatch_request"}})
			continue
		}
		seenAssignments[assignmentID] = true
		seenWork[workPackageID] = true
		if existing, assigned := scheduler.work[workPackageID]; assigned && existing != assignmentID {
			report.Items = append(report.Items, ItemReport{WorkPackageID: workPackageID, AssignmentID: assignmentID, Outcome: OutcomeDuplicate, Reasons: []string{"work_package_already_assigned"}})
			continue
		}
		if state, duplicate := scheduler.known[assignmentID]; duplicate && state != storage.AssignmentPlanned {
			report.Items = append(report.Items, ItemReport{WorkPackageID: workPackageID, AssignmentID: assignmentID, Outcome: OutcomeDuplicate, Reasons: []string{string(state)}})
			continue
		}
		if node.State != orchestration.ReadinessReady {
			outcome := OutcomeWaiting
			if node.State == orchestration.ReadinessBlocked || node.State == orchestration.ReadinessFailed || node.State == orchestration.ReadinessCanceled {
				outcome = OutcomeBlocked
			}
			report.Items = append(report.Items, ItemReport{WorkPackageID: workPackageID, AssignmentID: assignmentID, Outcome: outcome, Reasons: []string{node.ExplanationCode}})
			continue
		}
		request.Selection.Task.State = string(snapshot.State)
		request.Selection.Task.ExplanationCode = snapshot.ExplanationCode
		request.Selection.Task.ParallelReady = append([]string(nil), snapshot.ParallelReady...)
		request.Selection.Node.CanonicalState = string(node.CanonicalState)
		request.Selection.Node.Readiness = string(node.State)
		request.Selection.Node.ExplanationCode = node.ExplanationCode
		queue = append(queue, queuedRequest{request: request, priority: priorityRank(request.Selection.WorkPackage.Priority, request.Graph.Task.Priority)})
	}
	sort.SliceStable(queue, func(i, j int) bool {
		if queue[i].priority != queue[j].priority {
			return queue[i].priority > queue[j].priority
		}
		return queue[i].request.Selection.WorkPackage.WorkPackageID < queue[j].request.Selection.WorkPackage.WorkPackageID
	})

	for _, queued := range queue {
		request := queued.request
		workPackageID := request.Selection.WorkPackage.WorkPackageID
		assignmentID := request.Binding.Plan.AssignmentID
		if len(scheduler.active) >= scheduler.config.MaxConcurrent || scheduler.paused {
			reason := "concurrency_limit_reached"
			if scheduler.paused {
				reason = "scheduler_paused"
			}
			report.Items = append(report.Items, ItemReport{WorkPackageID: workPackageID, AssignmentID: assignmentID, Outcome: OutcomeBlocked, Reasons: []string{reason}})
			continue
		}
		request.Selection.Policy.ActiveAssignments = int64(len(scheduler.active))
		if request.Selection.Policy.MaximumBudget.MaxConcurrentWorker > int64(scheduler.config.MaxConcurrent) {
			request.Selection.Policy.MaximumBudget.MaxConcurrentWorker = int64(scheduler.config.MaxConcurrent)
		}
		runtimes, nodes := scheduler.refreshCandidates(ctx, &request.Selection)
		selection, err := orchestration.Select(ctx, request.Selection)
		if err != nil {
			return report, err
		}
		if selection.Blocked {
			report.Items = append(report.Items, ItemReport{WorkPackageID: workPackageID, AssignmentID: assignmentID, Outcome: OutcomeBlocked, Reasons: append([]string(nil), selection.BlockReasons...)})
			continue
		}
		request.Binding.Selection = selection
		bindSelectedCandidate(&request.Binding, request.Selection.Candidates, *selection.Selected)
		item, err := scheduler.dispatchLocked(ctx, request, runtimes, nodes)
		report.Items = append(report.Items, item)
		if err != nil {
			continue
		}
	}
	report.Active = len(scheduler.active)
	return report, nil
}

func bindSelectedCandidate(binding *dispatchbinding.BindAndPlanRequest, candidates []orchestration.Candidate, selected orchestration.CandidateIdentity) {
	for _, candidate := range candidates {
		if candidate.Worker.WorkerID != selected.WorkerID || candidate.Worker.Version != selected.WorkerVersion || candidate.Node.NodeID != selected.NodeID || candidate.Node.Version != selected.NodeVersion || candidate.Runtime.ID != selected.RuntimeID || candidate.Runtime.Version != selected.RuntimeVer {
			continue
		}
		binding.Plan.WorkerID = selected.WorkerID
		binding.Plan.NodeID = selected.NodeID
		binding.Contract.Worker = project.RegistryReference{ID: candidate.Worker.WorkerID, Version: candidate.Worker.Version, Digest: candidate.Worker.ContentHash}
		binding.Contract.WorkerProfile = candidate.Worker
		binding.Contract.Runtime = candidate.Runtime
		binding.Contract.Node = executionReference(candidate.Node)
		return
	}
}

func (scheduler *Scheduler) refreshCandidates(ctx context.Context, request *orchestration.SelectionRequest) (map[string]runtimecontract.Adapter, map[string]computenode.Provider) {
	runtimes := map[string]runtimecontract.Adapter{}
	nodes := map[string]computenode.Provider{}
	for index := range request.Candidates {
		candidate := &request.Candidates[index]
		adapter, runtimeErr := scheduler.config.ResolveRuntime(ctx, candidate.Runtime.ID)
		candidate.RuntimeAvailable = runtimeErr == nil && adapter != nil
		if candidate.RuntimeAvailable {
			runtimes[candidate.Runtime.ID] = adapter
		}
		provider, nodeErr := scheduler.config.ResolveNode(ctx, candidate.Node.NodeID)
		if nodeErr == nil && provider != nil {
			definition, definitionErr := provider.Definition(ctx)
			observation, observationErr := provider.Observe(ctx)
			expected := executionReference(candidate.Node)
			if definitionErr == nil && observationErr == nil && executionReference(definition) == expected {
				candidate.Node, candidate.Observation = definition, observation
				nodes[definition.NodeID] = provider
				continue
			}
		}
		candidate.Observation.Health = computenode.HealthUnhealthy
	}
	return runtimes, nodes
}

func (scheduler *Scheduler) dispatchLocked(ctx context.Context, request DispatchRequest, runtimes map[string]runtimecontract.Adapter, nodes map[string]computenode.Provider) (ItemReport, error) {
	plan := request.Binding.Plan
	workPackageID, assignmentID := request.Selection.WorkPackage.WorkPackageID, plan.AssignmentID
	blocked := func(reason string, err error) (ItemReport, error) {
		return ItemReport{WorkPackageID: workPackageID, AssignmentID: assignmentID, Outcome: OutcomeBlocked, Reasons: []string{reason}}, err
	}
	bound, err := scheduler.config.Binder.BindAndPlan(ctx, request.Binding)
	if err != nil {
		return blocked("binding_failed", err)
	}
	scheduler.known[assignmentID] = storage.AssignmentPlanned
	scheduler.work[workPackageID] = assignmentID
	selected := request.Binding.Selection.Selected
	adapter, runtimeOK := runtimes[selected.RuntimeID]
	provider, nodeOK := nodes[selected.NodeID]
	if !runtimeOK || !nodeOK {
		return blocked("selected_resource_unavailable", ErrInvalidConfiguration)
	}
	nodeDefinition, err := provider.Definition(ctx)
	if err != nil {
		return blocked("node_unavailable", err)
	}
	if executionReference(nodeDefinition) != bound.Contract.Node {
		return blocked("node_definition_changed", computenode.ErrNodeUnavailable)
	}
	nodeLease, err := provider.AcquireLease(ctx, computenode.LeaseRequest{
		Node: executionReference(nodeDefinition), OwnerID: assignmentID, Duration: scheduler.config.LeaseDuration,
		IdempotencyKeyDigest: digest("node-lease", assignmentID, plan.AttemptID, bound.Contract.Digest),
	})
	if err != nil {
		return blocked("lease_unavailable", err)
	}
	fence := digest("fence", assignmentID, plan.AttemptID, nodeLease.LeaseID, bound.Contract.Digest)
	claimed, err := scheduler.config.Control.Claim(ctx, orchestration.ClaimRequest{
		AssignmentID: assignmentID, AttemptID: plan.AttemptID, LeaseID: nodeLease.LeaseID, OwnerNodeID: selected.NodeID,
		OwnerRuntimeID: selected.RuntimeID, FencingDigest: fence, OperationID: stableID("operation:claim:", assignmentID, plan.AttemptID),
		OperationDigest: digest("claim", assignmentID, plan.AttemptID, fence), AuditID: stableID("audit:claim:", assignmentID, plan.AttemptID), ActorID: scheduler.config.ActorID,
	})
	if err != nil {
		_ = provider.ReleaseLease(ctx, nodeLease.LeaseID, assignmentID, nodeLease.Generation)
		return blocked("claim_failed", err)
	}
	leaseGeneration := claimed.Snapshot.Attempt.LeaseGeneration
	if leaseGeneration < 1 {
		leaseGeneration = 1
	}
	workspaceRequest := request.Workspace
	workspaceRequest.WorkspaceID = stableID("workspace:", assignmentID, plan.AttemptID)
	workspaceRequest.OwnerID = assignmentID
	workspaceRequest.AttemptID = plan.AttemptID
	workspaceRequest.IdempotencyKeyDigest = digest("workspace", assignmentID, plan.AttemptID, bound.Contract.Digest)
	workspaceRequest.BranchName, err = workspace.AttemptBranchName(plan.AttemptID)
	if err != nil {
		scheduler.cancelClaimedLocked(ctx, provider, nodeLease, assignmentID, plan.AttemptID, leaseGeneration, fence, "workspace_invalid")
		return blocked("workspace_invalid", err)
	}
	allocated, err := scheduler.config.Workspace.Allocate(ctx, workspaceRequest)
	if err != nil {
		scheduler.cancelClaimedLocked(ctx, provider, nodeLease, assignmentID, plan.AttemptID, leaseGeneration, fence, "workspace_unavailable")
		return blocked("workspace_unavailable", err)
	}
	resumeKey := digest("resume", assignmentID, plan.AttemptID, bound.Contract.Digest)
	session, err := adapter.Prepare(ctx, runtimecontract.PrepareRequest{
		Contract: bound.Contract, AssignmentID: assignmentID, AttemptID: plan.AttemptID, LeaseGeneration: leaseGeneration, FencingDigest: fence,
		WorkspaceID: allocated.WorkspaceID, ContextBundle: append([]byte(nil), bound.Context.Bytes...), InstructionBundle: append([]byte(nil), request.InstructionBundle...),
		IdempotencyKeyDigest: digest("prepare", assignmentID, plan.AttemptID, bound.Contract.Digest), ResumeKeyDigest: resumeKey,
	})
	if err != nil {
		scheduler.cancelClaimedLocked(ctx, provider, nodeLease, assignmentID, plan.AttemptID, leaseGeneration, fence, "runtime_prepare_failed")
		return blocked("runtime_prepare_failed", err)
	}
	prepared, err := scheduler.config.Control.BindResources(ctx, orchestration.BindResourcesRequest{
		AssignmentID: assignmentID, AttemptID: plan.AttemptID, LeaseGeneration: leaseGeneration, FencingDigest: fence,
		RuntimeSessionID: session.SessionID, RuntimeResumeKey: resumeKey, WorkspaceID: allocated.WorkspaceID, WorkspaceGeneration: allocated.Generation,
		OperationID: stableID("operation:prepare:", assignmentID, plan.AttemptID), OperationDigest: digest("bind-resources", assignmentID, plan.AttemptID, session.SessionID),
		AuditID: stableID("audit:prepare:", assignmentID, plan.AttemptID), ActorID: scheduler.config.ActorID,
	})
	if err != nil {
		_, _ = adapter.Cancel(ctx, action(session.SessionID, "cancel-unbound", assignmentID, plan.AttemptID))
		scheduler.cancelClaimedLocked(ctx, provider, nodeLease, assignmentID, plan.AttemptID, leaseGeneration, fence, "resource_binding_failed")
		return blocked("resource_binding_failed", err)
	}
	_ = prepared
	observation, err := adapter.Start(ctx, action(session.SessionID, "start", assignmentID, plan.AttemptID))
	if err != nil {
		_, _ = scheduler.transition(ctx, assignmentID, plan.AttemptID, leaseGeneration, fence, storage.AssignmentFailed, "runtime_start_failed")
		_ = provider.ReleaseLease(ctx, nodeLease.LeaseID, assignmentID, nodeLease.Generation)
		scheduler.known[assignmentID] = storage.AssignmentFailed
		return blocked("runtime_start_failed", err)
	}
	if observation.Status != runtimecontract.StatusRunning {
		_, _ = scheduler.transition(ctx, assignmentID, plan.AttemptID, leaseGeneration, fence, storage.AssignmentFailed, "runtime_not_running")
		_ = provider.ReleaseLease(ctx, nodeLease.LeaseID, assignmentID, nodeLease.Generation)
		scheduler.known[assignmentID] = storage.AssignmentFailed
		return blocked("runtime_not_running", errors.New("runtime did not enter running state"))
	}
	session.Status, session.Sequence, session.UpdatedAt = observation.Status, observation.Sequence, observation.UpdatedAt
	if _, err := scheduler.transition(ctx, assignmentID, plan.AttemptID, leaseGeneration, fence, storage.AssignmentRunning, ""); err != nil {
		_, _ = adapter.Cancel(ctx, action(session.SessionID, "cancel-start-race", assignmentID, plan.AttemptID))
		_ = provider.ReleaseLease(ctx, nodeLease.LeaseID, assignmentID, nodeLease.Generation)
		return blocked("running_transition_failed", err)
	}
	scheduler.active[assignmentID] = &activeAttempt{assignmentID: assignmentID, attemptID: plan.AttemptID, workPackageID: workPackageID, runtimeID: selected.RuntimeID, runtime: adapter, node: provider, nodeLease: nodeLease, session: session, fence: fence, state: storage.AssignmentRunning}
	scheduler.known[assignmentID] = storage.AssignmentRunning
	return ItemReport{WorkPackageID: workPackageID, AssignmentID: assignmentID, Outcome: OutcomeDispatched}, nil
}

func (scheduler *Scheduler) cancelClaimedLocked(ctx context.Context, provider computenode.Provider, lease computenode.Lease, assignmentID, attemptID string, generation int64, fence, reason string) {
	_, _ = scheduler.transition(ctx, assignmentID, attemptID, generation, fence, storage.AssignmentCanceled, reason)
	_ = provider.ReleaseLease(ctx, lease.LeaseID, assignmentID, lease.Generation)
	scheduler.known[assignmentID] = storage.AssignmentCanceled
}

func (scheduler *Scheduler) reapLocked(ctx context.Context, report *CycleReport) {
	ids := make([]string, 0, len(scheduler.active))
	for id := range scheduler.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		attempt := scheduler.active[id]
		if reason, err := scheduler.renewLocked(ctx, attempt); err != nil {
			_, _ = attempt.runtime.Cancel(ctx, action(attempt.session.SessionID, "cancel-lease-loss", id, attempt.attemptID))
			if _, transitionErr := scheduler.transition(ctx, id, attempt.attemptID, attempt.session.LeaseGeneration, attempt.fence, storage.AssignmentFailed, reason); transitionErr == nil {
				scheduler.known[id] = storage.AssignmentFailed
			}
			scheduler.releaseActiveLease(ctx, attempt)
			delete(scheduler.active, id)
			report.Items = append(report.Items, ItemReport{WorkPackageID: attempt.workPackageID, AssignmentID: id, Outcome: OutcomeFailed, Reasons: []string{reason}})
			continue
		}
		observation, err := attempt.runtime.Observe(ctx, attempt.session.SessionID)
		if err != nil {
			continue
		}
		attempt.session.Status, attempt.session.Sequence, attempt.session.UpdatedAt = observation.Status, observation.Sequence, observation.UpdatedAt
		switch observation.Status {
		case runtimecontract.StatusSucceeded:
			if _, err := scheduler.transition(ctx, id, attempt.attemptID, attempt.session.LeaseGeneration, attempt.fence, storage.AssignmentCollecting, ""); err == nil {
				scheduler.releaseActiveLease(ctx, attempt)
				delete(scheduler.active, id)
				scheduler.known[id] = storage.AssignmentCollecting
				report.Items = append(report.Items, ItemReport{WorkPackageID: attempt.workPackageID, AssignmentID: id, Outcome: OutcomeCollecting})
			}
		case runtimecontract.StatusFailed, runtimecontract.StatusClosed:
			if _, err := scheduler.transition(ctx, id, attempt.attemptID, attempt.session.LeaseGeneration, attempt.fence, storage.AssignmentFailed, runtimeFailureCode(observation)); err == nil {
				scheduler.releaseActiveLease(ctx, attempt)
				delete(scheduler.active, id)
				scheduler.known[id] = storage.AssignmentFailed
				report.Items = append(report.Items, ItemReport{WorkPackageID: attempt.workPackageID, AssignmentID: id, Outcome: OutcomeFailed, Reasons: []string{runtimeFailureCode(observation)}})
			}
		case runtimecontract.StatusCanceled:
			if _, err := scheduler.transition(ctx, id, attempt.attemptID, attempt.session.LeaseGeneration, attempt.fence, storage.AssignmentCanceled, "runtime_canceled"); err == nil {
				scheduler.releaseActiveLease(ctx, attempt)
				delete(scheduler.active, id)
				scheduler.known[id] = storage.AssignmentCanceled
				report.Items = append(report.Items, ItemReport{WorkPackageID: attempt.workPackageID, AssignmentID: id, Outcome: OutcomeCanceled})
			}
		}
	}
}

func (scheduler *Scheduler) renewLocked(ctx context.Context, attempt *activeAttempt) (string, error) {
	renewed, err := attempt.node.RenewLease(ctx, computenode.RenewRequest{
		LeaseID: attempt.nodeLease.LeaseID, OwnerID: attempt.assignmentID, Generation: attempt.nodeLease.Generation,
		Duration: scheduler.config.LeaseDuration, IdempotencyKeyDigest: digest("node-renew", attempt.assignmentID, attempt.attemptID, fmt.Sprint(attempt.nodeLease.Generation)),
	})
	if err != nil {
		return "lease_renewal_failed", err
	}
	attempt.nodeLease = renewed
	if _, err := scheduler.config.Control.Heartbeat(ctx, orchestration.HeartbeatRequest{
		AssignmentID: attempt.assignmentID, AttemptID: attempt.attemptID, LeaseID: attempt.nodeLease.LeaseID,
		LeaseGeneration: attempt.session.LeaseGeneration, FencingDigest: attempt.fence, ExtendBy: scheduler.config.LeaseDuration,
	}); err != nil {
		return "control_lease_heartbeat_failed", err
	}
	return "", nil
}

func (scheduler *Scheduler) Pause(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidConfiguration
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if !scheduler.started {
		return ErrNotStarted
	}
	scheduler.paused = true
	ids := sortedActive(scheduler.active)
	var result error
	for _, id := range ids {
		attempt := scheduler.active[id]
		observation, err := attempt.runtime.Pause(ctx, action(attempt.session.SessionID, "pause", id, attempt.attemptID, fmt.Sprint(attempt.session.Sequence)))
		if runtimecontract.IsCode(err, runtimecontract.CodeCapabilityUnavailable) {
			continue
		}
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if observation.Status == runtimecontract.StatusPaused {
			if _, err := scheduler.transition(ctx, id, attempt.attemptID, attempt.session.LeaseGeneration, attempt.fence, storage.AssignmentPaused, "", fmt.Sprint(observation.Sequence)); err != nil {
				result = errors.Join(result, err)
			} else {
				attempt.session.Status, attempt.session.Sequence, attempt.session.UpdatedAt = observation.Status, observation.Sequence, observation.UpdatedAt
				attempt.state = storage.AssignmentPaused
				scheduler.known[id] = storage.AssignmentPaused
			}
		}
	}
	return result
}

func (scheduler *Scheduler) Resume(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidConfiguration
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if !scheduler.started {
		return ErrNotStarted
	}
	var result error
	for _, id := range sortedActive(scheduler.active) {
		attempt := scheduler.active[id]
		if attempt.state != storage.AssignmentPaused {
			continue
		}
		observation, err := attempt.runtime.Resume(ctx, action(attempt.session.SessionID, "resume", id, attempt.attemptID, fmt.Sprint(attempt.session.Sequence)))
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if observation.Status == runtimecontract.StatusRunning {
			if _, err := scheduler.transition(ctx, id, attempt.attemptID, attempt.session.LeaseGeneration, attempt.fence, storage.AssignmentRunning, "", fmt.Sprint(observation.Sequence)); err != nil {
				result = errors.Join(result, err)
			} else {
				attempt.session.Status, attempt.session.Sequence, attempt.session.UpdatedAt = observation.Status, observation.Sequence, observation.UpdatedAt
				attempt.state = storage.AssignmentRunning
				scheduler.known[id] = storage.AssignmentRunning
			}
		}
	}
	scheduler.paused = false
	return result
}

// Cancel is idempotent and intentionally never releases a workspace. Cleanup
// remains an explicit ownership-checked operator action.
func (scheduler *Scheduler) Cancel(ctx context.Context, assignmentID string) error {
	if ctx == nil {
		return ErrInvalidConfiguration
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	state, known := scheduler.known[assignmentID]
	if known && terminal(state) {
		return nil
	}
	attempt, active := scheduler.active[assignmentID]
	if !active {
		return nil
	}
	_, runtimeErr := attempt.runtime.Cancel(ctx, action(attempt.session.SessionID, "cancel", assignmentID, attempt.attemptID))
	_, controlErr := scheduler.transition(ctx, assignmentID, attempt.attemptID, attempt.session.LeaseGeneration, attempt.fence, storage.AssignmentCanceled, "operator_canceled")
	scheduler.releaseActiveLease(ctx, attempt)
	delete(scheduler.active, assignmentID)
	scheduler.known[assignmentID] = storage.AssignmentCanceled
	return errors.Join(runtimeErr, controlErr)
}

// Shutdown drains dispatch before daemon storage is closed.
func (scheduler *Scheduler) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidConfiguration
	}
	scheduler.mu.Lock()
	if !scheduler.started {
		scheduler.mu.Unlock()
		return nil
	}
	scheduler.draining = true
	scheduler.paused = true
	cancel := scheduler.cancel
	scheduler.cancel = nil
	scheduler.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	wait := make(chan struct{})
	go func() { scheduler.wg.Wait(); close(wait) }()
	select {
	case <-wait:
	case <-ctx.Done():
		return ctx.Err()
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	var result error
	for _, id := range sortedActive(scheduler.active) {
		attempt := scheduler.active[id]
		_, runtimeErr := attempt.runtime.Cancel(ctx, action(attempt.session.SessionID, "shutdown-cancel", id, attempt.attemptID))
		_, controlErr := scheduler.transition(ctx, id, attempt.attemptID, attempt.session.LeaseGeneration, attempt.fence, storage.AssignmentCanceled, "daemon_shutdown")
		scheduler.releaseActiveLease(ctx, attempt)
		delete(scheduler.active, id)
		scheduler.known[id] = storage.AssignmentCanceled
		result = errors.Join(result, runtimeErr, controlErr)
	}
	return result
}

func (scheduler *Scheduler) Recovery() []RecoveryReport {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return append([]RecoveryReport(nil), scheduler.recovery...)
}

func (scheduler *Scheduler) transition(ctx context.Context, assignmentID, attemptID string, generation int64, fence string, target storage.AssignmentState, failure string, nonce ...string) (storage.OrchestrationWriteResult, error) {
	identity := append([]string{assignmentID, attemptID}, nonce...)
	digestInput := append([]string{"transition", assignmentID, attemptID, string(target), failure}, nonce...)
	return scheduler.config.Control.Transition(ctx, orchestration.TransitionRequest{
		AssignmentID: assignmentID, AttemptID: attemptID, TargetState: target, LeaseGeneration: generation, FencingDigest: fence,
		FailureCode: failure, OperationID: stableID("operation:"+string(target)+":", identity...),
		OperationDigest: digest(digestInput...), AuditID: stableID("audit:"+string(target)+":", identity...), ActorID: scheduler.config.ActorID,
	})
}

func (scheduler *Scheduler) releaseActiveLease(ctx context.Context, attempt *activeAttempt) {
	_ = attempt.node.ReleaseLease(ctx, attempt.nodeLease.LeaseID, attempt.assignmentID, attempt.nodeLease.Generation)
}

func readinessNode(snapshot orchestration.Snapshot, id string) (orchestration.NodeReadiness, bool) {
	for _, node := range snapshot.Nodes {
		if node.WorkPackageID == id {
			return node, true
		}
	}
	return orchestration.NodeReadiness{}, false
}

func priorityRank(work, task project.TaskPriority) int {
	value := work
	if value == "" {
		value = task
	}
	switch value {
	case project.TaskPriorityCritical:
		return 4
	case project.TaskPriorityHigh:
		return 3
	case project.TaskPriorityNormal:
		return 2
	case project.TaskPriorityLow:
		return 1
	default:
		return 0
	}
}

func sortedActive(values map[string]*activeAttempt) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func terminal(state storage.AssignmentState) bool {
	switch state {
	case storage.AssignmentAccepted, storage.AssignmentFailed, storage.AssignmentCanceled, storage.AssignmentExpired:
		return true
	default:
		return false
	}
}

func runtimeFailureCode(observation runtimecontract.Observation) string {
	if observation.ErrorCode != "" {
		return string(observation.ErrorCode)
	}
	return "runtime_execution_failed"
}

func action(sessionID, label, assignmentID, attemptID string, nonce ...string) runtimecontract.ActionRequest {
	values := append([]string{label, assignmentID, attemptID}, nonce...)
	return runtimecontract.ActionRequest{SessionID: sessionID, IdempotencyKeyDigest: digest(values...)}
}

func executionReference(definition computenode.Definition) executioncontract.BindingReference {
	return executioncontract.BindingReference{ID: definition.NodeID, Version: definition.Version, Digest: definition.DefinitionDigest}
}

func stableID(prefix string, values ...string) string {
	return prefix + digest(values...)[:24]
}

func digest(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
