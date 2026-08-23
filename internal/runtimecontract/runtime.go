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
	CodeTimedOut              ErrorCode = "timed_out"
	CodeRateLimited           ErrorCode = "rate_limited"
	CodeRefused               ErrorCode = "refused"
	CodeExecutionFailed       ErrorCode = "execution_failed"
	CodeMalformedOutput       ErrorCode = "malformed_output"
	CodeDisconnected          ErrorCode = "disconnected"
	CodeUncertainTermination  ErrorCode = "uncertain_termination"
	CodeBudgetExceeded        ErrorCode = "budget_exceeded"
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
	AttemptID            string
	LeaseGeneration      int64
	FencingDigest        string
	WorkspaceID          string
	ContextBundle        []byte
	InstructionBundle    []byte
	IdempotencyKeyDigest string
	ResumeKeyDigest      string
}

type Session struct {
	SessionID       string
	Contract        executioncontract.ContractReference
	WorkspaceID     string
	AttemptID       string
	LeaseGeneration int64
	FencingDigest   string
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
	SessionID    string
	Status       Status
	Sequence     int64
	UpdatedAt    time.Time
	ErrorCode    ErrorCode
	Retryable    bool
	ProgressCode string
	Usage        *UsageEvidence
}

type UsageEvidence struct {
	Source                string                             `json:"source"`
	Runtime               executioncontract.BindingReference `json:"runtime"`
	Provider              executioncontract.BindingReference `json:"provider"`
	Model                 executioncontract.BindingReference `json:"model"`
	InputTokens           *int64                             `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64                             `json:"cached_input_tokens,omitempty"`
	OutputTokens          *int64                             `json:"output_tokens,omitempty"`
	ReasoningOutputTokens *int64                             `json:"reasoning_output_tokens,omitempty"`
	ToolCalls             int64                              `json:"tool_calls"`
}

type CollectedResult struct {
	ResultID       string `json:"result_id"`
	EnvelopeDigest string `json:"envelope_digest"`
	ClaimedOutcome string `json:"claimed_outcome"`
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
