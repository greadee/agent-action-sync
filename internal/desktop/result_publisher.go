package desktop

import (
	"context"
	"errors"
	"time"

	"syncgate/internal/codexruntime"
	"syncgate/internal/executioncontract"
	"syncgate/internal/resultintake"
	"syncgate/internal/runtimecontract"
	"syncgate/internal/storage"
)

// LocalResultPublisher turns a fenced terminal runtime claim into an
// authority-authored envelope. The model never chooses the canonical result
// identity, digest, assignment binding, or provenance.
type LocalResultPublisher struct {
	Control orchestrationSnapshotReader
	Results LocalResultStore
	Intake  resultintake.Service
	Now     func() time.Time
}

type orchestrationSnapshotReader interface {
	GetAssignment(context.Context, string) (storage.OrchestrationSnapshot, error)
}

func (publisher LocalResultPublisher) PublishResult(ctx context.Context, publication codexruntime.ResultPublication) (runtimecontract.CollectedResult, error) {
	if ctx == nil || publisher.Control == nil || publisher.Now == nil || publication.ClaimedOutcome != "succeeded" {
		return runtimecontract.CollectedResult{}, errors.New("result publication is invalid")
	}
	session, contract := publication.Session, publication.Contract
	snapshot, err := publisher.Control.GetAssignment(ctx, session.AssignmentID)
	if err != nil || snapshot.Assignment.AssignmentID != session.AssignmentID || snapshot.Attempt.AttemptID != session.AttemptID ||
		snapshot.Assignment.CurrentAttemptID != session.AttemptID || snapshot.Assignment.ContractID != contract.ContractID ||
		snapshot.Assignment.ContractVersion != contract.Version || snapshot.Assignment.ContractDigest != contract.Digest ||
		snapshot.Lease == nil || snapshot.Lease.State != storage.LeaseActive || snapshot.Lease.Generation != session.LeaseGeneration ||
		snapshot.Lease.FencingDigest != session.FencingDigest || snapshot.Resources == nil ||
		snapshot.Resources.RuntimeSessionID != session.SessionID || snapshot.Resources.WorkspaceID != session.WorkspaceID {
		return runtimecontract.CollectedResult{}, errors.New("result publication fence is stale")
	}
	createdAt := publisher.Now().UTC()
	if createdAt.IsZero() {
		return runtimecontract.CollectedResult{}, errors.New("result publication clock is unavailable")
	}
	resultID := "result:" + localHash("result", contract.Digest, session.AssignmentID, session.AttemptID, session.FencingDigest)[:32]
	envelope, raw, err := resultintake.BuildEnvelope(resultintake.Envelope{
		ResultID: resultID, IdempotencyKeyDigest: localHash("result-envelope", resultID, contract.Digest),
		ProjectID: contract.ProjectID, TaskID: contract.TaskID, TaskRevision: contract.TaskRevision, GraphRevision: contract.GraphRevision,
		WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID,
		Contract: executioncontract.ContractReference{ContractID: contract.ContractID, Version: contract.Version, Digest: contract.Digest}, Assignment: LocalAssignmentReference(snapshot), Worker: contract.Worker,
		Runtime: contract.Runtime, Node: contract.Node,
		Provenance:  resultintake.Provenance{Trade: contract.Trade, Instruction: contract.Instruction, ContextDigest: contract.ContextDigest, Provider: contract.Provider, Model: contract.Model},
		WorkspaceID: session.WorkspaceID, ClaimedOutcome: publication.ClaimedOutcome, CreatedAt: createdAt,
	})
	if err != nil {
		return runtimecontract.CollectedResult{}, err
	}
	stored, err := publisher.Results.PutEnvelope(ctx, raw)
	if err != nil || stored.ResultID != envelope.ResultID || stored.Digest != envelope.Digest {
		return runtimecontract.CollectedResult{}, errors.New("result envelope could not be stored")
	}
	decision, err := publisher.Intake.IntakeResult(ctx, resultintake.Request{
		EnvelopeJSON: raw, ExpectedAssignment: envelope.Assignment, ExpectedWorkspaceID: session.WorkspaceID,
		ExpectedProjectID: contract.ProjectID, ExpectedExecutionID: contract.ExecutionID, DecidedBy: "actor:desktop-runtime",
	})
	if err != nil || !decision.AcceptedForIntake || decision.ResultID != envelope.ResultID || decision.EnvelopeDigest != envelope.Digest {
		return runtimecontract.CollectedResult{}, errors.New("result envelope intake was rejected")
	}
	return runtimecontract.CollectedResult{ResultID: envelope.ResultID, EnvelopeDigest: envelope.Digest, ClaimedOutcome: envelope.ClaimedOutcome}, nil
}
