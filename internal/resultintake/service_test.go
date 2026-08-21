package resultintake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/storage"
)

func TestEnvelopeIsDeterministicStrictAndPathFree(t *testing.T) {
	contract, _ := intakeContract(t)
	envelope := intakeEnvelope(contract, "result:one")
	envelope.Patches = []ContentReference{{ID: "patch:b", Digest: intakeDigest("1"), Size: 20}, {ID: "patch:a", Digest: intakeDigest("2"), Size: 10}}
	first, raw, err := BuildEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	reversed := envelope
	reversed.Patches[0], reversed.Patches[1] = reversed.Patches[1], reversed.Patches[0]
	second, secondRaw, err := BuildEnvelope(reversed)
	if err != nil || first.Digest != second.Digest || !reflect.DeepEqual(raw, secondRaw) {
		t.Fatalf("deterministic envelope=%+v err=%v", second, err)
	}
	decoded, canonical, err := DecodeEnvelope(raw)
	if err != nil || decoded.Digest != first.Digest || !bytes.Equal(canonical, raw) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	unknown := bytes.Replace(raw, []byte(`"result_id"`), []byte(`"raw_prompt":"do not store","result_id"`), 1)
	if _, _, err := DecodeEnvelope(unknown); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("unknown-field error=%v", err)
	}
	leaky := envelope
	leaky.Tests = []TestEvidence{{EvidenceID: "test-evidence:one", Name: `C:\Users\owner\output`, Outcome: "passed", Digest: intakeDigest("3")}}
	if _, _, err := BuildEnvelope(leaky); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("absolute path error=%v", err)
	}
}

func TestAuthorityIntakeIsIdempotentRejectsConflictsAndForgedIdentity(t *testing.T) {
	contract, contractRecord := intakeContract(t)
	contracts := &memoryContracts{records: map[string]storage.ExecutionContractRecord{contractKey(contract.ContractID, contract.Version): contractRecord}}
	intakes := &memoryIntakes{records: map[string]storage.ResultIntakeRecord{}}
	when := time.Date(2026, time.August, 17, 20, 0, 0, 0, time.UTC)
	service := Service{Contracts: contracts, Intake: intakes, Now: func() time.Time { return when }}
	assignment := AssignmentReference{AssignmentID: "assignment:one", Version: 1, Digest: intakeDigest("8")}
	envelope, raw, err := BuildEnvelope(intakeEnvelope(contract, "result:one"))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{EnvelopeJSON: raw, ExpectedAssignment: assignment, ExpectedWorkspaceID: envelope.WorkspaceID, ExpectedProjectID: envelope.ProjectID, ExpectedExecutionID: envelope.ExecutionID, DecidedBy: "actor:intake"}
	decision, err := service.IntakeResult(context.Background(), request)
	if err != nil || !decision.AcceptedForIntake || decision.CanonicalPublished || decision.AlreadyPresent || decision.ReasonCode != "validated_untrusted_result" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	replay, err := service.IntakeResult(context.Background(), request)
	if err != nil || !replay.AlreadyPresent || replay.DecidedAt != decision.DecidedAt {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}

	conflictEnvelope := intakeEnvelope(contract, "result:one")
	conflictEnvelope.ClaimedOutcome = "failed"
	_, conflictRaw, _ := BuildEnvelope(conflictEnvelope)
	request.EnvelopeJSON = conflictRaw
	if _, err := service.IntakeResult(context.Background(), request); !errors.Is(err, ErrResultConflict) {
		t.Fatalf("result conflict error=%v", err)
	}

	forgedEnvelope := intakeEnvelope(contract, "result:forged")
	forgedEnvelope.Worker = project.RegistryReference{ID: "worker:forged", Version: 1, Digest: intakeDigest("9")}
	_, forgedRaw, _ := BuildEnvelope(forgedEnvelope)
	request.EnvelopeJSON = forgedRaw
	forged, err := service.IntakeResult(context.Background(), request)
	if err != nil || forged.AcceptedForIntake || forged.ReasonCode != "authority_binding_mismatch" || forged.CanonicalPublished {
		t.Fatalf("forged decision=%+v err=%v", forged, err)
	}
}

func TestUploadOnlyReceiptNeverGrantsCanonicalAuthority(t *testing.T) {
	contract, _ := intakeContract(t)
	_, raw, _ := BuildEnvelope(intakeEnvelope(contract, "result:upload"))
	fake := NewDeterministicUploadFake()
	receipt, err := fake.ReceiveCandidate(context.Background(), raw)
	if err != nil || receipt.CanonicalAuthority || receipt.AlreadyPresent {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	replay, err := fake.ReceiveCandidate(context.Background(), raw)
	if err != nil || !replay.AlreadyPresent || replay.CanonicalAuthority {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := intakeEnvelope(contract, "result:upload")
	changed.ClaimedOutcome = "failed"
	_, changedRaw, _ := BuildEnvelope(changed)
	if _, err := fake.ReceiveCandidate(context.Background(), changedRaw); !errors.Is(err, ErrResultConflict) {
		t.Fatalf("upload conflict error=%v", err)
	}
}

func intakeContract(t *testing.T) (executioncontract.Contract, storage.ExecutionContractRecord) {
	t.Helper()
	contract := executioncontract.Contract{
		Schema: executioncontract.Schema, ContractID: "contract:one", Version: 1, ProjectID: "project-one", TaskID: "task:one", TaskRevision: 2,
		GraphRevision: 3, WorkPackageID: "work-one", ExecutionID: "execution:one",
		Trade:       project.RegistryReference{ID: "trade:one", Version: 1, Digest: intakeDigest("2")},
		Worker:      project.RegistryReference{ID: "worker:one", Version: 4, Digest: intakeDigest("a")},
		Instruction: executioncontract.BindingReference{ID: "instruction:one", Version: 1, Digest: intakeDigest("3")}, ContextDigest: intakeDigest("4"),
		Runtime:  executioncontract.BindingReference{ID: "runtime:one", Version: 5, Digest: intakeDigest("b")},
		Provider: executioncontract.BindingReference{ID: "provider:one", Version: 1, Digest: intakeDigest("5")}, Model: executioncontract.BindingReference{ID: "model:one", Version: 1, Digest: intakeDigest("6")},
		Node: executioncontract.BindingReference{ID: "node:one", Version: 6, Digest: intakeDigest("c")},
	}
	unsigned, _ := json.Marshal(contract)
	contract.Digest = intakeHash(unsigned)
	raw, _ := json.Marshal(contract)
	record := storage.ExecutionContractRecord{
		ContractID: contract.ContractID, Version: contract.Version, ProjectID: contract.ProjectID, TaskID: contract.TaskID,
		TaskRevision: contract.TaskRevision, GraphRevision: contract.GraphRevision, WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID,
		Digest: contract.Digest, ContractJSON: raw, CreatedAt: time.Date(2026, time.August, 17, 19, 0, 0, 0, time.UTC),
	}
	return contract, record
}

func intakeEnvelope(contract executioncontract.Contract, resultID string) Envelope {
	return Envelope{
		ResultID: resultID, IdempotencyKeyDigest: intakeDigest("7"), ProjectID: contract.ProjectID, TaskID: contract.TaskID,
		TaskRevision: contract.TaskRevision, GraphRevision: contract.GraphRevision, WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID,
		Contract:   executioncontract.ContractReference{ContractID: contract.ContractID, Version: contract.Version, Digest: contract.Digest},
		Assignment: AssignmentReference{AssignmentID: "assignment:one", Version: 1, Digest: intakeDigest("8")}, Worker: contract.Worker,
		Runtime: contract.Runtime, Node: contract.Node, WorkspaceID: "workspace:one", ClaimedOutcome: "succeeded",
		Provenance: Provenance{Trade: contract.Trade, Instruction: contract.Instruction, ContextDigest: contract.ContextDigest, Provider: contract.Provider, Model: contract.Model},
		Artifacts:  []ContentReference{{ID: "artifact:one", Digest: intakeDigest("d"), Size: 12}},
		Handoff:    &ContentReference{ID: "handoff:one", Digest: intakeDigest("e"), Size: 2},
		Tests:      []TestEvidence{{EvidenceID: "test-evidence:one", Name: "unit-tests", Outcome: "passed", Digest: intakeDigest("f")}},
		Telemetry:  &ContentReference{ID: "telemetry:one", Digest: intakeDigest("1"), Size: 8}, CreatedAt: time.Date(2026, time.August, 17, 19, 30, 0, 0, time.UTC),
	}
}

func intakeDigest(seed string) string { return strings.Repeat(seed, 64) }
func intakeHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func contractKey(id string, version int64) string { return id + ":" + strconv.FormatInt(version, 10) }

type memoryContracts struct {
	records map[string]storage.ExecutionContractRecord
}

func (store *memoryContracts) SaveExecutionContract(context.Context, storage.ExecutionContractRecord) (storage.RegistryWriteResult, error) {
	return storage.RegistryWriteResult{}, nil
}
func (store *memoryContracts) GetExecutionContract(_ context.Context, id string, version int64) (storage.ExecutionContractRecord, error) {
	record, ok := store.records[contractKey(id, version)]
	if !ok {
		return storage.ExecutionContractRecord{}, storage.ErrNotFound
	}
	return record, nil
}
func (store *memoryContracts) ListExecutionContracts(context.Context, storage.ExecutionContractQuery) (storage.Page[storage.ExecutionContractRecord], error) {
	return storage.Page[storage.ExecutionContractRecord]{}, nil
}

type memoryIntakes struct {
	records map[string]storage.ResultIntakeRecord
}

func (store *memoryIntakes) SaveResultIntake(_ context.Context, record storage.ResultIntakeRecord) (storage.RegistryWriteResult, error) {
	if existing, ok := store.records[record.ResultID]; ok {
		if existing.EnvelopeDigest != record.EnvelopeDigest {
			return storage.RegistryWriteResult{}, storage.ErrConflict
		}
		return storage.RegistryWriteResult{AlreadyPresent: true}, nil
	}
	store.records[record.ResultID] = record
	return storage.RegistryWriteResult{}, nil
}
func (store *memoryIntakes) GetResultIntake(_ context.Context, resultID string) (storage.ResultIntakeRecord, error) {
	record, ok := store.records[resultID]
	if !ok {
		return storage.ResultIntakeRecord{}, storage.ErrNotFound
	}
	return record, nil
}
