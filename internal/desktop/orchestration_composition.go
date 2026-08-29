package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"syncgate/internal/codexruntime"
	"syncgate/internal/computenode"
	"syncgate/internal/config"
	"syncgate/internal/daemon"
	"syncgate/internal/dispatchbinding"
	"syncgate/internal/executioncontract"
	"syncgate/internal/identity"
	"syncgate/internal/integrationgate"
	"syncgate/internal/orchestration"
	"syncgate/internal/projector"
	"syncgate/internal/resultintake"
	"syncgate/internal/runtimecontract"
	"syncgate/internal/scheduler"
	"syncgate/internal/storage"
	"syncgate/internal/workhistory"
	"syncgate/internal/workspace"
)

type LocalOrchestrationOptions struct {
	Base        Roots
	Config      config.Config
	Store       storage.Store
	Identity    identity.DeviceIdentity
	Credentials ProviderCredentials
	Now         func() time.Time
	Executor    codexruntime.Executor
	Source      scheduler.WorkSource
	Observe     ResourceObserver
	Binder      scheduler.BindingPlanner
}

type LocalOrchestration struct {
	Scheduler           *scheduler.Scheduler
	Administration      *LocalOrchestrationAdministration
	Runtime             *codexruntime.Adapter
	Node                *LocalNodeProvider
	Workspace           *workspace.GitWorktreeManager
	Results             LocalResultStore
	RuntimeRef          executioncontract.BindingReference
	ProviderRef         executioncontract.BindingReference
	ModelRef            executioncontract.BindingReference
	NodeRef             executioncontract.BindingReference
	AuthorizedProjectID string
}

func BuildLocalOrchestration(ctx context.Context, options LocalOrchestrationOptions) (*LocalOrchestration, error) {
	if ctx == nil || options.Store == nil || options.Now == nil {
		return nil, errors.New("local orchestration composition is incomplete")
	}
	roots, err := RootsFromConfig(options.Base, options.Config)
	if err != nil {
		return nil, err
	}
	manager := ExecutionManager{Base: options.Base, Credentials: options.Credentials, Now: options.Now}
	record, err := manager.ValidateEnabledAuthorization(ctx, options.Config, roots)
	if err != nil {
		return nil, err
	}
	authorizedProjectID, err := registeredProjectForRoot(ctx, options.Store.ProjectRegistrations(), record.ProjectRoot)
	if err != nil {
		return nil, err
	}
	worktreeBase := filepath.Join(roots.WorktreeRoot, "allocated")
	codexHome := filepath.Join(roots.RuntimeCacheDir, "codex-home")
	runtimeState := filepath.Join(roots.DataDir, "orchestration", "runtime")
	resultRoot := filepath.Join(roots.DataDir, "orchestration", "results")
	for _, directory := range []string{worktreeBase, codexHome, runtimeState, resultRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}

	runtimeRef := executioncontract.BindingReference{ID: "runtime:codex-local", Version: 1, Digest: localHash("runtime", record.RuntimeDigest)}
	providerRef := executioncontract.BindingReference{ID: "provider:" + record.ProviderID, Version: 1, Digest: localHash("provider", record.ProviderID)}
	modelRef := executioncontract.BindingReference{ID: "model:" + record.ModelID, Version: 1, Digest: localHash("model", record.ModelID)}
	nodeID := "node:local-" + localHash(string(options.Identity.DeviceID))[:24]
	node, err := NewLocalNodeProvider(LocalNodeOptions{
		NodeID: nodeID, Runtime: runtimeRef, ResourceRoot: roots.WorktreeRoot,
		MaxConcurrent: options.Config.Node.Execution.MaxConcurrent, Now: options.Now, Observe: options.Observe,
	})
	if err != nil {
		return nil, err
	}
	nodeDefinition, err := node.Definition(ctx)
	if err != nil {
		return nil, err
	}
	nodeRef := executioncontract.BindingReference{ID: nodeDefinition.NodeID, Version: nodeDefinition.Version, Digest: nodeDefinition.DefinitionDigest}
	workspaces, err := workspace.NewGitWorktreeManager(workspace.GitManagerConfig{
		ProjectSyncRoot: record.ProjectRoot, RepositoryRoot: record.ProjectRoot,
		WorktreeBase: worktreeBase, AllowedBaseCommits: []string{record.HeadDigest},
	})
	if err != nil {
		return nil, err
	}
	control := orchestration.ControlService{Store: options.Store.OrchestrationControl(), Now: options.Now}
	adapter, err := codexruntime.New(codexruntime.Config{
		Enabled: true, Executable: record.RuntimeExecutable, CodexHome: codexHome, StateRoot: runtimeState,
		Runtime: runtimeRef, Provider: providerRef, Model: modelRef, Node: nodeRef,
		Capabilities: []executioncontract.Capability{
			executioncontract.CapabilityInspect, executioncontract.CapabilityWrite,
			executioncontract.CapabilityShell, executioncontract.CapabilityTest, executioncontract.CapabilityBranch,
		},
		MaxConcurrent: options.Config.Node.Execution.MaxConcurrent, Now: options.Now, Executor: options.Executor,
		Workspace: func(resolveCtx context.Context, binding codexruntime.WorkspaceBinding) (string, error) {
			return resolveRuntimeWorkspace(resolveCtx, workspaces, control, binding)
		},
	})
	if err != nil {
		return nil, err
	}
	var binder scheduler.BindingPlanner = dispatchbinding.ContractBindingService{
		Contracts: executioncontract.Service{Store: options.Store.ExecutionContracts()}, Control: control, Now: options.Now,
	}
	if options.Binder != nil {
		binder = options.Binder
	}
	var workSource scheduler.WorkSource
	if options.Source != nil {
		workSource = authorizedWorkSource{source: options.Source, projectID: authorizedProjectID}
	}
	schedulerValue, err := scheduler.New(scheduler.Config{
		Binder: binder, Control: control, Workspace: workspaces, Source: workSource,
		ActorID: "scheduler:desktop", MaxConcurrent: options.Config.Node.Execution.MaxConcurrent,
		StartPaused: true,
		ResolveRuntime: func(_ context.Context, id string) (runtimecontract.Adapter, error) {
			if id != runtimeRef.ID {
				return nil, errors.New("runtime is unavailable")
			}
			return adapter, nil
		},
		ResolveNode: func(_ context.Context, id string) (computenode.Provider, error) {
			if id != nodeRef.ID {
				return nil, errors.New("node is unavailable")
			}
			return node, nil
		},
	})
	if err != nil {
		return nil, err
	}
	results := LocalResultStore{Root: resultRoot}
	projection := &projector.Projector{Store: options.Store, Now: options.Now}
	history, err := workhistory.New(options.Store, projection)
	if err != nil {
		return nil, err
	}
	runtimes := singleRuntimeResolver{reference: runtimeRef, adapter: adapter}
	gate := &integrationgate.Service{
		Contracts: options.Store.ExecutionContracts(),
		Intake:    resultintake.Service{Contracts: options.Store.ExecutionContracts(), Intake: options.Store.ResultIntake(), Now: options.Now},
		Control:   control, History: history, HistoryRoots: projectHistoryRoots{projects: options.Store.ProjectRegistrations()},
		Runtimes: runtimes, Results: results, Contents: results, Workspaces: workspaces, Tests: unavailableTestRunner{},
	}
	administration := NewLocalOrchestrationAdministration(LocalAdministrationOptions{
		Scheduler: schedulerValue, Control: control, Inventory: options.Store.OrchestrationControl(),
		Projects: options.Store.ProjectRegistrations(), Operations: options.Store.LocalProjectOperations(),
		Tasks: options.Store.ProjectTasks(), TaskNodes: options.Store.ProjectTaskNodes(), Registry: options.Store.Registry(),
		AuthorizedProjectID: authorizedProjectID, MaxConcurrent: options.Config.Node.Execution.MaxConcurrent,
		Node: node, Gate: gate, Now: options.Now,
	})
	return &LocalOrchestration{
		Scheduler: schedulerValue, Administration: administration, Runtime: adapter, Node: node,
		Workspace: workspaces, Results: results, RuntimeRef: runtimeRef, ProviderRef: providerRef, ModelRef: modelRef, NodeRef: nodeRef,
		AuthorizedProjectID: authorizedProjectID,
	}, nil
}

const maxCompositionProjectRegistrations = 1000

func registeredProjectForRoot(ctx context.Context, projects storage.ProjectRegistrationStore, root string) (string, error) {
	if projects == nil || root == "" {
		return "", errors.New("authorized project registration is unavailable")
	}
	cursor := storage.PageCursor{}
	seen := 0
	for {
		page, err := projects.ListProjects(ctx, storage.PageRequest{Limit: storage.MaxAdminPageLimit, Cursor: cursor})
		if err != nil {
			return "", fmt.Errorf("list authorized project registrations: %w", err)
		}
		for _, registration := range page.Items {
			seen++
			if sameLocalPath(registration.RootPath, root) {
				return registration.ProjectID, nil
			}
		}
		if page.NextCursor == nil {
			break
		}
		if seen >= maxCompositionProjectRegistrations {
			return "", errors.New("authorized project registration inventory exceeds the local bound")
		}
		cursor = *page.NextCursor
	}
	return "", errors.New("execution-authorized root is not a registered project")
}

func sameLocalPath(left, right string) bool {
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(leftAbsolute), filepath.Clean(rightAbsolute))
}

type authorizedWorkSource struct {
	source    scheduler.WorkSource
	projectID string
}

func (source authorizedWorkSource) Pending(ctx context.Context) ([]scheduler.DispatchRequest, error) {
	if source.source == nil {
		return nil, nil
	}
	requests, err := source.source.Pending(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]scheduler.DispatchRequest, 0, len(requests))
	for _, request := range requests {
		if request.Binding.Plan.ProjectID == source.projectID {
			filtered = append(filtered, request)
		}
	}
	return filtered, nil
}

func resolveRuntimeWorkspace(ctx context.Context, workspaces *workspace.GitWorktreeManager, control orchestration.ControlService, binding codexruntime.WorkspaceBinding) (string, error) {
	workspaceValue, inspectErr := workspaces.Inspect(ctx, binding.WorkspaceID)
	if inspectErr != nil || workspaceValue.OwnerID == "" {
		return "", workspace.ErrWorkspaceMissing
	}
	snapshot, controlErr := control.GetAssignment(ctx, workspaceValue.OwnerID)
	if controlErr != nil || snapshot.Attempt.AttemptID != binding.AttemptID || snapshot.Attempt.LeaseGeneration != binding.LeaseGeneration || snapshot.Lease == nil || snapshot.Lease.FencingDigest != binding.FencingDigest {
		return "", orchestration.ErrStaleFence
	}
	return workspaces.ResolveLocalPath(ctx, binding.WorkspaceID)
}

func NewOrchestrationComposer(base Roots) daemon.OrchestrationComposer {
	return func(ctx context.Context, cfg config.Config, store storage.Store, deviceIdentity identity.DeviceIdentity) (daemon.OrchestrationComponents, error) {
		composition, err := BuildLocalOrchestration(ctx, LocalOrchestrationOptions{
			Base: base, Config: cfg, Store: store, Identity: deviceIdentity,
			Credentials: ProviderCredentials{ScopeRoot: base.ConfigDir}, Now: func() time.Time { return time.Now().UTC() },
		})
		if err != nil {
			return daemon.OrchestrationComponents{}, err
		}
		return daemon.OrchestrationComponents{Scheduler: composition.Scheduler, Administration: composition.Administration}, nil
	}
}

type singleRuntimeResolver struct {
	reference executioncontract.BindingReference
	adapter   runtimecontract.Adapter
}

func (resolver singleRuntimeResolver) ResolveRuntime(reference executioncontract.BindingReference) (runtimecontract.Adapter, error) {
	if reference != resolver.reference {
		return nil, errors.New("runtime reference is unavailable")
	}
	return resolver.adapter, nil
}

type projectHistoryRoots struct {
	projects storage.ProjectRegistrationStore
}

func (resolver projectHistoryRoots) HistoryRoot(projectID string) (string, error) {
	registration, err := resolver.projects.GetProject(context.Background(), projectID)
	if err != nil {
		return "", err
	}
	return registration.RootPath, nil
}

type unavailableTestRunner struct{}

func (unavailableTestRunner) RunAuthorized(context.Context, executioncontract.Contract, integrationgate.TestCommand) (integrationgate.TestResult, error) {
	return integrationgate.TestResult{}, errors.New("authorized test runner is not configured")
}
