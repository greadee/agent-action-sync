// Package runtimecontract defines the provider-neutral runtime lifecycle.
// Implementations receive immutable authority and opaque workspace IDs. They
// do not own project history, credentials, sync transport, or result intake.
package runtimecontract

import (
	"context"
	"errors"
	"time"

	"syncgate/internal/executioncontract"
)

type Status string

const (
	StatusPrepared  Status = "prepared"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusCanceled  Status = "canceled"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusClosed    Status = "closed"
)

type ErrorCode string

const (
	CodeInvalidRequest        ErrorCode = "invalid_request"
	CodeNotFound              ErrorCode = "not_found"
	CodeConflict              ErrorCode = "conflict"
	CodeInvalidTransition     ErrorCode = "invalid_transition"
	CodeCapabilityUnavailable ErrorCode = "capability_unavailable"
	CodeUnavailable           ErrorCode = "runtime_unavailable"
	CodeCanceled              ErrorCode = "canceled"
)

type NormalizedError struct {
	Code      ErrorCode
	Retryable bool
	Message   string
}

func (err *NormalizedError) Error() string { return string(err.Code) + ": " + err.Message }

func IsCode(err error, code ErrorCode) bool {
	var normalized *NormalizedError
	return errors.As(err, &normalized) && normalized.Code == code
}

type NegotiationRequest struct {
	Contract             executioncontract.Contract
	RequiredCapabilities []executioncontract.Capability
}

type NegotiationResult struct {
	Capabilities  []executioncontract.Capability
	BindingDigest string
}

type PrepareRequest struct {
	Contract             executioncontract.Contract
	WorkspaceID          string
	IdempotencyKeyDigest string
	ResumeKeyDigest      string
}

type Session struct {
	SessionID       string
	Contract        executioncontract.ContractReference
	WorkspaceID     string
	ResumeKeyDigest string
	Status          Status
	Sequence        int64
	UpdatedAt       time.Time
}

type ActionRequest struct {
	SessionID            string
	IdempotencyKeyDigest string
}

type Observation struct {
	SessionID string
	Status    Status
	Sequence  int64
	UpdatedAt time.Time
	ErrorCode ErrorCode
}

type CollectedResult struct {
	ResultID       string
	EnvelopeDigest string
	ClaimedOutcome string
}

type Adapter interface {
	Negotiate(context.Context, NegotiationRequest) (NegotiationResult, error)
	Prepare(context.Context, PrepareRequest) (Session, error)
	Start(context.Context, ActionRequest) (Observation, error)
	Observe(context.Context, string) (Observation, error)
	Pause(context.Context, ActionRequest) (Observation, error)
	Resume(context.Context, ActionRequest) (Observation, error)
	Cancel(context.Context, ActionRequest) (Observation, error)
	CollectResult(context.Context, ActionRequest) (CollectedResult, error)
	Close(context.Context, ActionRequest) error
}
