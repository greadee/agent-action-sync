package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/storage"
)

func TestEnvelopeIsDeterministicKeepsUnknownUnknownAndRejectsLeaks(t *testing.T) {
	contract, _ := testContract(t)
	envelope := testEnvelope(contract)
	value := int64(12)
	envelope.Summary.Observations = []project.TelemetryObservation{{Name: "tool_calls", Value: &value, Source: "locally_measured"}, {Name: "provider_cost_micros", Source: "provider_reported"}}
	first, raw, err := BuildEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary.Observations[0].Name != "provider_cost_micros" || first.Summary.Observations[0].Value != nil {
		t.Fatalf("missing measurement was changed: %+v", first.Summary.Observations[0])
	}
	reversed := envelope
	reversed.Summary.Observations[0], reversed.Summary.Observations[1] = reversed.Summary.Observations[1], reversed.Summary.Observations[0]
	second, secondRaw, err := BuildEnvelope(reversed)
	if err != nil || first.Digest != second.Digest || !bytes.Equal(raw, secondRaw) {
		t.Fatalf("determinism err=%v", err)
	}
	decoded, canonical, err := DecodeEnvelope(raw)
	if err != nil || decoded.Summary.Observations[0].Value != nil || !bytes.Equal(raw, canonical) {
		t.Fatalf("decode=%+v err=%v", decoded, err)
	}
	unknown := bytes.Replace(raw, []byte(`"telemetry_id"`), []byte(`"raw_prompt":"do not store","telemetry_id"`), 1)
	if _, _, err := DecodeEnvelope(unknown); !errors.Is(err, ErrInvalidTelemetry) {
		t.Fatalf("raw prompt error=%v", err)
	}
	leaky := testEnvelope(contract)
	leaky.Summary.Evidence = []project.TelemetryEvidenceReference{{ID: `evidence:C:\Users\owner\terminal.log`, Digest: td("a"), Kind: "runtime", Source: "locally_measured"}}
	if _, _, err := BuildEnvelope(leaky); !errors.Is(err, ErrInvalidTelemetry) {
		t.Fatalf("absolute path error=%v", err)
	}
}

func TestIngestIsIdempotentRejectsForgedContractBindingAndDerivesCandidate(t *testing.T) {
	contract, record := testContract(t)
	contracts := &memoryContracts{records: map[string]storage.ExecutionContractRecord{key(contract.ContractID, contract.Version): record}}
	store := &memoryTelemetry{records: map[string]storage.ExecutionTelemetryRecord{}}
	service := Service{Contracts: contracts, Telemetry: store}
	_, raw, err := BuildEnvelope(testEnvelope(contract))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{EnvelopeJSON: raw, ExpectedProjectID: contract.ProjectID, ExpectedExecutionID: contract.ExecutionID}
	decision, err := service.Ingest(context.Background(), request)
	if err != nil || !decision.Accepted || decision.AlreadyPresent {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	replay, err := service.Ingest(context.Background(), request)
	if err != nil || !replay.AlreadyPresent {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	candidate := SummaryCandidate(decision.Summary)
	if candidate.Kind != "success" || candidate.TelemetryDigest != decision.Summary.TelemetryDigest || len(candidate.Inputs) != 1 {
		t.Fatalf("candidate=%+v", candidate)
	}
	forged := testEnvelope(contract)
	forged.TelemetryID = "telemetry:forged"
	forged.Summary.Worker = project.RegistryReference{ID: "worker:forged", Version: 1, Digest: td("f")}
	_, forgedRaw, _ := BuildEnvelope(forged)
	if _, err := service.Ingest(context.Background(), Request{EnvelopeJSON: forgedRaw, ExpectedProjectID: contract.ProjectID, ExpectedExecutionID: contract.ExecutionID}); !errors.Is(err, ErrInvalidTelemetry) {
		t.Fatalf("forged binding error=%v", err)
	}
}

func testContract(t *testing.T) (executioncontract.Contract, storage.ExecutionContractRecord) {
	t.Helper()
	contract := executioncontract.Contract{Schema: executioncontract.Schema, ContractID: "contract:one", Version: 1, ProjectID: "project-one", ProjectRevision: td("1"), TaskID: "task:one", TaskRecordID: "task-record", TaskRevision: 2, TaskDigest: td("2"), GraphRecordID: "graph-record", GraphRevision: 3, GraphDigest: td("3"), WorkPackageID: "work-one", WorkPackageRecordID: "work-record", WorkPackageDigest: td("4"), ExecutionID: "execution:one", Trade: project.RegistryReference{ID: "trade:one", Version: 1, Digest: td("5")}, Worker: project.RegistryReference{ID: "worker:one", Version: 1, Digest: td("6")}, Instruction: executioncontract.BindingReference{ID: "instruction:one", Version: 1, Digest: td("7")}, ContextDigest: td("8"), Runtime: executioncontract.BindingReference{ID: "runtime:one", Version: 1, Digest: td("9")}, Provider: executioncontract.BindingReference{ID: "provider:one", Version: 1, Digest: td("a")}, Model: executioncontract.BindingReference{ID: "model:one", Version: 1, Digest: td("b")}, Node: executioncontract.BindingReference{ID: "node:one", Version: 1, Digest: td("c")}}
	unsigned, _ := json.Marshal(contract)
	contract.Digest = th(unsigned)
	raw, _ := json.Marshal(contract)
	return contract, storage.ExecutionContractRecord{ContractID: contract.ContractID, Version: contract.Version, ProjectID: contract.ProjectID, TaskID: contract.TaskID, TaskRevision: contract.TaskRevision, GraphRevision: contract.GraphRevision, WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID, Digest: contract.Digest, ContractJSON: raw, CreatedAt: time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)}
}

func testEnvelope(contract executioncontract.Contract) Envelope {
	value := int64(10)
	return Envelope{TelemetryID: "telemetry:one", IdempotencyKeyDigest: td("d"), CreatedAt: time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC), Summary: project.TelemetrySummaryPayload{Contract: project.RegistryReference{ID: contract.ContractID, Version: contract.Version, Digest: contract.Digest}, ProjectRevision: contract.ProjectRevision, TaskID: contract.TaskID, TaskRecordID: contract.TaskRecordID, TaskRevision: contract.TaskRevision, TaskDigest: contract.TaskDigest, GraphRecordID: contract.GraphRecordID, GraphRevision: contract.GraphRevision, GraphDigest: contract.GraphDigest, WorkPackageID: contract.WorkPackageID, WorkPackageRecordID: contract.WorkPackageRecordID, WorkPackageDigest: contract.WorkPackageDigest, Trade: contract.Trade, Worker: contract.Worker, Instruction: bind(contract.Instruction), ContextDigest: contract.ContextDigest, Runtime: bind(contract.Runtime), Provider: bind(contract.Provider), Model: bind(contract.Model), Node: bind(contract.Node), FinalOutcome: "succeeded", Observations: []project.TelemetryObservation{{Name: "input_tokens", Value: &value, Source: "provider_reported"}}}}
}
func bind(value executioncontract.BindingReference) project.TelemetryBindingReference {
	return project.TelemetryBindingReference{ID: value.ID, Version: value.Version, Digest: value.Digest}
}
func td(value string) string              { return strings.Repeat(value, 64) }
func th(value []byte) string              { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func key(id string, version int64) string { return id + ":" + string(rune(version+'0')) }

type memoryContracts struct {
	records map[string]storage.ExecutionContractRecord
}

func (store *memoryContracts) SaveExecutionContract(context.Context, storage.ExecutionContractRecord) (storage.RegistryWriteResult, error) {
	return storage.RegistryWriteResult{}, nil
}
func (store *memoryContracts) GetExecutionContract(_ context.Context, id string, version int64) (storage.ExecutionContractRecord, error) {
	value, ok := store.records[key(id, version)]
	if !ok {
		return storage.ExecutionContractRecord{}, storage.ErrNotFound
	}
	return value, nil
}
func (store *memoryContracts) ListExecutionContracts(context.Context, storage.ExecutionContractQuery) (storage.Page[storage.ExecutionContractRecord], error) {
	return storage.Page[storage.ExecutionContractRecord]{}, nil
}

type memoryTelemetry struct {
	records map[string]storage.ExecutionTelemetryRecord
}

func (store *memoryTelemetry) SaveExecutionTelemetry(_ context.Context, value storage.ExecutionTelemetryRecord) (storage.RegistryWriteResult, error) {
	if old, ok := store.records[value.TelemetryID]; ok {
		if old.TelemetryDigest != value.TelemetryDigest {
			return storage.RegistryWriteResult{}, storage.ErrConflict
		}
		return storage.RegistryWriteResult{AlreadyPresent: true}, nil
	}
	store.records[value.TelemetryID] = value
	return storage.RegistryWriteResult{}, nil
}
func (store *memoryTelemetry) GetExecutionTelemetry(_ context.Context, id string) (storage.ExecutionTelemetryRecord, error) {
	value, ok := store.records[id]
	if !ok {
		return storage.ExecutionTelemetryRecord{}, storage.ErrNotFound
	}
	return value, nil
}
func (store *memoryTelemetry) ListExecutionTelemetry(context.Context, string, string) ([]storage.ExecutionTelemetryRecord, error) {
	return nil, nil
}
