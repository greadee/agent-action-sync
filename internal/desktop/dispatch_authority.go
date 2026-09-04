package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/computenode"
	"syncgate/internal/contextcompiler"
	"syncgate/internal/dispatchbinding"
	"syncgate/internal/executioncontract"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/scheduler"
	"syncgate/internal/storage"
	"syncgate/internal/workspace"
)

const localContractWallClock = 6 * time.Hour

type workspacePreflighter interface {
	Preflight(context.Context, workspace.PreflightRequest) (workspace.PreflightReport, error)
}

type LocalDispatchAuthorityOptions struct {
	AuthorizedProjectID string
	Projects            storage.ProjectRegistrationStore
	Operations          storage.LocalProjectOperationsStore
	Tasks               storage.ProjectTaskStore
	TaskNodes           storage.ProjectTaskNodeStore
	Events              storage.ProjectEventStore
	Registry            storage.RegistryStore
	Contracts           storage.ExecutionContractStore
	Control             storage.OrchestrationControlStore
	Node                computenode.Provider
	Workspace           workspacePreflighter
	WorktreeBase        string
	BaseCommit          string
	ProjectRevision     string
	Runtime             executioncontract.BindingReference
	Provider            executioncontract.BindingReference
	Model               executioncontract.BindingReference
	NodeReference       executioncontract.BindingReference
	RuntimeCapabilities []executioncontract.Capability
	Worker              project.RegistryReference
	Now                 func() time.Time
}

type LocalDispatchAuthority struct {
	options LocalDispatchAuthorityOptions
}

type dispatchMaterial struct {
	registration storage.ProjectRegistration
	layout       project.Layout
	task         storage.ProjectTaskProjection
	node         storage.ProjectTaskNodeProjection
	graph        orchestration.Graph
	events       []orchestration.Event
	sources      []contextcompiler.SourceSpec
	sourceDigest string
}

type preparedDispatch struct {
	request  scheduler.DispatchRequest
	contract executioncontract.Contract
}

func NewLocalDispatchAuthority(options LocalDispatchAuthorityOptions) (*LocalDispatchAuthority, error) {
	if options.AuthorizedProjectID == "" || options.Projects == nil || options.Operations == nil || options.Tasks == nil || options.TaskNodes == nil || options.Events == nil || options.Registry == nil || options.Contracts == nil || options.Control == nil || options.Node == nil || options.Workspace == nil || options.WorktreeBase == "" || options.BaseCommit == "" || !validDispatchDigest(options.ProjectRevision) || options.Now == nil || options.Worker.ID == "" || len(options.RuntimeCapabilities) == 0 {
		return nil, errors.New("local dispatch authority is incomplete")
	}
	return &LocalDispatchAuthority{options: options}, nil
}

// Pending returns only work approved for the selected execution-authorized
// project. It reconstructs exact portable records before creating a request.
func (authority *LocalDispatchAuthority) Pending(ctx context.Context) ([]scheduler.DispatchRequest, error) {
	if authority == nil || ctx == nil {
		return nil, errors.New("local dispatch authority is unavailable")
	}
	selected, err := authority.options.Operations.GetSelectedLocalProject(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return []scheduler.DispatchRequest{}, nil
	}
	if err != nil {
		return nil, err
	}
	if selected.ProjectID != authority.options.AuthorizedProjectID {
		return []scheduler.DispatchRequest{}, nil
	}
	policy, err := authority.options.Operations.GetLocalProjectPolicy(ctx, selected.ProjectID)
	if errors.Is(err, storage.ErrNotFound) || err == nil && !policy.SchedulingEnabled {
		return []scheduler.DispatchRequest{}, nil
	}
	if err != nil {
		return nil, err
	}
	tasks, err := authority.listTasks(ctx, selected.ProjectID)
	if err != nil {
		return nil, err
	}
	requests := make([]scheduler.DispatchRequest, 0)
	for _, task := range tasks {
		if task.State != string(orchestration.ReadinessReady) {
			continue
		}
		approval, err := authority.approval(ctx, task)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		nodes, err := authority.listNodes(ctx, task)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			if node.Readiness != string(orchestration.ReadinessReady) {
				continue
			}
			ids := dispatchIdentities(task.ProjectID, task.TaskID, task.TaskRevision, task.GraphRevision, node.WorkPackageID, approval.IdempotencyKey)
			if _, err := authority.options.Control.GetAssignment(ctx, ids.assignmentID); err == nil {
				continue
			} else if !errors.Is(err, storage.ErrNotFound) {
				return nil, err
			}
			prepared, err := authority.prepare(ctx, task.ProjectID, node.WorkPackageID, approval.IdempotencyKey, nil, approval.OccurredAt)
			if err != nil {
				return nil, err
			}
			requests = append(requests, prepared.request)
		}
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].Binding.Plan.AssignmentID < requests[j].Binding.Plan.AssignmentID
	})
	return requests, nil
}

func (authority *LocalDispatchAuthority) ContextPreflight(ctx context.Context, input api.ContextPreflightInput) (api.ContextPreflightResult, error) {
	material, err := authority.loadMaterial(ctx, input.ProjectID, input.WorkPackageID)
	if err != nil {
		return api.ContextPreflightResult{}, err
	}
	trade := material.graph.Definitions[input.WorkPackageID].Record.TradeReference
	if trade == nil || trade.ID != input.TradeID || trade.Version != input.TradeVersion || trade.Digest != input.TradeDigest {
		return api.ContextPreflightResult{}, setupError(409, "trade_authority_conflict", "trade reference does not match portable work authority", nil)
	}
	if input.SourceSetDigest != "" && input.SourceSetDigest != material.sourceDigest {
		return api.ContextPreflightResult{}, setupError(409, "source_set_conflict", "source set digest does not match authority selection", nil)
	}
	compiled, err := contextcompiler.Compile(ctx, material.layout, contextcompiler.Request{
		ProjectID: input.ProjectID, WorkPackageID: input.WorkPackageID, TradeReference: *trade, Sources: material.sources,
	})
	if err != nil {
		return api.ContextPreflightResult{}, mapDispatchError(err)
	}
	omissions := noticeCodes(compiled.Bundle.Manifest.Omissions)
	warnings := noticeCodes(compiled.Bundle.Manifest.Warnings)
	return api.ContextPreflightResult{
		CompilerVersion: compiled.Bundle.Manifest.CompilerVersion, ContextDigest: compiled.Bundle.Manifest.ContextDigest,
		SourceSetDigest: material.sourceDigest, EstimatedTokens: compiled.Bundle.Manifest.EstimatedTokens,
		SourceCount: len(compiled.Bundle.Manifest.Sources), OmissionCodes: omissions, WarningCodes: warnings,
	}, nil
}

func (authority *LocalDispatchAuthority) PreviewContract(ctx context.Context, input api.ExecutionContractPreviewInput) (api.ExecutionContractPreviewResult, error) {
	confirmed := make([]executioncontract.Capability, 0, len(input.RequestedCapabilityIDs))
	for _, value := range input.RequestedCapabilityIDs {
		confirmed = append(confirmed, executioncontract.Capability(value))
	}
	prepared, err := authority.prepare(ctx, input.ProjectID, input.WorkPackageID, input.IdempotencyKey, confirmed, authority.options.Now().UTC())
	if err != nil {
		return api.ExecutionContractPreviewResult{}, err
	}
	contract := prepared.contract
	if contract.TaskID != input.TaskID || contract.TaskRevision != input.TaskRevision || contract.GraphRevision != input.GraphRevision || contract.Worker.ID != input.WorkerID || contract.Worker.Version != input.WorkerVersion || contract.Worker.Digest != input.WorkerDigest {
		return api.ExecutionContractPreviewResult{}, setupError(409, "contract_authority_conflict", "contract preview input does not match resolved authority", nil)
	}
	created, _, err := (executioncontract.Service{Store: authority.options.Contracts}).Create(ctx, prepared.request.Binding.Contract)
	if err != nil {
		return api.ExecutionContractPreviewResult{}, mapDispatchError(err)
	}
	return contractPreviewResult(created), nil
}

func (authority *LocalDispatchAuthority) RuntimePreflight(ctx context.Context, input api.RuntimePreflightInput) (api.RuntimePreflightResult, error) {
	if input.ProjectID != authority.options.AuthorizedProjectID || input.RuntimeID != authority.options.Runtime.ID || input.RuntimeVersion != authority.options.Runtime.Version || input.RuntimeDigest != authority.options.Runtime.Digest || input.NodeID != authority.options.NodeReference.ID || input.NodeVersion != authority.options.NodeReference.Version || input.NodeDigest != authority.options.NodeReference.Digest {
		return api.RuntimePreflightResult{}, setupError(409, "runtime_authority_conflict", "runtime or node reference is not execution-authorized", nil)
	}
	record, err := authority.options.Contracts.GetExecutionContract(ctx, input.ContractID, input.ContractVersion)
	if err != nil {
		return api.RuntimePreflightResult{}, mapDispatchError(err)
	}
	var contract executioncontract.Contract
	if json.Unmarshal(record.ContractJSON, &contract) != nil || executioncontract.VerifyDigest(contract) != nil || contract.Digest != input.ContractDigest || contract.ProjectID != input.ProjectID || contract.ProjectRevision != authority.options.ProjectRevision || contract.Runtime != authority.options.Runtime || contract.Provider != authority.options.Provider || contract.Model != authority.options.Model || contract.Node != authority.options.NodeReference || contract.Worker != authority.options.Worker {
		return api.RuntimePreflightResult{}, setupError(409, "contract_authority_conflict", "execution contract does not match runtime preflight", nil)
	}
	material, err := authority.loadMaterial(ctx, contract.ProjectID, contract.WorkPackageID)
	if err != nil {
		return api.RuntimePreflightResult{}, err
	}
	definition, err := authority.options.Node.Definition(ctx)
	if err != nil {
		return api.RuntimePreflightResult{}, mapDispatchError(err)
	}
	observation, err := authority.options.Node.Observe(ctx)
	if err != nil {
		return api.RuntimePreflightResult{}, mapDispatchError(err)
	}
	eligibility := computenode.Evaluate(definition, observation, nodeRequirements(material, contract), authority.options.Now().UTC())
	reasons := append([]string(nil), eligibility.Reasons...)
	workspaceReady := false
	if eligibility.Eligible {
		request, requestErr := authority.workspaceRequest(contract.ContractID, material, observation)
		if requestErr != nil {
			reasons = append(reasons, "workspace_request_invalid")
		} else if report, preflightErr := authority.options.Workspace.Preflight(ctx, request); preflightErr != nil || !report.Ready {
			reasons = append(reasons, "workspace_preflight_failed")
		} else {
			workspaceReady = true
		}
	}
	for _, gate := range contract.RequiredGates {
		if !configuredGate(gate) {
			reasons = append(reasons, "required_gate_unavailable:"+gate.GateID)
		}
	}
	sort.Strings(reasons)
	return api.RuntimePreflightResult{
		Ready:               eligibility.Eligible && workspaceReady && len(reasons) == 0,
		RuntimeCapabilities: capabilityStrings(authority.options.RuntimeCapabilities), NodeEligibility: eligibility.Eligible,
		WorkspaceReady: workspaceReady, ReasonCodes: reasons,
	}, nil
}

func (authority *LocalDispatchAuthority) prepare(ctx context.Context, projectID, workPackageID, operationKey string, requested []executioncontract.Capability, createdAt time.Time) (preparedDispatch, error) {
	if projectID != authority.options.AuthorizedProjectID {
		return preparedDispatch{}, setupError(409, "project_authority_conflict", "project is not execution-authorized", nil)
	}
	material, err := authority.loadMaterial(ctx, projectID, workPackageID)
	if err != nil {
		return preparedDispatch{}, err
	}
	work := material.graph.Definitions[workPackageID]
	if work.Record.TradeReference == nil {
		return preparedDispatch{}, setupError(409, "trade_authority_conflict", "work package has no immutable trade reference", nil)
	}
	trade, err := authority.options.Registry.GetTradeDefinition(ctx, work.Record.TradeReference.ID, work.Record.TradeReference.Version)
	if err != nil || trade.ContentHash != work.Record.TradeReference.Digest || trade.Lifecycle != storage.RegistryActive {
		return preparedDispatch{}, setupError(409, "trade_authority_conflict", "work package trade is unavailable", err)
	}
	worker, err := authority.options.Registry.GetWorkerProfile(ctx, authority.options.Worker.ID, authority.options.Worker.Version)
	if err != nil || worker.ContentHash != authority.options.Worker.Digest || worker.Lifecycle != storage.RegistryActive {
		return preparedDispatch{}, setupError(409, "worker_authority_conflict", "local worker is unavailable", err)
	}
	if worker.TradeID != trade.TradeID || worker.TradeVersion != trade.Version {
		return preparedDispatch{}, setupError(409, "worker_authority_conflict", "local worker does not match the work trade", nil)
	}
	authorizedCapabilities, err := requestedCapabilities(work.Record, material.graph.Task)
	if err != nil {
		return preparedDispatch{}, err
	}
	if len(requested) > 0 && !sameCapabilities(requested, authorizedCapabilities) {
		return preparedDispatch{}, setupError(409, "runtime_capability_conflict", "confirmed capabilities do not match portable work authority", nil)
	}
	requested = authorizedCapabilities
	if !capabilitiesAvailable(requested, authority.options.RuntimeCapabilities) {
		return preparedDispatch{}, setupError(409, "runtime_capability_conflict", "requested capabilities are unavailable", nil)
	}
	compiled, err := contextcompiler.Compile(ctx, material.layout, contextcompiler.Request{
		ProjectID: projectID, WorkPackageID: workPackageID, TradeReference: *work.Record.TradeReference, Sources: material.sources,
	})
	if err != nil {
		return preparedDispatch{}, mapDispatchError(err)
	}
	ids := dispatchIdentities(projectID, material.task.TaskID, material.task.TaskRevision, material.task.GraphRevision, workPackageID, operationKey)
	instruction, instructionDigest, err := instructionBundle(material.graph.Task, work.Record)
	if err != nil {
		return preparedDispatch{}, err
	}
	budget := defaultExecutionBudget()
	createdAt = createdAt.UTC()
	contractRequest := executioncontract.BuildRequest{
		ContractID: ids.contractID, Version: 1, Task: material.graph.Task, TaskDigest: material.task.TaskRecordHash,
		Graph: material.graph.Revision, GraphDigest: material.task.GraphRecordHash, WorkPackage: work.Record, WorkPackageDigest: work.RecordDigest,
		ExecutionID: ids.executionID, ProjectRevision: authority.options.ProjectRevision, Trade: *work.Record.TradeReference,
		Worker: authority.options.Worker, WorkerProfile: worker,
		Instruction:   executioncontract.BindingReference{ID: worker.InstructionID, Version: worker.InstructionVer, Digest: instructionDigest},
		ContextDigest: compiled.Bundle.Manifest.ContextDigest, Runtime: authority.options.Runtime, Provider: authority.options.Provider,
		Model: authority.options.Model, Node: authority.options.NodeReference, RequestedCapabilities: requested,
		PolicyLayers: defaultPolicyLayers(authority.options.RuntimeCapabilities, budget), RequestedBudget: budget,
		CreatedAt: createdAt, NotBefore: createdAt, Deadline: createdAt.Add(localContractWallClock), CreatedBy: "actor:desktop-scheduler",
	}
	if existing, getErr := authority.options.Contracts.GetExecutionContract(ctx, ids.contractID, 1); getErr == nil {
		var prior executioncontract.Contract
		if json.Unmarshal(existing.ContractJSON, &prior) != nil || executioncontract.VerifyDigest(prior) != nil {
			return preparedDispatch{}, setupError(409, "contract_authority_conflict", "stored execution contract is invalid", nil)
		}
		contractRequest.CreatedAt, contractRequest.NotBefore, contractRequest.Deadline = prior.CreatedAt, prior.NotBefore, prior.Deadline
	} else if !errors.Is(getErr, storage.ErrNotFound) {
		return preparedDispatch{}, getErr
	}
	contract, _, err := executioncontract.Build(contractRequest)
	if err != nil {
		return preparedDispatch{}, mapDispatchError(err)
	}
	definition, err := authority.options.Node.Definition(ctx)
	if err != nil {
		return preparedDispatch{}, err
	}
	observation, err := authority.options.Node.Observe(ctx)
	if err != nil {
		return preparedDispatch{}, err
	}
	selectionBudget := orchestration.DispatchBudget{MaxTokens: budget.MaxTokens, MaxCostMicros: budget.MaxCostMicros, MaxWallClock: time.Duration(budget.MaxWallClockSeconds) * time.Second, MaxConcurrentWorker: budget.MaxConcurrentWorkers}
	evidence := gateEvidence(contract.RequiredGates)
	candidate := orchestration.Candidate{Worker: worker, Trade: trade, Runtime: authority.options.Runtime, RuntimeAvailable: true, RuntimeCapabilities: append([]executioncontract.Capability(nil), authority.options.RuntimeCapabilities...), Node: definition, Observation: observation}
	request := scheduler.DispatchRequest{
		Graph: material.graph, Events: material.events,
		Selection: orchestration.SelectionRequest{
			Task: material.task, Node: material.node, WorkPackage: work.Record, WorkPackageDigest: work.RecordDigest,
			Risk: workRisk(material.graph.Task, work.Record), RequestedBudget: selectionBudget, RequiredRuntime: requested,
			RequiredGates: contract.RequiredGates,
			Policy:        orchestration.SelectionPolicy{AllowedCapabilities: append([]executioncontract.Capability(nil), authority.options.RuntimeCapabilities...), MaximumRisk: orchestration.RiskHigh, MaximumBudget: selectionBudget, GateEvidence: evidence},
			Candidates:    []orchestration.Candidate{candidate}, Now: authority.options.Now().UTC(),
		},
		Binding: dispatchbinding.BindAndPlanRequest{
			Layout:   material.layout,
			Context:  contextcompiler.Request{ProjectID: projectID, WorkPackageID: workPackageID, TradeReference: *work.Record.TradeReference, Sources: material.sources},
			Contract: contractRequest,
			Plan: orchestration.PlanRequest{
				AssignmentID: ids.assignmentID, AttemptID: ids.attemptID, ProjectID: projectID, TaskID: material.task.TaskID,
				TaskRevision: material.task.TaskRevision, GraphRevision: material.task.GraphRevision, WorkPackageID: workPackageID,
				ExecutionID: ids.executionID, WorkerID: worker.WorkerID, NodeID: definition.NodeID,
				IdempotencyDigest: dispatchDigest("assignment", ids.contractID), OperationID: ids.operationID,
				OperationDigest: dispatchDigest("plan", ids.contractID), AuditID: ids.auditID, ActorID: "actor:desktop-scheduler",
			},
		},
		InstructionBundle: instruction,
	}
	request.Workspace, err = authority.workspaceRequest(contract.ContractID, material, observation)
	if err != nil {
		return preparedDispatch{}, err
	}
	return preparedDispatch{request: request, contract: contract}, nil
}

func (authority *LocalDispatchAuthority) loadMaterial(ctx context.Context, projectID, workPackageID string) (dispatchMaterial, error) {
	if ctx == nil || projectID != authority.options.AuthorizedProjectID {
		return dispatchMaterial{}, setupError(409, "project_authority_conflict", "project is not execution-authorized", nil)
	}
	registration, err := authority.options.Projects.GetProject(ctx, projectID)
	if err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	layout, err := project.NewLayout(registration.RootPath)
	if err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	workPath, err := project.WorkPackageDefinitionRelativePath(workPackageID)
	if err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	workDecoded, err := project.ReadPortableRecord(layout, workPath)
	if err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	work, ok := workDecoded.Value.(*project.WorkPackageDefinition)
	if !ok || work.ProjectID != projectID || work.WorkPackageID != workPackageID || work.TaskID == "" {
		return dispatchMaterial{}, setupError(409, "portable_authority_conflict", "work package authority is invalid", nil)
	}
	taskPath, _ := project.TaskRevisionRelativePath(work.TaskID, work.TaskRevision)
	graphPath, _ := project.DependencyGraphRevisionRelativePath(work.TaskID, work.TaskRevision, work.GraphRevision)
	taskDecoded, err := project.ReadPortableRecord(layout, taskPath)
	if err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	graphDecoded, err := project.ReadPortableRecord(layout, graphPath)
	if err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	taskRecord, taskOK := taskDecoded.Value.(*project.TaskRevision)
	graphRecord, graphOK := graphDecoded.Value.(*project.DependencyGraphRevision)
	if !taskOK || !graphOK {
		return dispatchMaterial{}, setupError(409, "portable_authority_conflict", "task graph records are invalid", nil)
	}
	taskProjection, err := authority.options.Tasks.GetProjectTask(ctx, projectID, work.TaskID, work.TaskRevision)
	if err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	if taskProjection.GraphRevision != work.GraphRevision || taskProjection.TaskRecordID != taskRecord.RecordID || taskProjection.TaskRecordHash != taskDecoded.Digest || taskProjection.TaskRecordPath != taskPath || taskProjection.GraphRecordID != graphRecord.RecordID || taskProjection.GraphRecordHash != graphDecoded.Digest || taskProjection.GraphRecordPath != graphPath {
		return dispatchMaterial{}, setupError(409, "projection_authority_conflict", "task projection does not match portable records", nil)
	}
	definitions := make(map[string]orchestration.WorkPackageDefinition, len(graphRecord.Members))
	for _, member := range graphRecord.Members {
		definitionPath, pathErr := project.WorkPackageDefinitionRelativePath(member.WorkPackageID)
		if pathErr != nil {
			return dispatchMaterial{}, mapDispatchError(pathErr)
		}
		decoded, readErr := project.ReadPortableRecord(layout, definitionPath)
		if readErr != nil {
			return dispatchMaterial{}, mapDispatchError(readErr)
		}
		definition, valid := decoded.Value.(*project.WorkPackageDefinition)
		if !valid || definition.RecordID != member.DefinitionRecordID || decoded.Digest != member.DefinitionDigest {
			return dispatchMaterial{}, setupError(409, "portable_authority_conflict", "graph member does not match its portable definition", nil)
		}
		definitions[member.WorkPackageID] = orchestration.WorkPackageDefinition{Record: *definition, RecordDigest: decoded.Digest, RecordPath: definitionPath}
	}
	graph := orchestration.Graph{Task: *taskRecord, Revision: *graphRecord, Definitions: definitions}
	if err := orchestration.ValidateGraph(ctx, graph); err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	nodes, err := authority.listNodes(ctx, taskProjection)
	if err != nil {
		return dispatchMaterial{}, err
	}
	var node storage.ProjectTaskNodeProjection
	for _, candidate := range nodes {
		if candidate.WorkPackageID == workPackageID {
			node = candidate
			break
		}
	}
	if node.WorkPackageID == "" || node.DefinitionRecordID != work.RecordID || node.DefinitionHash != workDecoded.Digest || node.DefinitionPath != workPath {
		return dispatchMaterial{}, setupError(409, "projection_authority_conflict", "work package projection does not match portable authority", nil)
	}
	events, err := authority.loadEvents(ctx, layout, projectID, definitions)
	if err != nil {
		return dispatchMaterial{}, err
	}
	snapshot, err := orchestration.ReduceReadiness(ctx, graph, events)
	if err != nil {
		return dispatchMaterial{}, mapDispatchError(err)
	}
	if taskProjection.State != string(snapshot.State) || taskProjection.ExplanationCode != snapshot.ExplanationCode || taskProjection.EventWatermark != snapshot.EventWatermark || node.Readiness != string(snapshotNode(snapshot, workPackageID).State) {
		return dispatchMaterial{}, setupError(409, "projection_authority_conflict", "readiness projection is stale", nil)
	}
	sources := []contextcompiler.SourceSpec{
		{RelativePath: taskPath, Kind: contextcompiler.SourceTaskGraph, Privacy: contextcompiler.PrivacyProject},
		{RelativePath: graphPath, Kind: contextcompiler.SourceTaskGraph, Privacy: contextcompiler.PrivacyProject},
	}
	sourceDigest, err := contextcompiler.SourceSetDigest(sources)
	if err != nil {
		return dispatchMaterial{}, err
	}
	return dispatchMaterial{registration: registration, layout: layout, task: taskProjection, node: node, graph: graph, events: events, sources: sources, sourceDigest: sourceDigest}, nil
}

func (authority *LocalDispatchAuthority) listTasks(ctx context.Context, projectID string) ([]storage.ProjectTaskProjection, error) {
	page := storage.PageRequest{Limit: storage.MaxAdminPageLimit}
	items := []storage.ProjectTaskProjection{}
	for {
		result, err := authority.options.Tasks.ListProjectTasks(ctx, storage.ProjectTaskQuery{ProjectID: projectID, Page: page})
		if err != nil {
			return nil, err
		}
		items = append(items, result.Items...)
		if result.NextCursor == nil {
			return items, nil
		}
		page.Cursor = *result.NextCursor
	}
}

func (authority *LocalDispatchAuthority) listNodes(ctx context.Context, task storage.ProjectTaskProjection) ([]storage.ProjectTaskNodeProjection, error) {
	page := storage.PageRequest{Limit: storage.MaxAdminPageLimit}
	items := []storage.ProjectTaskNodeProjection{}
	for {
		result, err := authority.options.TaskNodes.ListProjectTaskNodes(ctx, storage.ProjectTaskNodeQuery{ProjectID: task.ProjectID, TaskID: task.TaskID, TaskRevision: task.TaskRevision, GraphRevision: task.GraphRevision, Page: page})
		if err != nil {
			return nil, err
		}
		items = append(items, result.Items...)
		if result.NextCursor == nil {
			return items, nil
		}
		page.Cursor = *result.NextCursor
	}
}

func (authority *LocalDispatchAuthority) loadEvents(ctx context.Context, layout project.Layout, projectID string, definitions map[string]orchestration.WorkPackageDefinition) ([]orchestration.Event, error) {
	page := storage.PageRequest{Limit: storage.MaxAdminPageLimit}
	items := []orchestration.Event{}
	for {
		result, err := authority.options.Events.ListProjectEvents(ctx, storage.ProjectEventQuery{ProjectID: projectID, Page: page})
		if err != nil {
			return nil, err
		}
		for _, event := range result.Items {
			if event.Status != "accepted" {
				continue
			}
			if _, member := definitions[event.WorkPackageID]; !member {
				continue
			}
			decoded, err := project.ReadPortableRecord(layout, event.RecordPath)
			if err != nil {
				return nil, err
			}
			record, ok := decoded.Value.(*project.WorkEvent)
			if !ok || record.RecordID != event.EventID || decoded.Digest != event.RecordHash {
				return nil, setupError(409, "portable_authority_conflict", "event projection does not match portable authority", nil)
			}
			items = append(items, orchestration.Event{Record: *record, RecordDigest: decoded.Digest})
		}
		if result.NextCursor == nil {
			return items, nil
		}
		page.Cursor = *result.NextCursor
	}
}

func (authority *LocalDispatchAuthority) approval(ctx context.Context, task storage.ProjectTaskProjection) (storage.LocalOperatorOperation, error) {
	operation, err := authority.options.Operations.FindLatestLocalOperatorOperation(ctx, "approve_task_graph", task.ProjectID, task.TaskID)
	if err != nil {
		return storage.LocalOperatorOperation{}, err
	}
	var approval api.TaskGraphApprovalResult
	if json.Unmarshal(operation.ResultJSON, &approval) != nil || approval.ProjectID != task.ProjectID || approval.TaskID != task.TaskID || approval.TaskRevision != task.TaskRevision || approval.GraphRevision != task.GraphRevision || approval.ApprovalDigest != task.GraphRecordHash {
		return storage.LocalOperatorOperation{}, setupError(409, "approval_authority_conflict", "task approval does not match current graph authority", nil)
	}
	return operation, nil
}

func (authority *LocalDispatchAuthority) workspaceRequest(contractID string, material dispatchMaterial, observation computenode.Observation) (workspace.PreflightRequest, error) {
	ids := identitiesFromContract(contractID)
	branch, err := workspace.AttemptBranchName(ids.attemptID)
	if err != nil {
		return workspace.PreflightRequest{}, err
	}
	required := int64(1)
	work := material.graph.Definitions[material.node.WorkPackageID].Record
	if work.Resources != nil && work.Resources.MinimumDiskMB > 0 {
		required = work.Resources.MinimumDiskMB * 1024 * 1024
	}
	return workspace.PreflightRequest{
		WorkspaceID: ids.workspaceID, OwnerID: ids.assignmentID, AttemptID: ids.attemptID,
		ProjectSyncRoot: material.registration.RootPath, RepositoryRoot: material.registration.RootPath, WorktreeBase: authority.options.WorktreeBase,
		BranchName: branch, BaseCommit: authority.options.BaseCommit, RequiredDiskBytes: required, AvailableDiskBytes: observation.Available.DiskBytes,
		IdempotencyKeyDigest: dispatchDigest("workspace", contractID),
	}, nil
}

type dispatchIDs struct {
	contractID, assignmentID, attemptID, executionID, workspaceID, operationID, auditID string
}

func dispatchIdentities(projectID, taskID string, taskRevision, graphRevision int64, workPackageID, operationKey string) dispatchIDs {
	seed := strings.Join([]string{projectID, taskID, fmt.Sprint(taskRevision), fmt.Sprint(graphRevision), workPackageID, operationKey}, "\x00")
	contractID := "contract:" + dispatchDigest("contract", seed)[:32]
	return identitiesFromContract(contractID)
}

func identitiesFromContract(contractID string) dispatchIDs {
	key := dispatchDigest("identity", contractID)[:32]
	return dispatchIDs{
		contractID: contractID, assignmentID: "assignment:" + key, attemptID: "attempt:" + key,
		executionID: "execution:" + key, workspaceID: "workspace:" + key,
		operationID: "operation:plan-" + key, auditID: "audit:plan-" + key,
	}
}

func requestedCapabilities(work project.WorkPackageDefinition, task project.TaskRevision) ([]executioncontract.Capability, error) {
	resources := work.Resources
	if resources == nil {
		resources = task.Resources
	}
	values := []string{"inspect"}
	if resources != nil && len(resources.RequiredCapabilities) > 0 {
		values = resources.RequiredCapabilities
	}
	result := make([]executioncontract.Capability, 0, len(values))
	seen := map[executioncontract.Capability]bool{}
	for _, value := range values {
		capability := executioncontract.Capability(value)
		switch capability {
		case executioncontract.CapabilityInspect, executioncontract.CapabilityWrite, executioncontract.CapabilityShell, executioncontract.CapabilityTest, executioncontract.CapabilityBranch:
		default:
			return nil, setupError(409, "runtime_capability_conflict", "work package requests an unsupported capability", nil)
		}
		if !seen[capability] {
			seen[capability] = true
			result = append(result, capability)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func defaultExecutionBudget() executioncontract.BudgetLimits {
	return executioncontract.BudgetLimits{MaxTokens: 50_000, MaxCostMicros: 10_000_000, MaxWallClockSeconds: int64(localContractWallClock / time.Second), MaxRetries: 1, MaxToolCalls: 250, MaxConcurrentWorkers: 1}
}

func defaultPolicyLayers(capabilities []executioncontract.Capability, budget executioncontract.BudgetLimits) []executioncontract.PolicyLayer {
	result := make([]executioncontract.PolicyLayer, 0, 7)
	for _, name := range []string{"project", "runtime", "task", "trade", "user", "work_package", "worker"} {
		result = append(result, executioncontract.PolicyLayer{Name: name, AllowedCapabilities: append([]executioncontract.Capability(nil), capabilities...), InspectPaths: []string{"."}, WritePaths: []string{"."}, BudgetCeiling: budget})
	}
	return result
}

func workRisk(task project.TaskRevision, work project.WorkPackageDefinition) orchestration.RiskLevel {
	rank := 0
	for _, value := range append(append([]project.RiskDimension(nil), task.Risk...), work.Risk...) {
		switch value.Level {
		case project.RiskCritical:
			rank = 3
		case project.RiskHigh:
			if rank < 2 {
				rank = 2
			}
		case project.RiskMedium:
			if rank < 1 {
				rank = 1
			}
		}
	}
	return []orchestration.RiskLevel{orchestration.RiskLow, orchestration.RiskModerate, orchestration.RiskHigh, orchestration.RiskCritical}[rank]
}

func nodeRequirements(material dispatchMaterial, contract executioncontract.Contract) computenode.Requirements {
	access := computenode.RepositoryRead
	for _, capability := range contract.Permissions.Capabilities {
		if capability == executioncontract.CapabilityWrite || capability == executioncontract.CapabilityBranch {
			access = computenode.RepositoryWrite
		}
	}
	requirements := computenode.Requirements{RepositoryAccess: access, Runtime: contract.Runtime}
	work := material.graph.Definitions[contract.WorkPackageID].Record
	if work.Resources != nil {
		if len(work.Resources.AllowedOS) == 1 {
			requirements.OS = work.Resources.AllowedOS[0]
		}
		if len(work.Resources.AllowedArchitectures) == 1 {
			requirements.Architecture = work.Resources.AllowedArchitectures[0]
		}
		requirements.Minimum.MemoryBytes = work.Resources.MinimumMemoryMB * 1024 * 1024
		requirements.Minimum.DiskBytes = work.Resources.MinimumDiskMB * 1024 * 1024
	}
	return requirements
}

func instructionBundle(task project.TaskRevision, work project.WorkPackageDefinition) ([]byte, string, error) {
	value := struct {
		Schema             string   `json:"schema"`
		TaskObjective      string   `json:"task_objective"`
		WorkObjective      string   `json:"work_objective"`
		Deliverables       []string `json:"deliverables"`
		AcceptanceCriteria []string `json:"acceptance_criteria"`
		OutputSchema       string   `json:"output_schema"`
	}{"syncgate.instruction-bundle.v1", task.Objective, work.Objective, append([]string(nil), work.Deliverables...), append([]string(nil), work.AcceptanceCriteria...), "syncgate.result-envelope.v1"}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return raw, dispatchDigestBytes(raw), nil
}

func contractPreviewResult(contract executioncontract.Contract) api.ExecutionContractPreviewResult {
	capabilities := make([]string, len(contract.Permissions.Capabilities))
	for index, value := range contract.Permissions.Capabilities {
		capabilities[index] = string(value)
	}
	gates := make([]string, len(contract.RequiredGates))
	for index, value := range contract.RequiredGates {
		gates[index] = value.GateID
	}
	return api.ExecutionContractPreviewResult{ContractID: contract.ContractID, ContractVersion: contract.Version, ContractDigest: contract.Digest, EffectiveCapabilityIDs: capabilities, RequiredGateIDs: gates, MaxTokens: contract.Budget.MaxTokens, MaxCostMicros: contract.Budget.MaxCostMicros, MaxWallClockSeconds: contract.Budget.MaxWallClockSeconds, PreviewOnly: true}
}

func configuredGate(gate executioncontract.GateRequirement) bool {
	switch gate.GateID {
	case "gate:tests", "gate:human-review", "gate:security-review", "gate:data-review", "gate:operations-approval":
		return gate.Version == 1 && gate.Digest == builtInGateDigest(gate.GateID)
	default:
		return false
	}
}

func gateEvidence(gates []executioncontract.GateRequirement) []orchestration.GateEvidence {
	result := []orchestration.GateEvidence{}
	for _, gate := range gates {
		if configuredGate(gate) {
			result = append(result, orchestration.GateEvidence{GateID: gate.GateID, Version: gate.Version, Digest: gate.Digest, EvidenceID: "evidence:configured-gate", EvidenceDigest: dispatchDigest("gate", gate.Digest)})
		}
	}
	return result
}

func capabilitiesAvailable(requested, available []executioncontract.Capability) bool {
	set := map[executioncontract.Capability]bool{}
	for _, value := range available {
		set[value] = true
	}
	for _, value := range requested {
		if !set[value] {
			return false
		}
	}
	return true
}

func sameCapabilities(left, right []executioncontract.Capability) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]executioncontract.Capability(nil), left...)
	rightCopy := append([]executioncontract.Capability(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool { return leftCopy[i] < leftCopy[j] })
	sort.Slice(rightCopy, func(i, j int) bool { return rightCopy[i] < rightCopy[j] })
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] || index > 0 && leftCopy[index] == leftCopy[index-1] {
			return false
		}
	}
	return true
}

func capabilityStrings(values []executioncontract.Capability) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	sort.Strings(result)
	return result
}

func noticeCodes(values []contextcompiler.Notice) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Code)
	}
	sort.Strings(result)
	return result
}

func snapshotNode(snapshot orchestration.Snapshot, workPackageID string) orchestration.NodeReadiness {
	for _, node := range snapshot.Nodes {
		if node.WorkPackageID == workPackageID {
			return node
		}
	}
	return orchestration.NodeReadiness{}
}

func dispatchDigest(parts ...string) string {
	return dispatchDigestBytes([]byte(strings.Join(parts, "\x00")))
}
func dispatchDigestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validDispatchDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func mapDispatchError(err error) error {
	switch {
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, project.ErrRecordNotFound):
		return setupError(404, "authority_not_found", "required dispatch authority was not found", err)
	case errors.Is(err, executioncontract.ErrPrivilegeEscalation), errors.Is(err, executioncontract.ErrInvalidContract), errors.Is(err, contextcompiler.ErrScopeViolation), errors.Is(err, orchestration.ErrInvalidGraph):
		return setupError(409, "dispatch_authority_conflict", "dispatch authority validation failed", err)
	default:
		return err
	}
}

var _ scheduler.WorkSource = (*LocalDispatchAuthority)(nil)
