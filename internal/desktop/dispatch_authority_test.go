package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/project"
	"syncgate/internal/scheduler"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	"syncgate/internal/taskspec"
)

func TestLocalDispatchAuthorityBuildsApprovedWorkAndRejectsVisibleProject(t *testing.T) {
	ctx := context.Background()
	manager, _, projectRoot, runtimePath, now := executionFixture(t)
	projectRoot = filepath.Join(filepath.Dir(projectRoot), "dispatch-authority")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	deviceIdentity, err := identity.GenerateDeviceIdentity(bytes.NewReader(bytes.Repeat([]byte{0x63}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	projectID := "project:dispatch-authority"
	shareID := core.ShareID("share-dispatch-authority")
	bootstrap, err := (project.ProjectBootstrapper{Now: func() time.Time { return now.UTC() }}).Bootstrap(project.ProjectBootstrapRequest{
		RootPath: projectRoot, ProjectID: projectID, Name: "Dispatch Authority",
		Authority: project.Authority{DeviceID: string(deviceIdentity.DeviceID), ShareID: string(shareID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, projectRoot, "init")
	gitTestCommand(t, projectRoot, "config", "user.email", "dispatch@example.invalid")
	gitTestCommand(t, projectRoot, "config", "user.name", "Dispatch Test")
	gitTestCommand(t, projectRoot, "config", "core.longpaths", "true")
	gitTestCommand(t, projectRoot, "add", ".agent-project")
	gitTestCommand(t, projectRoot, "commit", "-m", "add dispatch authority fixture")
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
	store, err := sqlite.Open(filepath.Join(roots.DataDir, "dispatch-authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: shareID, Name: "Dispatch Authority", RootPath: projectRoot, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	manifestDecoded, err := project.ReadPortableRecord(bootstrap.Preflight.Layout, project.ManifestRelativePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := manifestDecoded.Value.(*project.ProjectManifest)
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, storage.ProjectRegistration{
		ProjectID: projectID, ShareID: shareID, RootPath: projectRoot, Name: manifest.Name,
		AuthorityDeviceID: deviceIdentity.DeviceID, ManifestRecordID: manifest.RecordID,
		ManifestRecordHash: manifestDecoded.Digest, ManifestPath: project.ManifestRelativePath, RegisteredAt: now.UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	composition, err := BuildLocalOrchestration(ctx, LocalOrchestrationOptions{
		Base: manager.Base, Config: cfg, Store: store, Identity: deviceIdentity, Credentials: manager.Credentials,
		AuthRunner: &recordingCodexAuthRunner{},
		Now:        func() time.Time { return now.UTC() },
		Observe: func(_ string, ceiling int, observedAt time.Time) (MachineResources, error) {
			return MachineResources{CPUMillis: 4000, LogicalCPUs: 4, DiskTotalBytes: 100 << 20, DiskAvailableBytes: 80 << 20, ConfiguredConcurrency: ceiling, ObservedAt: observedAt, ExpiresAt: observedAt.Add(30 * time.Second)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if composition.Dispatch == nil {
		t.Fatal("production dispatch authority was not composed")
	}
	specification := taskspec.Specification{
		Schema: taskspec.Schema, SpecificationID: "spec:dispatch-authority", ProjectID: projectID,
		CreatedAt: now.UTC(), TaskID: "task:dispatch-authority", TaskRevision: 1, GraphRevision: 1,
		Objective: "prove project-backed dispatch authority", Priority: project.TaskPriorityHigh,
		QualityGates: []project.QualityGateReference{{GateID: "gate:human-review", Version: 1, Digest: dispatchDigestBytes([]byte("gate:human-review:v1")), Required: true}},
		Barriers:     []string{"work:dispatch-authority"},
		WorkPackages: []taskspec.WorkPackage{{
			WorkPackageID: "work:dispatch-authority", Objective: "prepare one supervised dispatch", Trade: "codex",
			Scope:        project.WorkScope{Allowed: []string{"."}, Inspect: []string{"."}},
			Deliverables: []string{"dispatch request"}, AcceptanceCriteria: []string{"scheduler remains paused"}, ReviewRequired: true,
			Resources:      &project.ResourceConstraints{RequiredCapabilities: []string{"branch", "inspect", "shell", "test", "write"}},
			TradeReference: &project.RegistryReference{ID: localTradeID, Version: 1, Digest: "a1e30c66fabc6e0ec98b9afaab061ec9ac6d43f1d3295dd64c9a193c1ea1f09b"},
		}},
	}
	raw, err := json.Marshal(specification)
	if err != nil {
		t.Fatal(err)
	}
	specifications := taskspec.FileStore{Root: filepath.Join(roots.DataDir, "orchestration", "task-specifications")}
	stored, err := specifications.Put(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	created, err := composition.Setup.CreateTaskGraph(ctx, api.TaskGraphCreateInput{
		ProjectID: projectID, TaskID: specification.TaskID, TaskRevision: 1, GraphRevision: 1,
		SpecificationID: specification.SpecificationID, SpecificationDigest: stored.Digest, IdempotencyKey: "create-dispatch-authority",
	})
	if err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, projectRoot, "add", ".agent-project")
	gitTestCommand(t, projectRoot, "commit", "-m", "add portable task authority")
	enabled := true
	if _, err := composition.Administration.SetLocalProjectPolicy(ctx, api.LocalProjectPolicyInput{ProjectID: projectID, SchedulingEnabled: &enabled, MaxConcurrent: 1, IdempotencyKey: "policy-dispatch-authority"}); err != nil {
		t.Fatal(err)
	}
	if _, err := composition.Administration.SelectLocalProject(ctx, api.LocalProjectSelectionInput{ProjectID: projectID, IdempotencyKey: "select-dispatch-authority"}); err != nil {
		t.Fatal(err)
	}
	contextResult, err := composition.Setup.ContextPreflight(ctx, api.ContextPreflightInput{
		ProjectID: projectID, WorkPackageID: "work:dispatch-authority", TradeID: localTradeID, TradeVersion: 1,
		TradeDigest: specification.WorkPackages[0].TradeReference.Digest,
	})
	if err != nil || contextResult.ContextDigest == "" || contextResult.SourceSetDigest == "" || contextResult.SourceCount != 3 {
		t.Fatalf("context preflight = %+v, err=%v", contextResult, err)
	}
	worker := composition.Dispatch.options.Worker
	operationKey := "approve-dispatch-authority"
	if _, err := composition.Setup.PreviewExecutionContract(ctx, api.ExecutionContractPreviewInput{
		ProjectID: projectID, TaskID: specification.TaskID, TaskRevision: 1, GraphRevision: 1,
		WorkPackageID: "work:dispatch-authority", WorkerID: worker.ID, WorkerVersion: worker.Version, WorkerDigest: worker.Digest,
		RequestedCapabilityIDs: []string{"inspect"}, IdempotencyKey: "reject-capability-downgrade",
	}); err == nil {
		t.Fatal("contract preview accepted a capability downgrade")
	}
	contract, err := composition.Setup.PreviewExecutionContract(ctx, api.ExecutionContractPreviewInput{
		ProjectID: projectID, TaskID: specification.TaskID, TaskRevision: 1, GraphRevision: 1,
		WorkPackageID: "work:dispatch-authority", WorkerID: worker.ID, WorkerVersion: worker.Version, WorkerDigest: worker.Digest,
		IdempotencyKey: operationKey,
	})
	if err != nil || !contract.PreviewOnly || contract.ContractVersion != 1 || contract.ContractID == "" {
		t.Fatalf("contract preview = %+v, err=%v", contract, err)
	}
	runtimeResult, err := composition.Setup.RuntimePreflight(ctx, api.RuntimePreflightInput{
		ProjectID: projectID, ContractID: contract.ContractID, ContractVersion: contract.ContractVersion, ContractDigest: contract.ContractDigest,
		RuntimeID: composition.RuntimeRef.ID, RuntimeVersion: composition.RuntimeRef.Version, RuntimeDigest: composition.RuntimeRef.Digest,
		NodeID: composition.NodeRef.ID, NodeVersion: composition.NodeRef.Version, NodeDigest: composition.NodeRef.Digest,
	})
	if err != nil || !runtimeResult.Ready || !runtimeResult.NodeEligibility || !runtimeResult.WorkspaceReady {
		t.Fatalf("runtime preflight = %+v, err=%v", runtimeResult, err)
	}
	if _, err := composition.Administration.ApproveTaskGraph(ctx, api.TaskGraphApprovalInput{
		ProjectID: projectID, TaskID: specification.TaskID, TaskRevision: 1, GraphRevision: 1,
		ApprovalDigest: created.GraphDigest, IdempotencyKey: operationKey,
	}); err != nil {
		t.Fatal(err)
	}
	requests, err := composition.Dispatch.Pending(ctx)
	if err != nil || len(requests) != 1 || requests[0].Binding.Contract.ContractID != contract.ContractID {
		t.Fatalf("pending dispatches = %+v, err=%v", requests, err)
	}
	if err := composition.Scheduler.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composition.Scheduler.Shutdown(context.Background()) })
	cycle, err := composition.Scheduler.Cycle(ctx, requests)
	if err != nil || len(cycle.Items) != 1 || cycle.Items[0].Outcome != scheduler.OutcomeBlocked || len(cycle.Items[0].Reasons) == 0 || cycle.Items[0].Reasons[0] != "scheduler_paused" {
		t.Fatalf("paused scheduler cycle = %+v, err=%v", cycle, err)
	}
	otherRoot := t.TempDir()
	otherShare := core.ShareID("share-visible-only")
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: otherShare, Name: "Visible Only", RootPath: otherRoot, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, storage.ProjectRegistration{
		ProjectID: "project:visible-only", ShareID: otherShare, RootPath: otherRoot, Name: "Visible Only",
		AuthorityDeviceID: deviceIdentity.DeviceID, ManifestRecordID: "manifest:visible-only", ManifestRecordHash: strings.Repeat("d", 64),
		ManifestPath: project.ManifestRelativePath, RegisteredAt: now.UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := composition.Administration.SelectLocalProject(ctx, api.LocalProjectSelectionInput{ProjectID: "project:visible-only", IdempotencyKey: "select-visible-only"}); err != nil {
		t.Fatal(err)
	}
	requests, err = composition.Dispatch.Pending(ctx)
	if err != nil || len(requests) != 0 {
		t.Fatalf("visible-only project dispatches = %+v, err=%v", requests, err)
	}
}
