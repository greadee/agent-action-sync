package desktop

import (
	"context"
	"errors"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/registry"
	"syncgate/internal/storage"
)

const (
	localTradeID       = "trade:codex-generalist"
	localWorkerID      = "worker:codex-local"
	localInstructionID = "instruction:codex-supervised"
	localToolPolicyID  = "tool-policy:desktop-supervised"
)

var localRegistryEpoch = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

type LocalRegistrySnapshot struct {
	Trade     storage.TradeDefinition
	TradeRef  project.RegistryReference
	Worker    storage.WorkerProfile
	WorkerRef project.RegistryReference
}

func BootstrapLocalRegistry(
	ctx context.Context,
	store storage.RegistryStore,
	projects storage.ProjectRegistrationStore,
	runtimeRef executioncontract.BindingReference,
	providerID, modelID string,
	now func() time.Time,
) (LocalRegistrySnapshot, error) {
	if ctx == nil || store == nil || projects == nil || runtimeRef.ID == "" || providerID == "" || modelID == "" || now == nil {
		return LocalRegistrySnapshot{}, errors.New("local registry bootstrap is incomplete")
	}
	service := registry.Service{Store: store, Projects: projects, Now: now}
	trade := storage.TradeDefinition{
		TradeID: localTradeID, Version: 1, Name: "Codex generalist", Lifecycle: storage.RegistryActive,
		CapabilityTags:       []string{"cap:branch", "cap:codex", "cap:inspect", "cap:shell", "cap:test", "cap:write"},
		RequiredCapabilities: []string{"cap:codex"}, Description: "Built-in supervised local Codex trade.",
		Evidence:  storage.RegistryEvidence{Source: "built-in", ObservedAt: localRegistryEpoch, EvidenceNote: "authority-local deterministic bootstrap"},
		CreatedAt: localRegistryEpoch,
	}
	tradeSaved, err := service.SaveTrade(ctx, "actor:desktop-bootstrap", trade)
	if err != nil {
		return LocalRegistrySnapshot{}, err
	}
	trade.ContentHash = tradeSaved.Digest
	workerID := localWorkerID + "-" + localHash(runtimeRef.ID, runtimeRef.Digest, providerID, modelID)[:12]
	worker := storage.WorkerProfile{
		WorkerID: workerID, Version: 1, Name: "Local Codex", Lifecycle: storage.RegistryActive,
		TradeID: trade.TradeID, TradeVersion: trade.Version,
		InstructionID: localInstructionID, InstructionVer: 1,
		RuntimeID: runtimeRef.ID, RuntimeVersion: runtimeRef.Version,
		Provider: providerID, Model: modelID, ModelVersion: "configured:" + runtimeRef.Digest[:16],
		ToolPolicyID: localToolPolicyID, ToolPolicyVer: 1,
		CapabilityTags: []string{"cap:branch", "cap:codex", "cap:inspect", "cap:shell", "cap:test", "cap:write"},
		Evidence:       storage.RegistryEvidence{Source: "built-in", ObservedAt: localRegistryEpoch, EvidenceNote: "authority-local deterministic bootstrap"},
		CreatedAt:      localRegistryEpoch,
	}
	workerSaved, err := service.SaveWorker(ctx, "actor:desktop-bootstrap", worker)
	if err != nil {
		return LocalRegistrySnapshot{}, err
	}
	worker.ContentHash = workerSaved.Digest
	return LocalRegistrySnapshot{
		Trade: trade, TradeRef: project.RegistryReference{ID: tradeSaved.ID, Version: tradeSaved.Version, Digest: tradeSaved.Digest},
		Worker: worker, WorkerRef: project.RegistryReference{ID: workerSaved.ID, Version: workerSaved.Version, Digest: workerSaved.Digest},
	}, nil
}
