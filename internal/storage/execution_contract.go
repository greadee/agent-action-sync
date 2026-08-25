package storage

import (
	"context"
	"time"
)

type ExecutionContractRecord struct {
	ContractID        string
	Version           int64
	ProjectID         string
	TaskID            string
	TaskRevision      int64
	GraphRevision     int64
	WorkPackageID     string
	ExecutionID       string
	Digest            string
	PredecessorDigest string
	ContractJSON      []byte
	CreatedAt         time.Time
}

type ExecutionContractQuery struct {
	Page          PageRequest
	ProjectID     string
	WorkPackageID string
	ExecutionID   string
}

type ExecutionContractStore interface {
	SaveExecutionContract(context.Context, ExecutionContractRecord) (RegistryWriteResult, error)
	GetExecutionContract(context.Context, string, int64) (ExecutionContractRecord, error)
	ListExecutionContracts(context.Context, ExecutionContractQuery) (Page[ExecutionContractRecord], error)
}
