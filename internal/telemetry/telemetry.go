// Package telemetry accepts bounded execution evidence at the project
// authority. It intentionally has no runtime, transport, prompt, terminal, or
// filesystem dependency.
package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/storage"
)

const (
	Schema           = "syncgate.execution-telemetry.v1"
	SummarySchema    = "syncgate.telemetry-summary.v1"
	MaxEnvelopeBytes = 1 << 20
)

var (
	ErrInvalidTelemetry  = errors.New("invalid execution telemetry")
	ErrTelemetryConflict = errors.New("execution telemetry conflict")
)

type Envelope struct {
	Schema               string                          `json:"schema"`
	TelemetryID          string                          `json:"telemetry_id"`
	IdempotencyKeyDigest string                          `json:"idempotency_key_digest"`
	Summary              project.TelemetrySummaryPayload `json:"summary"`
	CreatedAt            time.Time                       `json:"created_at"`
	Digest               string                          `json:"digest"`
}

type Request struct {
	EnvelopeJSON        []byte
	ExpectedProjectID   string
	ExpectedExecutionID string
}

type Decision struct {
	TelemetryID     string
	TelemetryDigest string
	Accepted        bool
	AlreadyPresent  bool
	Summary         project.TelemetrySummaryPayload
}

type Service struct {
	Contracts storage.ExecutionContractStore
	Telemetry storage.ExecutionTelemetryStore
}

func BuildEnvelope(envelope Envelope) (Envelope, []byte, error) {
	envelope.Schema = Schema
	envelope.Digest = ""
	normalize(&envelope)
	envelope.Summary.TelemetryDigest = ""
	summaryDigest, err := digestSummary(envelope.Summary)
	if err != nil {
		return Envelope{}, nil, err
	}
	envelope.Summary.TelemetryDigest = summaryDigest
	if err := validateEnvelope(envelope); err != nil {
		return Envelope{}, nil, err
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return Envelope{}, nil, err
	}
	envelope.Digest = digest(raw)
	raw, err = json.Marshal(envelope)
	if err != nil || len(raw) > MaxEnvelopeBytes {
		return Envelope{}, nil, ErrInvalidTelemetry
	}
	return envelope, raw, nil
}

func DecodeEnvelope(raw []byte) (Envelope, []byte, error) {
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes {
		return Envelope{}, nil, ErrInvalidTelemetry
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, nil, ErrInvalidTelemetry
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Envelope{}, nil, ErrInvalidTelemetry
	}
	expected := envelope.Digest
	envelope.Digest = ""
	normalize(&envelope)
	expectedSummary := envelope.Summary.TelemetryDigest
	envelope.Summary.TelemetryDigest = ""
	actualSummary, err := digestSummary(envelope.Summary)
	if err != nil || !validDigest(expectedSummary) || actualSummary != expectedSummary {
		return Envelope{}, nil, ErrInvalidTelemetry
	}
	envelope.Summary.TelemetryDigest = expectedSummary
	if err := validateEnvelope(envelope); err != nil || !validDigest(expected) {
		return Envelope{}, nil, ErrInvalidTelemetry
	}
	unsigned, err := json.Marshal(envelope)
	if err != nil || digest(unsigned) != expected {
		return Envelope{}, nil, ErrInvalidTelemetry
	}
	envelope.Digest = expected
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return Envelope{}, nil, err
	}
	return envelope, canonical, nil
}

func (service Service) Ingest(ctx context.Context, request Request) (Decision, error) {
	if ctx == nil || service.Contracts == nil || service.Telemetry == nil || !identifier(request.ExpectedProjectID) || !namespaced(request.ExpectedExecutionID, "execution:") {
		return Decision{}, ErrInvalidTelemetry
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	envelope, canonical, err := DecodeEnvelope(request.EnvelopeJSON)
	if err != nil {
		return Decision{}, err
	}
	if envelope.Summary.ProjectRevision == "" || envelope.Summary.TelemetryID != envelope.TelemetryID || request.ExpectedProjectID == "" {
		return Decision{}, ErrInvalidTelemetry
	}
	if err := service.validateAuthority(ctx, envelope, request); err != nil {
		return Decision{}, err
	}
	write, err := service.Telemetry.SaveExecutionTelemetry(ctx, storage.ExecutionTelemetryRecord{
		TelemetryID: envelope.TelemetryID, TelemetryDigest: envelope.Digest, IdempotencyKeyDigest: envelope.IdempotencyKeyDigest,
		ProjectID: request.ExpectedProjectID, ExecutionID: request.ExpectedExecutionID, ContractID: envelope.Summary.Contract.ID,
		ContractVersion: envelope.Summary.Contract.Version, ContractDigest: envelope.Summary.Contract.Digest,
		FinalOutcome: envelope.Summary.FinalOutcome, SummaryJSON: canonical, CreatedAt: envelope.CreatedAt,
	})
	if err != nil {
		if errors.Is(err, storage.ErrConflict) {
			return Decision{}, ErrTelemetryConflict
		}
		return Decision{}, err
	}
	return Decision{TelemetryID: envelope.TelemetryID, TelemetryDigest: envelope.Digest, Accepted: true, AlreadyPresent: write.AlreadyPresent, Summary: cloneSummary(envelope.Summary)}, nil
}

func (service Service) validateAuthority(ctx context.Context, envelope Envelope, request Request) error {
	summary := envelope.Summary
	if summary.TaskID == "" || summary.WorkPackageID == "" || request.ExpectedProjectID == "" {
		return ErrInvalidTelemetry
	}
	contractRecord, err := service.Contracts.GetExecutionContract(ctx, summary.Contract.ID, summary.Contract.Version)
	if err != nil || contractRecord.Digest != summary.Contract.Digest {
		return ErrInvalidTelemetry
	}
	var contract executioncontract.Contract
	if json.Unmarshal(contractRecord.ContractJSON, &contract) != nil || executioncontract.VerifyDigest(contract) != nil {
		return ErrInvalidTelemetry
	}
	if contract.ProjectID != request.ExpectedProjectID || contract.ExecutionID != request.ExpectedExecutionID ||
		summary.ProjectRevision != contract.ProjectRevision || summary.TaskID != contract.TaskID || summary.TaskRecordID != contract.TaskRecordID || summary.TaskRevision != contract.TaskRevision || summary.TaskDigest != contract.TaskDigest ||
		summary.GraphRecordID != contract.GraphRecordID || summary.GraphRevision != contract.GraphRevision || summary.GraphDigest != contract.GraphDigest ||
		summary.WorkPackageID != contract.WorkPackageID || summary.WorkPackageRecordID != contract.WorkPackageRecordID || summary.WorkPackageDigest != contract.WorkPackageDigest ||
		summary.Trade != contract.Trade || summary.Worker != contract.Worker || summary.ContextDigest != contract.ContextDigest ||
		!sameBinding(summary.Instruction, contract.Instruction) || !sameBinding(summary.Runtime, contract.Runtime) || !sameBinding(summary.Provider, contract.Provider) || !sameBinding(summary.Model, contract.Model) || !sameBinding(summary.Node, contract.Node) {
		return ErrInvalidTelemetry
	}
	return nil
}

func SummaryCandidate(summary project.TelemetrySummaryPayload) MemoryCandidate {
	kind := "failure"
	if summary.FinalOutcome == "succeeded" {
		kind = "success"
	}
	inputs := make([]PromotionInput, 0, len(summary.Observations))
	for _, observation := range summary.Observations {
		if observation.Value != nil {
			inputs = append(inputs, PromotionInput{Name: observation.Name, Value: *observation.Value, Source: observation.Source})
		}
	}
	return MemoryCandidate{Kind: kind, TelemetryID: summary.TelemetryID, TelemetryDigest: summary.TelemetryDigest, Contract: summary.Contract, Trade: summary.Trade, Worker: summary.Worker, Provider: summary.Provider, Model: summary.Model, FinalOutcome: summary.FinalOutcome, Inputs: inputs}
}

// MemoryCandidate is a deterministic, reviewable input. It does not promote
// knowledge, call a model, or update any global/workforce state.
type MemoryCandidate struct {
	Kind            string
	TelemetryID     string
	TelemetryDigest string
	Contract        project.RegistryReference
	Trade           project.RegistryReference
	Worker          project.RegistryReference
	Provider        project.TelemetryBindingReference
	Model           project.TelemetryBindingReference
	FinalOutcome    string
	Inputs          []PromotionInput
}
type PromotionInput struct {
	Name   string
	Value  int64
	Source string
}

func normalize(envelope *Envelope) {
	envelope.Summary.Schema = SummarySchema
	envelope.Summary.TelemetryID = envelope.TelemetryID
	envelope.Summary.Observations = append([]project.TelemetryObservation(nil), envelope.Summary.Observations...)
	envelope.Summary.Evidence = append([]project.TelemetryEvidenceReference(nil), envelope.Summary.Evidence...)
	sort.Slice(envelope.Summary.Observations, func(i, j int) bool {
		return envelope.Summary.Observations[i].Name < envelope.Summary.Observations[j].Name
	})
	sort.Slice(envelope.Summary.Evidence, func(i, j int) bool { return envelope.Summary.Evidence[i].ID < envelope.Summary.Evidence[j].ID })
}

func validateEnvelope(envelope Envelope) error {
	if envelope.Schema != Schema || !namespaced(envelope.TelemetryID, "telemetry:") || !validDigest(envelope.IdempotencyKeyDigest) || envelope.CreatedAt.IsZero() || !utc(envelope.CreatedAt) {
		return ErrInvalidTelemetry
	}
	summary := envelope.Summary
	// project validation is the portable allowlist; use a synthetic event so
	// both ingress and portable history reject the same unsafe field shapes.
	payload, err := json.Marshal(summary)
	if err != nil {
		return ErrInvalidTelemetry
	}
	event := project.WorkEvent{RecordHeader: project.NewRecordHeader(project.RecordWorkEvent, "telemetry-validation", "validation-project"), EventType: project.EventTelemetryRecorded, OccurredAt: envelope.CreatedAt, WorkPackageID: summary.WorkPackageID, ExecutionID: "execution:validation", Producer: project.Producer{DeviceID: "device-validation"}, Payload: payload}
	if _, err := project.MarshalRecord(event); err != nil {
		return ErrInvalidTelemetry
	}
	previous := ""
	for _, observation := range summary.Observations {
		if observation.Name == previous {
			return ErrInvalidTelemetry
		}
		previous = observation.Name
	}
	previous = ""
	for _, evidence := range summary.Evidence {
		if evidence.ID == previous {
			return ErrInvalidTelemetry
		}
		previous = evidence.ID
	}
	return nil
}

func sameBinding(left project.TelemetryBindingReference, right executioncontract.BindingReference) bool {
	return left.ID == right.ID && left.Version == right.Version && left.Digest == right.Digest
}
func cloneSummary(value project.TelemetrySummaryPayload) project.TelemetrySummaryPayload {
	value.Observations = append([]project.TelemetryObservation(nil), value.Observations...)
	value.Evidence = append([]project.TelemetryEvidenceReference(nil), value.Evidence...)
	return value
}
func digest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func digestSummary(value project.TelemetrySummaryPayload) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}
func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
func namespaced(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && identifier(value)
}
func identifier(value string) bool {
	if len(value) < 1 || len(value) > project.MaxIdentifierBytes {
		return false
	}
	for i, c := range value {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-'
		if !valid || (i == 0 && !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}
func utc(value time.Time) bool { _, offset := value.Zone(); return offset == 0 }
