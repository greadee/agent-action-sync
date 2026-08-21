package storage

import (
	"context"
	"time"
)

type ResultIntakeRecord struct {
	ResultID             string
	EnvelopeDigest       string
	IdempotencyKeyDigest string
	ProjectID            string
	ExecutionID          string
	ContractID           string
	ContractVersion      int64
	ContractDigest       string
	AssignmentID         string
	AssignmentDigest     string
	Decision             string
	ReasonCode           string
	EnvelopeJSON         []byte
	DecidedAt            time.Time
	DecidedBy            string
}

type ResultIntakeStore interface {
	SaveResultIntake(context.Context, ResultIntakeRecord) (RegistryWriteResult, error)
	GetResultIntake(context.Context, string) (ResultIntakeRecord, error)
}
