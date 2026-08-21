package storage

import (
	"context"
	"time"
)

// ExecutionTelemetryRecord is local authority evidence. Its canonical JSON is
// bounded and reference-only; raw evidence belongs in a separately approved
// artifact store and is never copied into portable project history.
type ExecutionTelemetryRecord struct {
	TelemetryID          string
	TelemetryDigest      string
	IdempotencyKeyDigest string
	ProjectID            string
	ExecutionID          string
	ContractID           string
	ContractVersion      int64
	ContractDigest       string
	FinalOutcome         string
	SummaryJSON          []byte
	CreatedAt            time.Time
}

type ExecutionTelemetryStore interface {
	SaveExecutionTelemetry(context.Context, ExecutionTelemetryRecord) (RegistryWriteResult, error)
	GetExecutionTelemetry(context.Context, string) (ExecutionTelemetryRecord, error)
	ListExecutionTelemetry(context.Context, string, string) ([]ExecutionTelemetryRecord, error)
}
