// Package codexruntime implements an opt-in supervised Codex CLI adapter.
// It retains only normalized lifecycle evidence; prompts, context, command
// output, credentials, process IDs, and local paths never enter durable state.
package codexruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"syncgate/internal/contextcompiler"
	"syncgate/internal/executioncontract"
	"syncgate/internal/runtimecontract"
)

const (
	stateSchema         = "syncgate.codex-runtime-state.v1"
	maxInstructionBytes = 256 << 10
	maxFinalBytes       = 64 << 10
	maxEventBytes       = 1 << 20
	maxStreamEvents     = 4096
)

type WorkspaceBinding struct {
	WorkspaceID     string
	AttemptID       string
	LeaseGeneration int64
	FencingDigest   string
}

type WorkspaceResolver func(context.Context, WorkspaceBinding) (string, error)

// ResultPublication contains only immutable authority bindings and the terminal
// worker claim. A publisher, not the model, creates the canonical result ID and
// envelope digest used by result intake.
type ResultPublication struct {
	Session        runtimecontract.Session
	Contract       executioncontract.Contract
	ClaimedOutcome string
}

type ResultPublisher interface {
	PublishResult(context.Context, ResultPublication) (runtimecontract.CollectedResult, error)
}

type Config struct {
	Enabled       bool
	Executable    string
	CodexHome     string
	StateRoot     string
	Runtime       executioncontract.BindingReference
	Provider      executioncontract.BindingReference
	Model         executioncontract.BindingReference
	Node          executioncontract.BindingReference
	Capabilities  []executioncontract.Capability
	MaxConcurrent int
	Workspace     WorkspaceResolver
	Now           func() time.Time
	Executor      Executor
	Publisher     ResultPublisher
}

type Invocation struct {
	Executable      string
	Arguments       []string
	Directory       string
	Environment     []string
	Stdin           []byte
	FinalOutputPath string
}

type Execution struct{ FinalMessage []byte }

type EventSink func([]byte) error

type Executor interface {
	Run(context.Context, Invocation, EventSink) (Execution, error)
}

type RunnerError struct {
	Code      runtimecontract.ErrorCode
	Retryable bool
}

func (err *RunnerError) Error() string { return string(err.Code) }

type Adapter struct {
	mu           sync.Mutex
	config       Config
	sessions     map[string]*sessionState
	preparations map[string]preparationReplay
	actions      map[string]runtimecontract.Observation
	slots        chan struct{}
}

type sessionState struct {
	Session         runtimecontract.Session
	Contract        executioncontract.Contract
	Input           *preparedInput
	Result          runtimecontract.CollectedResult
	ErrorCode       runtimecontract.ErrorCode
	Retryable       bool
	ProgressCode    string
	Usage           *runtimecontract.UsageEvidence
	Cancel          context.CancelFunc
	CancelRequested bool
}

type preparedInput struct{ Context, Instruction []byte }
type preparationReplay struct{ Fingerprint, SessionID string }

type durableSession struct {
	Schema       string                          `json:"schema"`
	Session      runtimecontract.Session         `json:"session"`
	Result       runtimecontract.CollectedResult `json:"result,omitempty"`
	ErrorCode    runtimecontract.ErrorCode       `json:"error_code,omitempty"`
	Retryable    bool                            `json:"retryable"`
	ProgressCode string                          `json:"progress_code,omitempty"`
	Usage        *runtimecontract.UsageEvidence  `json:"usage,omitempty"`
}

func New(config Config) (*Adapter, error) {
	config.Capabilities = sortedCapabilities(config.Capabilities)
	if config.MaxConcurrent < 1 || config.MaxConcurrent > 2 || config.Workspace == nil || config.Now == nil || !filepath.IsAbs(config.Executable) || !filepath.IsAbs(config.CodexHome) || !filepath.IsAbs(config.StateRoot) || !binding(config.Runtime, "runtime:") || !binding(config.Provider, "provider:") || !binding(config.Model, "model:") || !binding(config.Node, "node:") {
		return nil, normalized(runtimecontract.CodeInvalidRequest, false, "codex runtime configuration is incomplete")
	}
	if config.Executor == nil {
		config.Executor = OSExecutor{}
	}
	resolvedHome, homeErr := canonicalDirectory(config.CodexHome)
	resolvedState, stateErr := filepath.Abs(config.StateRoot)
	if homeErr != nil || stateErr != nil || pathsOverlap(resolvedHome, filepath.Clean(resolvedState)) {
		return nil, normalized(runtimecontract.CodeInvalidRequest, false, "codex credential and state roots must be separate directories")
	}
	if err := os.MkdirAll(filepath.Join(resolvedState, "sessions"), 0o700); err != nil {
		return nil, normalized(runtimecontract.CodeUnavailable, true, "codex runtime state is unavailable")
	}
	resolvedState, stateErr = canonicalDirectory(resolvedState)
	if stateErr != nil || pathsOverlap(resolvedHome, resolvedState) {
		return nil, normalized(runtimecontract.CodeInvalidRequest, false, "codex credential and state roots must be separate directories")
	}
	config.CodexHome = resolvedHome
	config.StateRoot = resolvedState
	adapter := &Adapter{config: config, sessions: map[string]*sessionState{}, preparations: map[string]preparationReplay{}, actions: map[string]runtimecontract.Observation{}, slots: make(chan struct{}, config.MaxConcurrent)}
	if err := adapter.loadDurableSessions(); err != nil {
		return nil, err
	}
	return adapter, nil
}

func (adapter *Adapter) Negotiate(ctx context.Context, request runtimecontract.NegotiationRequest) (runtimecontract.NegotiationResult, error) {
	if err := contextErr(ctx); err != nil {
		return runtimecontract.NegotiationResult{}, err
	}
	if !adapter.config.Enabled {
		return runtimecontract.NegotiationResult{}, normalized(runtimecontract.CodeUnavailable, false, "codex runtime is disabled")
	}
	if err := adapter.validateContract(request.Contract); err != nil {
		return runtimecontract.NegotiationResult{}, err
	}
	required := sortedCapabilities(request.RequiredCapabilities)
	for _, capability := range required {
		if !hasCapability(request.Contract.Permissions.Capabilities, capability) || !hasCapability(adapter.config.Capabilities, capability) {
			return runtimecontract.NegotiationResult{}, normalized(runtimecontract.CodeCapabilityUnavailable, false, "required runtime capability is unavailable")
		}
	}
	if err := enforceablePolicy(request.Contract); err != nil {
		return runtimecontract.NegotiationResult{}, err
	}
	payload, _ := json.Marshal(struct {
		Runtime, Provider, Model, Node executioncontract.BindingReference
		Capabilities                   []executioncontract.Capability
	}{adapter.config.Runtime, adapter.config.Provider, adapter.config.Model, adapter.config.Node, required})
	return runtimecontract.NegotiationResult{Capabilities: required, BindingDigest: digest(payload)}, nil
}

func (adapter *Adapter) Prepare(ctx context.Context, request runtimecontract.PrepareRequest) (runtimecontract.Session, error) {
	if err := contextErr(ctx); err != nil {
		return runtimecontract.Session{}, err
	}
	if !validReference(request.AssignmentID, "assignment:") || !validReference(request.AttemptID, "attempt:") || request.LeaseGeneration < 1 || !validDigest(request.FencingDigest) || !validReference(request.WorkspaceID, "workspace:") || !validDigest(request.IdempotencyKeyDigest) || !validDigest(request.ResumeKeyDigest) || len(request.InstructionBundle) == 0 || len(request.InstructionBundle) > maxInstructionBytes || !utf8.Valid(request.InstructionBundle) || digest(request.InstructionBundle) != request.Contract.Instruction.Digest || contextcompiler.VerifyBundleBytes(request.ContextBundle, request.Contract.ContextDigest) != nil {
		return runtimecontract.Session{}, normalized(runtimecontract.CodeInvalidRequest, false, "prepare input does not match immutable authority")
	}
	if _, err := adapter.Negotiate(ctx, runtimecontract.NegotiationRequest{Contract: request.Contract, RequiredCapabilities: request.Contract.Permissions.Capabilities}); err != nil {
		return runtimecontract.Session{}, err
	}
	fingerprint := digest([]byte(request.Contract.Digest + "\x00" + request.AssignmentID + "\x00" + request.AttemptID + "\x00" + fmt.Sprint(request.LeaseGeneration) + "\x00" + request.FencingDigest + "\x00" + request.WorkspaceID + "\x00" + request.ResumeKeyDigest + "\x00" + request.Contract.ContextDigest + "\x00" + request.Contract.Instruction.Digest))
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if replay, ok := adapter.preparations[request.IdempotencyKeyDigest]; ok {
		if replay.Fingerprint != fingerprint {
			return runtimecontract.Session{}, normalized(runtimecontract.CodeConflict, false, "prepare idempotency key was reused")
		}
		return adapter.sessions[replay.SessionID].Session, nil
	}
	sessionID := "runtime-session:" + digest([]byte(request.Contract.Digest + request.IdempotencyKeyDigest))[:32]
	if existing, ok := adapter.sessions[sessionID]; ok {
		contractReference := contractRef(request.Contract)
		if existing.Session.Contract != contractReference || existing.Session.AssignmentID != request.AssignmentID || existing.Session.WorkspaceID != request.WorkspaceID || existing.Session.AttemptID != request.AttemptID || existing.Session.LeaseGeneration != request.LeaseGeneration || existing.Session.FencingDigest != request.FencingDigest || existing.Session.ResumeKeyDigest != request.ResumeKeyDigest {
			return runtimecontract.Session{}, normalized(runtimecontract.CodeConflict, false, "prepare identity conflicts with recovered runtime state")
		}
		adapter.preparations[request.IdempotencyKeyDigest] = preparationReplay{Fingerprint: fingerprint, SessionID: sessionID}
		return existing.Session, nil
	}
	session := runtimecontract.Session{SessionID: sessionID, Contract: contractRef(request.Contract), AssignmentID: request.AssignmentID, WorkspaceID: request.WorkspaceID, AttemptID: request.AttemptID, LeaseGeneration: request.LeaseGeneration, FencingDigest: request.FencingDigest, ResumeKeyDigest: request.ResumeKeyDigest, Status: runtimecontract.StatusPrepared, Sequence: 1, UpdatedAt: adapter.now()}
	state := &sessionState{Session: session, Contract: request.Contract, Input: &preparedInput{Context: append([]byte(nil), request.ContextBundle...), Instruction: append([]byte(nil), request.InstructionBundle...)}, ProgressCode: "prepared"}
	adapter.sessions[sessionID] = state
	adapter.preparations[request.IdempotencyKeyDigest] = preparationReplay{Fingerprint: fingerprint, SessionID: sessionID}
	if err := adapter.persist(state); err != nil {
		delete(adapter.sessions, sessionID)
		delete(adapter.preparations, request.IdempotencyKeyDigest)
		return runtimecontract.Session{}, err
	}
	return session, nil
}

func (adapter *Adapter) Start(ctx context.Context, request runtimecontract.ActionRequest) (runtimecontract.Observation, error) {
	if err := contextErr(ctx); err != nil {
		return runtimecontract.Observation{}, err
	}
	if !validAction(request) {
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeInvalidRequest, false, "start request is incomplete")
	}
	adapter.mu.Lock()
	if prior, ok := adapter.actions["start:"+request.IdempotencyKeyDigest]; ok {
		adapter.mu.Unlock()
		if prior.SessionID != request.SessionID {
			return runtimecontract.Observation{}, normalized(runtimecontract.CodeConflict, false, "action idempotency key was reused")
		}
		return prior, nil
	}
	state, ok := adapter.sessions[request.SessionID]
	if !ok {
		adapter.mu.Unlock()
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeNotFound, false, "runtime session was not found")
	}
	if state.Session.Status != runtimecontract.StatusPrepared || state.Input == nil {
		adapter.mu.Unlock()
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeInvalidTransition, false, "runtime session is not prepared")
	}
	now := adapter.now()
	if now.Before(state.Contract.NotBefore) {
		adapter.mu.Unlock()
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeUnavailable, true, "execution contract is not active")
	}
	if !now.Before(state.Contract.Deadline) {
		adapter.mu.Unlock()
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeTimedOut, false, "execution contract deadline passed")
	}
	select {
	case adapter.slots <- struct{}{}:
	default:
		adapter.mu.Unlock()
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeUnavailable, true, "runtime concurrency limit is reached")
	}
	runCtx, cancel := adapter.executionContext(context.Background(), state.Contract)
	state.Cancel = cancel
	previousSession := state.Session
	previousProgress := state.ProgressCode
	state.Session.Status = runtimecontract.StatusRunning
	state.Session.Sequence++
	state.Session.UpdatedAt = now
	state.ProgressCode = "started"
	observation := adapter.observe(state)
	adapter.actions["start:"+request.IdempotencyKeyDigest] = observation
	if err := adapter.persist(state); err != nil {
		cancel()
		<-adapter.slots
		state.Cancel = nil
		state.Session = previousSession
		state.ProgressCode = previousProgress
		delete(adapter.actions, "start:"+request.IdempotencyKeyDigest)
		adapter.mu.Unlock()
		return runtimecontract.Observation{}, err
	}
	input := *state.Input
	state.Input = nil
	adapter.mu.Unlock()
	go adapter.execute(runCtx, request.SessionID, input)
	return observation, nil
}

func (adapter *Adapter) Observe(ctx context.Context, sessionID string) (runtimecontract.Observation, error) {
	if err := contextErr(ctx); err != nil {
		return runtimecontract.Observation{}, err
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	state, ok := adapter.sessions[sessionID]
	if !ok {
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeNotFound, false, "runtime session was not found")
	}
	return adapter.observe(state), nil
}

func (adapter *Adapter) Pause(context.Context, runtimecontract.ActionRequest) (runtimecontract.Observation, error) {
	return runtimecontract.Observation{}, normalized(runtimecontract.CodeCapabilityUnavailable, false, "codex exec does not support a safe resumable pause")
}
func (adapter *Adapter) Resume(context.Context, runtimecontract.ActionRequest) (runtimecontract.Observation, error) {
	return runtimecontract.Observation{}, normalized(runtimecontract.CodeCapabilityUnavailable, false, "codex exec does not support a safe resumable pause")
}

func (adapter *Adapter) Cancel(ctx context.Context, request runtimecontract.ActionRequest) (runtimecontract.Observation, error) {
	if err := contextErr(ctx); err != nil {
		return runtimecontract.Observation{}, err
	}
	if !validAction(request) {
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeInvalidRequest, false, "cancel request is incomplete")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	key := "cancel:" + request.IdempotencyKeyDigest
	if prior, ok := adapter.actions[key]; ok {
		if prior.SessionID != request.SessionID {
			return runtimecontract.Observation{}, normalized(runtimecontract.CodeConflict, false, "action idempotency key was reused")
		}
		return prior, nil
	}
	state, ok := adapter.sessions[request.SessionID]
	if !ok {
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeNotFound, false, "runtime session was not found")
	}
	if state.Session.Status != runtimecontract.StatusRunning {
		return runtimecontract.Observation{}, normalized(runtimecontract.CodeInvalidTransition, false, "only a running session can be canceled")
	}
	state.CancelRequested = true
	state.Session.Status = runtimecontract.StatusCanceled
	state.Session.Sequence++
	state.Session.UpdatedAt = adapter.now()
	state.ErrorCode = runtimecontract.CodeCanceled
	state.ProgressCode = "cancel_requested"
	if state.Cancel != nil {
		state.Cancel()
	}
	observation := adapter.observe(state)
	adapter.actions[key] = observation
	if err := adapter.persist(state); err != nil {
		return runtimecontract.Observation{}, err
	}
	return observation, nil
}

func (adapter *Adapter) CollectResult(ctx context.Context, request runtimecontract.ActionRequest) (runtimecontract.CollectedResult, error) {
	if err := contextErr(ctx); err != nil {
		return runtimecontract.CollectedResult{}, err
	}
	if !validAction(request) {
		return runtimecontract.CollectedResult{}, normalized(runtimecontract.CodeInvalidRequest, false, "collect request is incomplete")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	state, ok := adapter.sessions[request.SessionID]
	if !ok {
		return runtimecontract.CollectedResult{}, normalized(runtimecontract.CodeNotFound, false, "runtime session was not found")
	}
	if state.Session.Status != runtimecontract.StatusSucceeded && state.Session.Status != runtimecontract.StatusFailed && state.Session.Status != runtimecontract.StatusCanceled {
		return runtimecontract.CollectedResult{}, normalized(runtimecontract.CodeInvalidTransition, false, "result is unavailable")
	}
	if !validReference(state.Result.ResultID, "result:") || !validDigest(state.Result.EnvelopeDigest) {
		return runtimecontract.CollectedResult{}, normalized(runtimecontract.CodeUnavailable, true, "runtime did not stage a valid result reference")
	}
	return state.Result, nil
}

func (adapter *Adapter) Close(ctx context.Context, request runtimecontract.ActionRequest) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if !validAction(request) {
		return normalized(runtimecontract.CodeInvalidRequest, false, "close request is incomplete")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	key := "close:" + request.IdempotencyKeyDigest
	if prior, ok := adapter.actions[key]; ok {
		if prior.SessionID != request.SessionID {
			return normalized(runtimecontract.CodeConflict, false, "action idempotency key was reused")
		}
		return nil
	}
	state, ok := adapter.sessions[request.SessionID]
	if !ok {
		return normalized(runtimecontract.CodeNotFound, false, "runtime session was not found")
	}
	if state.Cancel != nil {
		return normalized(runtimecontract.CodeInvalidTransition, false, "active session must terminate before close")
	}
	if state.Session.Status == runtimecontract.StatusClosed {
		return normalized(runtimecontract.CodeInvalidTransition, false, "runtime session is already closed")
	}
	previousSession := state.Session
	previousInput := state.Input
	previousProgress := state.ProgressCode
	state.Input = nil
	state.Session.Status = runtimecontract.StatusClosed
	state.Session.Sequence++
	state.Session.UpdatedAt = adapter.now()
	state.ProgressCode = "closed"
	if err := adapter.persist(state); err != nil {
		state.Session = previousSession
		state.Input = previousInput
		state.ProgressCode = previousProgress
		return err
	}
	adapter.actions[key] = adapter.observe(state)
	return nil
}

func (adapter *Adapter) execute(ctx context.Context, sessionID string, input preparedInput) {
	defer func() { <-adapter.slots }()
	adapter.mu.Lock()
	state := adapter.sessions[sessionID]
	contract := state.Contract
	runtimeSession := state.Session
	workspaceID := runtimeSession.WorkspaceID
	adapter.mu.Unlock()
	workspacePath, err := adapter.config.Workspace(ctx, WorkspaceBinding{WorkspaceID: workspaceID, AttemptID: runtimeSession.AttemptID, LeaseGeneration: runtimeSession.LeaseGeneration, FencingDigest: runtimeSession.FencingDigest})
	if err != nil {
		adapter.finish(sessionID, runtimecontract.StatusFailed, runtimecontract.CodeUnavailable, true, "workspace_unavailable", runtimecontract.CollectedResult{}, nil)
		return
	}
	workspacePath, err = canonicalDirectory(workspacePath)
	if err != nil || pathsOverlap(workspacePath, adapter.config.StateRoot) || pathsOverlap(workspacePath, adapter.config.CodexHome) {
		adapter.finish(sessionID, runtimecontract.StatusFailed, runtimecontract.CodeUnavailable, false, "workspace_unavailable", runtimecontract.CollectedResult{}, nil)
		return
	}
	sessionRoot := filepath.Join(adapter.config.StateRoot, "runs", digest([]byte(sessionID))[:32])
	if os.MkdirAll(sessionRoot, 0o700) != nil {
		adapter.finish(sessionID, runtimecontract.StatusFailed, runtimecontract.CodeUnavailable, true, "state_unavailable", runtimecontract.CollectedResult{}, nil)
		return
	}
	schemaPath := filepath.Join(sessionRoot, "result.schema.json")
	finalPath := filepath.Join(sessionRoot, "result.json")
	if os.WriteFile(schemaPath, resultSchema, 0o600) != nil {
		adapter.finish(sessionID, runtimecontract.StatusFailed, runtimecontract.CodeUnavailable, true, "schema_unavailable", runtimecontract.CollectedResult{}, nil)
		return
	}
	prompt, err := buildPrompt(runtimeSession, contract, input)
	if err != nil {
		adapter.finish(sessionID, runtimecontract.StatusFailed, runtimecontract.CodeInvalidRequest, false, "input_invalid", runtimecontract.CollectedResult{}, nil)
		return
	}
	sandbox := "read-only"
	if hasCapability(contract.Permissions.Capabilities, executioncontract.CapabilityWrite) {
		sandbox = "workspace-write"
	}
	shellEnabled := hasCapability(contract.Permissions.Capabilities, executioncontract.CapabilityShell)
	invocation := Invocation{Executable: adapter.config.Executable, Directory: workspacePath, Environment: minimalEnvironment(adapter.config.CodexHome), Stdin: prompt, FinalOutputPath: finalPath, Arguments: []string{"exec", "--ephemeral", "--ignore-user-config", "--strict-config", "--json", "--ask-for-approval", "never", "--sandbox", sandbox, "--model", strings.TrimPrefix(contract.Model.ID, "model:"), "--cd", workspacePath, "-c", `web_search="disabled"`, "-c", "tools.web_search=false", "-c", "sandbox_workspace_write.network_access=false", "-c", `history.persistence="none"`, "-c", "hide_agent_reasoning=true", "-c", "feedback.enabled=false", "-c", "allow_login_shell=false", "-c", "shell_environment_policy.experimental_use_profile=false", "-c", "shell_environment_policy.ignore_default_excludes=false", "-c", `shell_environment_policy.filters={CODEX_HOME="exclude"}`, "-c", "features.skill_mcp_dependency_install=false", "-c", fmt.Sprintf("features.shell_tool=%t", shellEnabled), "-c", "features.rollout_budget.enabled=true", "-c", fmt.Sprintf("features.rollout_budget.limit_tokens=%d", contract.Budget.MaxTokens), "--output-schema", schemaPath, "--output-last-message", finalPath, "-"}}
	usage := &runtimecontract.UsageEvidence{Source: "provider_reported", Runtime: contract.Runtime, Provider: contract.Provider, Model: contract.Model}
	eventCount := 0
	execution, runErr := adapter.config.Executor.Run(ctx, invocation, func(line []byte) error {
		eventCount++
		if eventCount > maxStreamEvents {
			return &RunnerError{Code: runtimecontract.CodeBudgetExceeded}
		}
		progress, eventErr := consumeEvent(line, usage, contract.Budget.MaxToolCalls, contract.Budget.MaxTokens)
		if eventErr == nil && progress != "" {
			adapter.updateProgress(sessionID, progress, usage)
		}
		return eventErr
	})
	if runErr != nil {
		code, retryable := classifyRunError(ctx, runErr)
		status := runtimecontract.StatusFailed
		if code == runtimecontract.CodeCanceled {
			status = runtimecontract.StatusCanceled
		}
		adapter.finish(sessionID, status, code, retryable, "execution_"+string(code), runtimecontract.CollectedResult{}, usage)
		return
	}
	claim, err := decodeFinal(execution.FinalMessage, runtimeSession)
	if err != nil {
		adapter.finish(sessionID, runtimecontract.StatusFailed, runtimecontract.CodeMalformedOutput, false, "result_malformed", runtimecontract.CollectedResult{}, usage)
		return
	}
	status := runtimecontract.StatusSucceeded
	code := runtimecontract.ErrorCode("")
	progress := "completed"
	if claim.ClaimedOutcome == "refused" {
		status, code, progress = runtimecontract.StatusFailed, runtimecontract.CodeRefused, "refused"
	} else if claim.ClaimedOutcome == "failed" {
		status, code, progress = runtimecontract.StatusFailed, runtimecontract.CodeExecutionFailed, "worker_failed"
	}
	result := runtimecontract.CollectedResult{}
	if status == runtimecontract.StatusSucceeded {
		if adapter.config.Publisher == nil {
			adapter.finish(sessionID, runtimecontract.StatusFailed, runtimecontract.CodeUnavailable, false, "result_publisher_unavailable", result, usage)
			return
		}
		result, err = adapter.config.Publisher.PublishResult(ctx, ResultPublication{Session: runtimeSession, Contract: contract, ClaimedOutcome: claim.ClaimedOutcome})
		if err != nil || !validReference(result.ResultID, "result:") || !validDigest(result.EnvelopeDigest) || result.ClaimedOutcome != claim.ClaimedOutcome {
			adapter.finish(sessionID, runtimecontract.StatusFailed, runtimecontract.CodeMalformedOutput, false, "result_publication_failed", runtimecontract.CollectedResult{}, usage)
			return
		}
	}
	adapter.finish(sessionID, status, code, false, progress, result, usage)
}

func (adapter *Adapter) finish(sessionID string, status runtimecontract.Status, code runtimecontract.ErrorCode, retryable bool, progress string, result runtimecontract.CollectedResult, usage *runtimecontract.UsageEvidence) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	state := adapter.sessions[sessionID]
	if state == nil {
		return
	}
	if state.CancelRequested {
		status, code, progress = runtimecontract.StatusCanceled, runtimecontract.CodeCanceled, "canceled"
		retryable = false
		result = runtimecontract.CollectedResult{}
	}
	if state.Cancel != nil {
		state.Cancel()
	}
	state.Cancel = nil
	state.Session.Status = status
	state.Session.Sequence++
	state.Session.UpdatedAt = adapter.now()
	state.ErrorCode = code
	state.Retryable = retryable
	state.ProgressCode = progress
	state.Result = result
	state.Usage = nullableUsage(usage)
	_ = adapter.persist(state)
}

func (adapter *Adapter) updateProgress(sessionID, progress string, usage *runtimecontract.UsageEvidence) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	state := adapter.sessions[sessionID]
	if state == nil || state.Session.Status != runtimecontract.StatusRunning {
		return
	}
	state.ProgressCode = progress
	state.Usage = nullableUsage(usage)
	_ = adapter.persist(state)
}

func (adapter *Adapter) observe(state *sessionState) runtimecontract.Observation {
	return runtimecontract.Observation{SessionID: state.Session.SessionID, Status: state.Session.Status, Sequence: state.Session.Sequence, UpdatedAt: state.Session.UpdatedAt, ErrorCode: state.ErrorCode, Retryable: state.Retryable, ProgressCode: state.ProgressCode, Usage: cloneUsage(state.Usage)}
}
func (adapter *Adapter) now() time.Time { return adapter.config.Now().UTC() }
func (adapter *Adapter) executionContext(parent context.Context, contract executioncontract.Contract) (context.Context, context.CancelFunc) {
	deadline := contract.Deadline
	wall := adapter.now().Add(time.Duration(contract.Budget.MaxWallClockSeconds) * time.Second)
	if wall.Before(deadline) {
		deadline = wall
	}
	return context.WithDeadline(parent, deadline)
}
func (adapter *Adapter) validateContract(contract executioncontract.Contract) error {
	if executioncontract.VerifyDigest(contract) != nil || contract.Runtime != adapter.config.Runtime || contract.Provider != adapter.config.Provider || contract.Model != adapter.config.Model || contract.Node != adapter.config.Node {
		return normalized(runtimecontract.CodeInvalidRequest, false, "execution contract runtime binding is invalid")
	}
	return nil
}

func enforceablePolicy(contract executioncontract.Contract) error {
	const maxDurationSeconds = int64((1<<63 - 1) / int64(time.Second))
	if contract.Budget.MaxTokens < 1 || contract.Budget.MaxCostMicros < 1 || contract.Budget.MaxWallClockSeconds < 1 || contract.Budget.MaxWallClockSeconds > maxDurationSeconds || contract.Budget.MaxRetries < 0 || contract.Budget.MaxToolCalls < 1 || contract.Budget.MaxConcurrentWorkers < 1 || contract.NotBefore.IsZero() || contract.Deadline.IsZero() || contract.Deadline.Before(contract.NotBefore) || contract.Deadline.Sub(contract.NotBefore) > time.Duration(contract.Budget.MaxWallClockSeconds)*time.Second {
		return normalized(runtimecontract.CodeInvalidRequest, false, "execution contract budget or time window is invalid")
	}
	for _, capability := range contract.Permissions.Capabilities {
		switch capability {
		case executioncontract.CapabilityInspect, executioncontract.CapabilityWrite, executioncontract.CapabilityShell, executioncontract.CapabilityTest, executioncontract.CapabilityBranch:
		default:
			return normalized(runtimecontract.CodeCapabilityUnavailable, false, "contract capability cannot be enforced by the codex sandbox")
		}
	}
	if !hasCapability(contract.Permissions.Capabilities, executioncontract.CapabilityShell) && (hasCapability(contract.Permissions.Capabilities, executioncontract.CapabilityInspect) || hasCapability(contract.Permissions.Capabilities, executioncontract.CapabilityTest) || hasCapability(contract.Permissions.Capabilities, executioncontract.CapabilityBranch)) {
		return normalized(runtimecontract.CodeCapabilityUnavailable, false, "inspect, test, and branch capabilities require the bounded shell tool")
	}
	if hasCapability(contract.Permissions.Capabilities, executioncontract.CapabilityInspect) && (len(contract.Permissions.InspectPaths) != 1 || contract.Permissions.InspectPaths[0] != ".") {
		return normalized(runtimecontract.CodeCapabilityUnavailable, false, "codex sandbox requires whole-worktree inspect scope")
	}
	if hasCapability(contract.Permissions.Capabilities, executioncontract.CapabilityWrite) && (len(contract.Permissions.WritePaths) != 1 || contract.Permissions.WritePaths[0] != "." || len(contract.Permissions.Forbidden) != 0) {
		return normalized(runtimecontract.CodeCapabilityUnavailable, false, "codex sandbox requires whole-worktree write scope")
	}
	if len(contract.Permissions.Forbidden) != 0 {
		return normalized(runtimecontract.CodeCapabilityUnavailable, false, "path exclusions cannot be enforced by the codex sandbox")
	}
	if len(contract.Permissions.SecretIDs) != 0 {
		return normalized(runtimecontract.CodeCapabilityUnavailable, false, "secret access is disabled")
	}
	return nil
}

func buildPrompt(session runtimecontract.Session, contract executioncontract.Contract, input preparedInput) ([]byte, error) {
	var contextValue json.RawMessage = input.Context
	payload := struct {
		Schema          string                              `json:"schema"`
		Contract        executioncontract.ContractReference `json:"contract"`
		SessionID       string                              `json:"session_id"`
		AttemptID       string                              `json:"attempt_id"`
		LeaseGeneration int64                               `json:"lease_generation"`
		FencingDigest   string                              `json:"fencing_digest"`
		Instruction     string                              `json:"instruction"`
		Context         json.RawMessage                     `json:"context"`
		Output          string                              `json:"output_requirement"`
	}{"syncgate.codex-input.v1", contractRef(contract), session.SessionID, session.AttemptID, session.LeaseGeneration, session.FencingDigest, string(input.Instruction), contextValue, "Return only the schema-conforming terminal claim with the exact supplied session, attempt, lease, fence, and contract bindings. The authority creates the result envelope. Never include secrets, paths, logs, or prose."}
	return json.Marshal(payload)
}

var resultSchema = []byte(`{"type":"object","properties":{"claimed_outcome":{"type":"string","enum":["succeeded","failed","refused"]},"contract_digest":{"type":"string","pattern":"^[a-f0-9]{64}$"},"session_id":{"type":"string","pattern":"^runtime-session:[A-Za-z0-9._:-]{1,120}$"},"attempt_id":{"type":"string","pattern":"^attempt:[A-Za-z0-9._:-]{1,120}$"},"lease_generation":{"type":"integer","minimum":1},"fencing_digest":{"type":"string","pattern":"^[a-f0-9]{64}$"}},"required":["claimed_outcome","contract_digest","session_id","attempt_id","lease_generation","fencing_digest"],"additionalProperties":false}`)

type codexFinal struct {
	ClaimedOutcome  string `json:"claimed_outcome"`
	ContractDigest  string `json:"contract_digest"`
	SessionID       string `json:"session_id"`
	AttemptID       string `json:"attempt_id"`
	LeaseGeneration int64  `json:"lease_generation"`
	FencingDigest   string `json:"fencing_digest"`
}

func decodeFinal(raw []byte, session runtimecontract.Session) (codexFinal, error) {
	if len(raw) == 0 || len(raw) > maxFinalBytes {
		return codexFinal{}, errors.New("invalid final result")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value codexFinal
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || (value.ClaimedOutcome != "succeeded" && value.ClaimedOutcome != "failed" && value.ClaimedOutcome != "refused") || value.ContractDigest != session.Contract.Digest || value.SessionID != session.SessionID || value.AttemptID != session.AttemptID || value.LeaseGeneration != session.LeaseGeneration || value.FencingDigest != session.FencingDigest {
		return codexFinal{}, errors.New("invalid final result")
	}
	return value, nil
}

func consumeEvent(line []byte, usage *runtimecontract.UsageEvidence, maxTools, maxTokens int64) (string, error) {
	if len(line) == 0 || len(line) > maxEventBytes {
		return "", &RunnerError{Code: runtimecontract.CodeMalformedOutput}
	}
	var event struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Item    struct {
			Type string `json:"type"`
		} `json:"item"`
		Usage struct {
			Input     int64 `json:"input_tokens"`
			Cached    int64 `json:"cached_input_tokens"`
			Output    int64 `json:"output_tokens"`
			Reasoning int64 `json:"reasoning_output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &event) != nil {
		return "", &RunnerError{Code: runtimecontract.CodeMalformedOutput}
	}
	progress := ""
	switch event.Type {
	case "thread.started":
		progress = "connected"
	case "turn.started":
		progress = "working"
	case "turn.completed":
		progress = "response_complete"
	case "error":
		message := strings.ToLower(event.Message)
		if strings.Contains(message, "rate limit") || strings.Contains(message, "too many requests") || strings.Contains(message, "429") {
			return "", &RunnerError{Code: runtimecontract.CodeRateLimited, Retryable: true}
		}
		return "", &RunnerError{Code: runtimecontract.CodeDisconnected, Retryable: true}
	case "turn.failed":
		return "", &RunnerError{Code: runtimecontract.CodeDisconnected, Retryable: true}
	}
	if event.Type == "item.started" && (event.Item.Type == "command_execution" || event.Item.Type == "file_change" || event.Item.Type == "mcp_tool_call") {
		usage.ToolCalls++
		if usage.ToolCalls > maxTools {
			return "", &RunnerError{Code: runtimecontract.CodeBudgetExceeded}
		}
		progress = "tool_active"
	}
	if event.Type == "turn.completed" {
		if event.Usage.Input < 0 || event.Usage.Cached < 0 || event.Usage.Output < 0 || event.Usage.Reasoning < 0 {
			return "", &RunnerError{Code: runtimecontract.CodeMalformedOutput}
		}
		usage.InputTokens = int64ptr(event.Usage.Input)
		usage.CachedInputTokens = int64ptr(event.Usage.Cached)
		usage.OutputTokens = int64ptr(event.Usage.Output)
		usage.ReasoningOutputTokens = int64ptr(event.Usage.Reasoning)
		if exceedsTokenBudget(maxTokens, event.Usage.Input, event.Usage.Output, event.Usage.Reasoning) {
			return "", &RunnerError{Code: runtimecontract.CodeBudgetExceeded}
		}
	}
	return progress, nil
}

func classifyRunError(ctx context.Context, err error) (runtimecontract.ErrorCode, bool) {
	var runner *RunnerError
	if errors.As(err, &runner) {
		return runner.Code, runner.Retryable
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return runtimecontract.CodeTimedOut, false
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return runtimecontract.CodeCanceled, false
	}
	return runtimecontract.CodeDisconnected, true
}

func (adapter *Adapter) persist(state *sessionState) error {
	record := durableSession{Schema: stateSchema, Session: state.Session, Result: state.Result, ErrorCode: state.ErrorCode, Retryable: state.Retryable, ProgressCode: state.ProgressCode, Usage: cloneUsage(state.Usage)}
	raw, err := json.Marshal(record)
	if err != nil {
		return normalized(runtimecontract.CodeUnavailable, true, "runtime state is unavailable")
	}
	path := adapter.statePath(state.Session.SessionID)
	temporary := path + ".tmp"
	if os.WriteFile(temporary, raw, 0o600) != nil || os.Rename(temporary, path) != nil {
		return normalized(runtimecontract.CodeUnavailable, true, "runtime state is unavailable")
	}
	return nil
}
func (adapter *Adapter) statePath(sessionID string) string {
	return filepath.Join(adapter.config.StateRoot, "sessions", digest([]byte(sessionID))+".json")
}
func (adapter *Adapter) loadDurableSessions() error {
	entries, err := os.ReadDir(filepath.Join(adapter.config.StateRoot, "sessions"))
	if err != nil {
		return normalized(runtimecontract.CodeUnavailable, true, "runtime state is unavailable")
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(adapter.config.StateRoot, "sessions", entry.Name()))
		if readErr != nil {
			return normalized(runtimecontract.CodeUnavailable, true, "runtime state is unavailable")
		}
		var record durableSession
		if json.Unmarshal(raw, &record) != nil || record.Schema != stateSchema || !namespaced(record.Session.SessionID, "runtime-session:") {
			return normalized(runtimecontract.CodeUnavailable, false, "runtime state is corrupt")
		}
		state := &sessionState{Session: record.Session, Result: record.Result, ErrorCode: record.ErrorCode, Retryable: record.Retryable, ProgressCode: record.ProgressCode, Usage: cloneUsage(record.Usage)}
		if state.Session.Status == runtimecontract.StatusPrepared || state.Session.Status == runtimecontract.StatusRunning || state.Session.Status == runtimecontract.StatusPaused {
			state.Session.Status = runtimecontract.StatusFailed
			state.Session.Sequence++
			state.Session.UpdatedAt = adapter.now()
			state.ErrorCode = runtimecontract.CodeUncertainTermination
			state.Retryable = false
			state.ProgressCode = "restart_uncertain"
			adapter.sessions[state.Session.SessionID] = state
			if err := adapter.persist(state); err != nil {
				return err
			}
		} else {
			adapter.sessions[state.Session.SessionID] = state
		}
	}
	return nil
}

type OSExecutor struct{}

func (OSExecutor) Run(ctx context.Context, invocation Invocation, sink EventSink) (Execution, error) {
	command := exec.CommandContext(ctx, invocation.Executable, invocation.Arguments...)
	command.Dir = invocation.Directory
	command.Env = append([]string(nil), invocation.Environment...)
	command.Stdin = bytes.NewReader(invocation.Stdin)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return Execution{}, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return Execution{}, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), maxEventBytes)
	var sinkErr error
	for scanner.Scan() {
		if sinkErr = sink(append([]byte(nil), scanner.Bytes()...)); sinkErr != nil {
			_ = command.Process.Kill()
			break
		}
	}
	waitErr := command.Wait()
	if sinkErr != nil {
		return Execution{}, sinkErr
	}
	if scanner.Err() != nil {
		return Execution{}, &RunnerError{Code: runtimecontract.CodeMalformedOutput}
	}
	if waitErr != nil {
		return Execution{}, waitErr
	}
	raw, err := os.ReadFile(invocation.FinalOutputPath)
	if err != nil || len(raw) > maxFinalBytes {
		return Execution{}, &RunnerError{Code: runtimecontract.CodeMalformedOutput}
	}
	return Execution{FinalMessage: raw}, nil
}

func minimalEnvironment(codexHome string) []string {
	keys := []string{"COMSPEC", "PATH", "PATHEXT", "SYSTEMROOT", "TEMP", "TMP", "WINDIR"}
	result := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			result = append(result, key+"="+value)
		}
	}
	result = append(result, "CODEX_HOME="+codexHome)
	sort.Strings(result)
	return result
}
func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("directory path is not absolute")
	}
	linkInfo, err := os.Lstat(path)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("directory is unavailable")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("directory is unavailable")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}
func pathsOverlap(left, right string) bool {
	return left == "" || right == "" || pathContains(left, right) || pathContains(right, left)
}
func pathContains(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))))
}
func cloneUsage(value *runtimecontract.UsageEvidence) *runtimecontract.UsageEvidence {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func nullableUsage(value *runtimecontract.UsageEvidence) *runtimecontract.UsageEvidence {
	if value == nil || (value.InputTokens == nil && value.CachedInputTokens == nil && value.OutputTokens == nil && value.ReasoningOutputTokens == nil && value.ToolCalls == 0) {
		return nil
	}
	return cloneUsage(value)
}
func int64ptr(value int64) *int64 { copy := value; return &copy }
func exceedsTokenBudget(limit int64, values ...int64) bool {
	remaining := limit
	for _, value := range values {
		if value > remaining {
			return true
		}
		remaining -= value
	}
	return false
}
func contractRef(value executioncontract.Contract) executioncontract.ContractReference {
	return executioncontract.ContractReference{ContractID: value.ContractID, Version: value.Version, Digest: value.Digest}
}
func validAction(value runtimecontract.ActionRequest) bool {
	return namespaced(value.SessionID, "runtime-session:") && validDigest(value.IdempotencyKeyDigest)
}
func binding(value executioncontract.BindingReference, prefix string) bool {
	return namespaced(value.ID, prefix) && value.Version > 0 && validDigest(value.Digest)
}
func namespaced(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && len(value) <= 128
}
func validReference(value, prefix string) bool {
	if !namespaced(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '.' && character != '_' && character != ':' && character != '-' {
			return false
		}
	}
	return true
}
func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
func digest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func hasCapability(values []executioncontract.Capability, value executioncontract.Capability) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func sortedCapabilities(values []executioncontract.Capability) []executioncontract.Capability {
	result := append([]executioncontract.Capability(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	unique := result[:0]
	for _, value := range result {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}
func contextErr(ctx context.Context) error {
	if ctx == nil {
		return normalized(runtimecontract.CodeInvalidRequest, false, "context is required")
	}
	if ctx.Err() != nil {
		return normalized(runtimecontract.CodeCanceled, false, "runtime action was canceled")
	}
	return nil
}
func normalized(code runtimecontract.ErrorCode, retryable bool, message string) error {
	return &runtimecontract.NormalizedError{Code: code, Retryable: retryable, Message: message}
}
func (adapter *Adapter) Configured() bool { return adapter.config.Enabled }

var _ runtimecontract.Adapter = (*Adapter)(nil)
var _ Executor = OSExecutor{}

func (invocation Invocation) String() string {
	return fmt.Sprintf("codex invocation with %d arguments", len(invocation.Arguments))
}
