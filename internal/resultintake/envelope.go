// Package resultintake validates immutable, untrusted runtime results at the
// project authority. A validated envelope is not canonical project history and
// cannot satisfy acceptance by itself.
package resultintake

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
)

const (
	EnvelopeSchema   = "syncgate.result-envelope.v1"
	MaxEnvelopeBytes = 1 << 20
	MaxReferences    = 256
)

var (
	ErrInvalidEnvelope = errors.New("invalid result envelope")
	ErrForgedBinding   = errors.New("forged result binding")
	ErrResultConflict  = errors.New("result envelope conflict")
)

type AssignmentReference struct {
	AssignmentID string `json:"assignment_id"`
	Version      int64  `json:"version"`
	Digest       string `json:"digest"`
}

type ContentReference struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type TestEvidence struct {
	EvidenceID string `json:"evidence_id"`
	Name       string `json:"name"`
	Outcome    string `json:"outcome"`
	Digest     string `json:"digest"`
}

type Provenance struct {
	Trade         project.RegistryReference          `json:"trade"`
	Instruction   executioncontract.BindingReference `json:"instruction"`
	ContextDigest string                             `json:"context_digest"`
	Provider      executioncontract.BindingReference `json:"provider"`
	Model         executioncontract.BindingReference `json:"model"`
}

type Envelope struct {
	Schema               string                              `json:"schema"`
	ResultID             string                              `json:"result_id"`
	IdempotencyKeyDigest string                              `json:"idempotency_key_digest"`
	ProjectID            string                              `json:"project_id"`
	TaskID               string                              `json:"task_id"`
	TaskRevision         int64                               `json:"task_revision"`
	GraphRevision        int64                               `json:"graph_revision"`
	WorkPackageID        string                              `json:"work_package_id"`
	ExecutionID          string                              `json:"execution_id"`
	Contract             executioncontract.ContractReference `json:"contract"`
	Assignment           AssignmentReference                 `json:"assignment"`
	Worker               project.RegistryReference           `json:"worker"`
	Runtime              executioncontract.BindingReference  `json:"runtime"`
	Node                 executioncontract.BindingReference  `json:"node"`
	Provenance           Provenance                          `json:"provenance"`
	WorkspaceID          string                              `json:"workspace_id"`
	Patches              []ContentReference                  `json:"patches,omitempty"`
	Artifacts            []ContentReference                  `json:"artifacts,omitempty"`
	Handoff              *ContentReference                   `json:"handoff,omitempty"`
	Tests                []TestEvidence                      `json:"tests,omitempty"`
	Telemetry            *ContentReference                   `json:"telemetry,omitempty"`
	ClaimedOutcome       string                              `json:"claimed_outcome"`
	CreatedAt            time.Time                           `json:"created_at"`
	Digest               string                              `json:"digest"`
}

func BuildEnvelope(envelope Envelope) (Envelope, []byte, error) {
	envelope.Schema = EnvelopeSchema
	envelope.Digest = ""
	normalizeEnvelope(&envelope)
	if err := validateEnvelope(envelope); err != nil {
		return Envelope{}, nil, err
	}
	unsigned, err := json.Marshal(envelope)
	if err != nil {
		return Envelope{}, nil, err
	}
	envelope.Digest = hash(unsigned)
	raw, err := json.Marshal(envelope)
	if err != nil {
		return Envelope{}, nil, err
	}
	if len(raw) > MaxEnvelopeBytes {
		return Envelope{}, nil, ErrInvalidEnvelope
	}
	return envelope, raw, nil
}

func DecodeEnvelope(raw []byte) (Envelope, []byte, error) {
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes {
		return Envelope{}, nil, ErrInvalidEnvelope
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, nil, ErrInvalidEnvelope
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Envelope{}, nil, err
	}
	expected := envelope.Digest
	envelope.Digest = ""
	normalizeEnvelope(&envelope)
	if err := validateEnvelope(envelope); err != nil || !validDigest(expected) {
		return Envelope{}, nil, ErrInvalidEnvelope
	}
	unsigned, err := json.Marshal(envelope)
	if err != nil || hash(unsigned) != expected {
		return Envelope{}, nil, fmt.Errorf("%w: digest mismatch", ErrInvalidEnvelope)
	}
	envelope.Digest = expected
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return Envelope{}, nil, err
	}
	return envelope, canonical, nil
}

func validateEnvelope(envelope Envelope) error {
	if envelope.Schema != EnvelopeSchema || !namespaced(envelope.ResultID, "result:") || !validDigest(envelope.IdempotencyKeyDigest) ||
		!identifier(envelope.ProjectID) || !namespaced(envelope.TaskID, "task:") || envelope.TaskRevision < 1 || envelope.GraphRevision < 1 ||
		!identifier(envelope.WorkPackageID) || !namespaced(envelope.ExecutionID, "execution:") || !namespaced(envelope.WorkspaceID, "workspace:") ||
		!validContractReference(envelope.Contract) || !validAssignmentReference(envelope.Assignment) ||
		!validRegistryReference(envelope.Worker, "worker:") || !validBinding(envelope.Runtime, "runtime:") || !validBinding(envelope.Node, "node:") ||
		!validRegistryReference(envelope.Provenance.Trade, "trade:") || !validBinding(envelope.Provenance.Instruction, "instruction:") || !validDigest(envelope.Provenance.ContextDigest) ||
		!validBinding(envelope.Provenance.Provider, "provider:") || !validBinding(envelope.Provenance.Model, "model:") ||
		!validOutcome(envelope.ClaimedOutcome) || envelope.CreatedAt.IsZero() || !utc(envelope.CreatedAt) ||
		len(envelope.Patches) > MaxReferences || len(envelope.Artifacts) > MaxReferences || len(envelope.Tests) > MaxReferences {
		return ErrInvalidEnvelope
	}
	if err := validateContentReferences(envelope.Patches, "patch:"); err != nil {
		return err
	}
	if err := validateContentReferences(envelope.Artifacts, "artifact:"); err != nil {
		return err
	}
	if envelope.Handoff != nil && !validContentReference(*envelope.Handoff, "handoff:") {
		return ErrInvalidEnvelope
	}
	if envelope.Telemetry != nil && !validContentReference(*envelope.Telemetry, "telemetry:") {
		return ErrInvalidEnvelope
	}
	previous := ""
	for _, test := range envelope.Tests {
		if !namespaced(test.EvidenceID, "test-evidence:") || !identifier(test.Name) || (test.Outcome != "passed" && test.Outcome != "failed" && test.Outcome != "skipped") || !validDigest(test.Digest) || test.EvidenceID == previous {
			return ErrInvalidEnvelope
		}
		previous = test.EvidenceID
	}
	return nil
}

func normalizeEnvelope(envelope *Envelope) {
	envelope.Patches = append([]ContentReference(nil), envelope.Patches...)
	envelope.Artifacts = append([]ContentReference(nil), envelope.Artifacts...)
	envelope.Tests = append([]TestEvidence(nil), envelope.Tests...)
	sort.Slice(envelope.Patches, func(i, j int) bool { return envelope.Patches[i].ID < envelope.Patches[j].ID })
	sort.Slice(envelope.Artifacts, func(i, j int) bool { return envelope.Artifacts[i].ID < envelope.Artifacts[j].ID })
	sort.Slice(envelope.Tests, func(i, j int) bool { return envelope.Tests[i].EvidenceID < envelope.Tests[j].EvidenceID })
}

func validateContentReferences(values []ContentReference, prefix string) error {
	previous := ""
	for _, value := range values {
		if !validContentReference(value, prefix) || value.ID == previous {
			return ErrInvalidEnvelope
		}
		previous = value.ID
	}
	return nil
}

func validContentReference(value ContentReference, prefix string) bool {
	return namespaced(value.ID, prefix) && validDigest(value.Digest) && value.Size >= 0 && value.Size <= MaxEnvelopeBytes
}

func validContractReference(value executioncontract.ContractReference) bool {
	return namespaced(value.ContractID, "contract:") && value.Version > 0 && validDigest(value.Digest)
}

func validAssignmentReference(value AssignmentReference) bool {
	return namespaced(value.AssignmentID, "assignment:") && value.Version > 0 && validDigest(value.Digest)
}

func validRegistryReference(value project.RegistryReference, prefix string) bool {
	return namespaced(value.ID, prefix) && value.Version > 0 && validDigest(value.Digest)
}

func validBinding(value executioncontract.BindingReference, prefix string) bool {
	return namespaced(value.ID, prefix) && value.Version > 0 && validDigest(value.Digest)
}

func identifier(value string) bool {
	if len(value) < 1 || len(value) > project.MaxIdentifierBytes {
		return false
	}
	for index, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == ':' || character == '-'
		if !valid || index == 0 && !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func namespaced(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && identifier(value)
}

func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func validOutcome(value string) bool {
	return value == "succeeded" || value == "failed" || value == "canceled" || value == "partial"
}

func utc(value time.Time) bool { _, offset := value.Zone(); return offset == 0 }

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return ErrInvalidEnvelope
}
