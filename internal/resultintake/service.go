package resultintake

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/storage"
)

type Request struct {
	EnvelopeJSON        []byte
	ExpectedAssignment  AssignmentReference
	ExpectedWorkspaceID string
	ExpectedProjectID   string
	ExpectedExecutionID string
	DecidedBy           string
}

type Decision struct {
	ResultID           string
	EnvelopeDigest     string
	AcceptedForIntake  bool
	ReasonCode         string
	AlreadyPresent     bool
	CanonicalPublished bool
	DecidedAt          time.Time
}

type Service struct {
	Contracts storage.ExecutionContractStore
	Intake    storage.ResultIntakeStore
	Now       func() time.Time
}

func (service Service) IntakeResult(ctx context.Context, request Request) (Decision, error) {
	if ctx == nil || service.Contracts == nil || service.Intake == nil || service.Now == nil ||
		!validAssignmentReference(request.ExpectedAssignment) || !namespaced(request.ExpectedWorkspaceID, "workspace:") || !identifier(request.ExpectedProjectID) ||
		!namespaced(request.ExpectedExecutionID, "execution:") || !namespaced(request.DecidedBy, "actor:") {
		return Decision{}, ErrInvalidEnvelope
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	envelope, canonical, err := DecodeEnvelope(request.EnvelopeJSON)
	if err != nil {
		return Decision{}, err
	}
	accepted, reason := service.validateAuthority(ctx, envelope, request)
	decidedAt := service.Now().UTC()
	if decidedAt.IsZero() {
		return Decision{}, ErrInvalidEnvelope
	}
	decisionState := "rejected"
	if accepted {
		decisionState = "accepted"
	}
	record := storage.ResultIntakeRecord{
		ResultID: envelope.ResultID, EnvelopeDigest: envelope.Digest, IdempotencyKeyDigest: envelope.IdempotencyKeyDigest, ProjectID: request.ExpectedProjectID, ExecutionID: request.ExpectedExecutionID,
		ContractID: envelope.Contract.ContractID, ContractVersion: envelope.Contract.Version, ContractDigest: envelope.Contract.Digest,
		AssignmentID: envelope.Assignment.AssignmentID, AssignmentDigest: envelope.Assignment.Digest,
		Decision: decisionState, ReasonCode: reason, EnvelopeJSON: canonical, DecidedAt: decidedAt, DecidedBy: request.DecidedBy,
	}
	write, err := service.Intake.SaveResultIntake(ctx, record)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return Decision{}, ErrResultConflict
		}
		return Decision{}, err
	}
	if write.AlreadyPresent {
		existing, err := service.Intake.GetResultIntake(ctx, envelope.ResultID)
		if err != nil {
			return Decision{}, err
		}
		return decisionFromRecord(existing, true), nil
	}
	return decisionFromRecord(record, false), nil
}

func (service Service) validateAuthority(ctx context.Context, envelope Envelope, request Request) (bool, string) {
	if envelope.Assignment != request.ExpectedAssignment || envelope.WorkspaceID != request.ExpectedWorkspaceID {
		return false, "assignment_binding_mismatch"
	}
	if envelope.ProjectID != request.ExpectedProjectID || envelope.ExecutionID != request.ExpectedExecutionID {
		return false, "execution_binding_mismatch"
	}
	record, err := service.Contracts.GetExecutionContract(ctx, envelope.Contract.ContractID, envelope.Contract.Version)
	if err != nil || record.Digest != envelope.Contract.Digest {
		return false, "contract_unavailable"
	}
	var contract executioncontract.Contract
	if json.Unmarshal(record.ContractJSON, &contract) != nil || executioncontract.VerifyDigest(contract) != nil || contract.Digest != record.Digest {
		return false, "contract_invalid"
	}
	if envelope.ProjectID != contract.ProjectID || envelope.TaskID != contract.TaskID || envelope.TaskRevision != contract.TaskRevision ||
		envelope.GraphRevision != contract.GraphRevision || envelope.WorkPackageID != contract.WorkPackageID || envelope.ExecutionID != contract.ExecutionID ||
		envelope.Worker != contract.Worker || envelope.Runtime != contract.Runtime || envelope.Node != contract.Node {
		return false, "authority_binding_mismatch"
	}
	if envelope.Provenance.Trade != contract.Trade || envelope.Provenance.Instruction != contract.Instruction || envelope.Provenance.ContextDigest != contract.ContextDigest ||
		envelope.Provenance.Provider != contract.Provider || envelope.Provenance.Model != contract.Model {
		return false, "provenance_binding_mismatch"
	}
	return true, "validated_untrusted_result"
}

func decisionFromRecord(record storage.ResultIntakeRecord, replay bool) Decision {
	return Decision{
		ResultID: record.ResultID, EnvelopeDigest: record.EnvelopeDigest, AcceptedForIntake: record.Decision == "accepted",
		ReasonCode: record.ReasonCode, AlreadyPresent: replay, CanonicalPublished: false, DecidedAt: record.DecidedAt,
	}
}
