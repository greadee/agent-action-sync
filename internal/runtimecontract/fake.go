package runtimecontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"syncgate/internal/executioncontract"
)

type FakeConfig struct {
	Runtime      executioncontract.BindingReference
	Node         executioncontract.BindingReference
	Capabilities []executioncontract.Capability
	Now          func() time.Time
}

type DeterministicFake struct {
	mu           sync.Mutex
	config       FakeConfig
	sessions     map[string]*fakeSession
	preparations map[string]fakePreparation
	actions      map[string]Observation
}

type fakeSession struct {
	session Session
	result  CollectedResult
}

type fakePreparation struct {
	fingerprint string
	sessionID   string
}

func NewDeterministicFake(config FakeConfig) (*DeterministicFake, error) {
	config.Capabilities = sortedCapabilities(config.Capabilities)
	if !validBinding(config.Runtime, "runtime:") || !validBinding(config.Node, "node:") || config.Now == nil {
		return nil, normalized(CodeInvalidRequest, false, "runtime fake configuration is incomplete")
	}
	return &DeterministicFake{
		config: config, sessions: map[string]*fakeSession{}, preparations: map[string]fakePreparation{}, actions: map[string]Observation{},
	}, nil
}

func (fake *DeterministicFake) Negotiate(ctx context.Context, request NegotiationRequest) (NegotiationResult, error) {
	if err := contextError(ctx); err != nil {
		return NegotiationResult{}, err
	}
	if err := fake.validateContract(request.Contract); err != nil {
		return NegotiationResult{}, err
	}
	required := sortedCapabilities(request.RequiredCapabilities)
	for _, capability := range required {
		if !containsCapability(request.Contract.Permissions.Capabilities, capability) || !containsCapability(fake.config.Capabilities, capability) {
			return NegotiationResult{}, normalized(CodeCapabilityUnavailable, false, "required runtime capability is unavailable")
		}
	}
	payload, _ := json.Marshal(struct {
		Runtime      executioncontract.BindingReference `json:"runtime"`
		Node         executioncontract.BindingReference `json:"node"`
		Capabilities []executioncontract.Capability     `json:"capabilities"`
	}{fake.config.Runtime, fake.config.Node, required})
	return NegotiationResult{Capabilities: required, BindingDigest: hash(payload)}, nil
}

func (fake *DeterministicFake) Prepare(ctx context.Context, request PrepareRequest) (Session, error) {
	if err := contextError(ctx); err != nil {
		return Session{}, err
	}
	if !namespaced(request.WorkspaceID, "workspace:") || !validDigest(request.IdempotencyKeyDigest) || !validDigest(request.ResumeKeyDigest) {
		return Session{}, normalized(CodeInvalidRequest, false, "prepare request is incomplete")
	}
	if _, err := fake.Negotiate(ctx, NegotiationRequest{Contract: request.Contract, RequiredCapabilities: request.Contract.Permissions.Capabilities}); err != nil {
		return Session{}, err
	}
	fingerprintBytes, _ := json.Marshal(struct {
		Contract    executioncontract.ContractReference `json:"contract"`
		WorkspaceID string                              `json:"workspace_id"`
		ResumeKey   string                              `json:"resume_key_digest"`
	}{contractReference(request.Contract), request.WorkspaceID, request.ResumeKeyDigest})
	fingerprint := hash(fingerprintBytes)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if existing, ok := fake.preparations[request.IdempotencyKeyDigest]; ok {
		if existing.fingerprint != fingerprint {
			return Session{}, normalized(CodeConflict, false, "prepare idempotency key was reused for different authority")
		}
		return fake.sessions[existing.sessionID].session, nil
	}
	sessionID := "runtime-session:" + hash([]byte(request.Contract.Digest + request.IdempotencyKeyDigest))[:32]
	when := fake.now()
	session := Session{
		SessionID: sessionID, Contract: contractReference(request.Contract), WorkspaceID: request.WorkspaceID,
		ResumeKeyDigest: request.ResumeKeyDigest, Status: StatusPrepared, Sequence: 1, UpdatedAt: when,
	}
	fake.sessions[sessionID] = &fakeSession{session: session}
	fake.preparations[request.IdempotencyKeyDigest] = fakePreparation{fingerprint: fingerprint, sessionID: sessionID}
	return session, nil
}

func (fake *DeterministicFake) Start(ctx context.Context, request ActionRequest) (Observation, error) {
	return fake.transition(ctx, "start", request, []Status{StatusPrepared}, StatusRunning)
}

func (fake *DeterministicFake) Observe(ctx context.Context, sessionID string) (Observation, error) {
	if err := contextError(ctx); err != nil {
		return Observation{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	session, err := fake.find(sessionID)
	if err != nil {
		return Observation{}, err
	}
	return observe(session.session), nil
}

func (fake *DeterministicFake) Pause(ctx context.Context, request ActionRequest) (Observation, error) {
	return fake.transition(ctx, "pause", request, []Status{StatusRunning}, StatusPaused)
}

func (fake *DeterministicFake) Resume(ctx context.Context, request ActionRequest) (Observation, error) {
	return fake.transition(ctx, "resume", request, []Status{StatusPaused}, StatusRunning)
}

func (fake *DeterministicFake) Cancel(ctx context.Context, request ActionRequest) (Observation, error) {
	return fake.transition(ctx, "cancel", request, []Status{StatusPrepared, StatusRunning, StatusPaused}, StatusCanceled)
}

func (fake *DeterministicFake) CollectResult(ctx context.Context, request ActionRequest) (CollectedResult, error) {
	if err := contextError(ctx); err != nil {
		return CollectedResult{}, err
	}
	if !validActionRequest(request) {
		return CollectedResult{}, normalized(CodeInvalidRequest, false, "collect request is incomplete")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	session, err := fake.find(request.SessionID)
	if err != nil {
		return CollectedResult{}, err
	}
	if session.session.Status != StatusSucceeded && session.session.Status != StatusFailed && session.session.Status != StatusCanceled {
		return CollectedResult{}, normalized(CodeInvalidTransition, false, "result is unavailable before a terminal status")
	}
	if !namespaced(session.result.ResultID, "result:") || !validDigest(session.result.EnvelopeDigest) {
		return CollectedResult{}, normalized(CodeUnavailable, true, "runtime has not staged a result envelope")
	}
	return session.result, nil
}

func (fake *DeterministicFake) Close(ctx context.Context, request ActionRequest) error {
	_, err := fake.transition(ctx, "close", request, []Status{StatusPrepared, StatusRunning, StatusPaused, StatusCanceled, StatusSucceeded, StatusFailed}, StatusClosed)
	return err
}

// Complete is a deterministic-fake control seam for scheduler tests. It is not
// part of Adapter and cannot publish or intake the claimed result.
func (fake *DeterministicFake) Complete(sessionID string, result CollectedResult, succeeded bool) (Observation, error) {
	if !namespaced(result.ResultID, "result:") || !validDigest(result.EnvelopeDigest) {
		return Observation{}, normalized(CodeInvalidRequest, false, "fake completion result is incomplete")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	session, err := fake.find(sessionID)
	if err != nil {
		return Observation{}, err
	}
	if session.session.Status != StatusRunning {
		return Observation{}, normalized(CodeInvalidTransition, false, "only a running session can complete")
	}
	if succeeded {
		session.session.Status = StatusSucceeded
	} else {
		session.session.Status = StatusFailed
	}
	session.result = result
	session.session.Sequence++
	session.session.UpdatedAt = fake.now()
	return observe(session.session), nil
}

func (fake *DeterministicFake) transition(ctx context.Context, action string, request ActionRequest, from []Status, to Status) (Observation, error) {
	if err := contextError(ctx); err != nil {
		return Observation{}, err
	}
	if !validActionRequest(request) {
		return Observation{}, normalized(CodeInvalidRequest, false, "runtime action request is incomplete")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	key := action + ":" + request.IdempotencyKeyDigest
	if prior, ok := fake.actions[key]; ok {
		if prior.SessionID != request.SessionID {
			return Observation{}, normalized(CodeConflict, false, "action idempotency key was reused for another session")
		}
		return prior, nil
	}
	session, err := fake.find(request.SessionID)
	if err != nil {
		return Observation{}, err
	}
	allowed := false
	for _, candidate := range from {
		allowed = allowed || session.session.Status == candidate
	}
	if !allowed {
		return Observation{}, normalized(CodeInvalidTransition, false, "runtime action is invalid for the current status")
	}
	session.session.Status = to
	session.session.Sequence++
	session.session.UpdatedAt = fake.now()
	observation := observe(session.session)
	fake.actions[key] = observation
	return observation, nil
}

func (fake *DeterministicFake) validateContract(contract executioncontract.Contract) error {
	if err := executioncontract.VerifyDigest(contract); err != nil || contract.Runtime != fake.config.Runtime || contract.Node != fake.config.Node {
		return normalized(CodeInvalidRequest, false, "execution contract runtime or node binding is invalid")
	}
	return nil
}

func (fake *DeterministicFake) find(sessionID string) (*fakeSession, error) {
	if !namespaced(sessionID, "runtime-session:") {
		return nil, normalized(CodeInvalidRequest, false, "runtime session ID is invalid")
	}
	session, ok := fake.sessions[sessionID]
	if !ok {
		return nil, normalized(CodeNotFound, false, "runtime session was not found")
	}
	return session, nil
}

func (fake *DeterministicFake) now() time.Time { return fake.config.Now().UTC() }

func observe(session Session) Observation {
	return Observation{SessionID: session.SessionID, Status: session.Status, Sequence: session.Sequence, UpdatedAt: session.UpdatedAt}
}

func contractReference(contract executioncontract.Contract) executioncontract.ContractReference {
	return executioncontract.ContractReference{ContractID: contract.ContractID, Version: contract.Version, Digest: contract.Digest}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return normalized(CodeInvalidRequest, false, "context is required")
	}
	if err := ctx.Err(); err != nil {
		return normalized(CodeCanceled, false, "runtime action was canceled")
	}
	return nil
}

func validActionRequest(request ActionRequest) bool {
	return namespaced(request.SessionID, "runtime-session:") && validDigest(request.IdempotencyKeyDigest)
}

func validBinding(value executioncontract.BindingReference, prefix string) bool {
	return namespaced(value.ID, prefix) && value.Version > 0 && validDigest(value.Digest)
}

func namespaced(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && len(value) <= 128
}

func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
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

func containsCapability(values []executioncontract.Capability, value executioncontract.Capability) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func normalized(code ErrorCode, retryable bool, message string) error {
	return &NormalizedError{Code: code, Retryable: retryable, Message: message}
}
