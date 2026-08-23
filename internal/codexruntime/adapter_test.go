package codexruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"syncgate/internal/contextcompiler"
	"syncgate/internal/executioncontract"
	"syncgate/internal/project"
	"syncgate/internal/runtimecontract"
)

func TestAdapterSuccessUsesBoundedCodexInvocationAndObservedUsage(t *testing.T) {
	fixture := newAdapterFixture(t, true)
	fixture.executor.events = [][]byte{
		[]byte(`{"type":"item.started","item":{"type":"command_execution"}}`),
		[]byte(`{"type":"turn.completed","usage":{"input_tokens":40,"cached_input_tokens":10,"output_tokens":20,"reasoning_output_tokens":5}}`),
		[]byte(`{"type":"turn.completed","usage":{"input_tokens":40,"cached_input_tokens":10,"output_tokens":20,"reasoning_output_tokens":5}}`),
	}
	session, err := fixture.adapter.Prepare(context.Background(), fixture.prepare)
	if err != nil {
		t.Fatal(err)
	}
	fixture.executor.execution = Execution{FinalMessage: boundFinal(session, "result:one", "succeeded")}
	started, err := fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("6")})
	if err != nil || started.Status != runtimecontract.StatusRunning {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	if replay, err := fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("6")}); err != nil || replay != started {
		t.Fatalf("start replay=%+v err=%v", replay, err)
	}
	completed := waitForStatus(t, fixture.adapter, session.SessionID, runtimecontract.StatusSucceeded)
	if completed.Usage == nil || completed.Usage.Source != "provider_reported" || completed.Usage.Provider != fixture.prepare.Contract.Provider || completed.Usage.Model != fixture.prepare.Contract.Model || completed.Usage.ToolCalls != 1 || completed.Usage.InputTokens == nil || *completed.Usage.InputTokens != 40 {
		t.Fatalf("usage=%+v", completed.Usage)
	}
	result, err := fixture.adapter.CollectResult(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("7")})
	if err != nil || result.ResultID != "result:one" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if replay, err := fixture.adapter.CollectResult(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("7")}); err != nil || replay != result {
		t.Fatalf("result replay=%+v err=%v", replay, err)
	}
	invocation := fixture.executor.lastInvocation()
	joined := strings.Join(invocation.Arguments, " ")
	for _, required := range []string{"exec", "--ephemeral", "--ignore-user-config", "--json", "--ask-for-approval never", "--sandbox workspace-write", `shell_environment_policy.filters={CODEX_HOME="exclude"}`, "features.shell_tool=true", "features.rollout_budget.enabled=true", "features.rollout_budget.limit_tokens=1000", "--output-schema"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing argument %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"--yolo", "danger-full-access", "CODEX_API_KEY", "OPENAI_API_KEY", string(fixture.prepare.ContextBundle), string(fixture.prepare.InstructionBundle)} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("arguments leaked %q", forbidden)
		}
	}
	for _, value := range invocation.Environment {
		if strings.HasPrefix(value, "CODEX_API_KEY=") || strings.HasPrefix(value, "OPENAI_API_KEY=") {
			t.Fatalf("credential environment leaked: %s", value)
		}
	}
	if !strings.Contains(string(invocation.Stdin), "bounded instruction") || !strings.Contains(string(invocation.Stdin), "Project Context") {
		t.Fatal("bounded input was not supplied over stdin")
	}
	durable, err := os.ReadFile(fixture.adapter.statePath(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{string(fixture.prepare.ContextBundle), string(fixture.prepare.InstructionBundle), "bounded instruction", "Project Context", invocation.Directory, fixture.config.CodexHome, fixture.config.Executable, "OPENAI_API_KEY", "CODEX_API_KEY"} {
		if strings.Contains(string(durable), forbidden) {
			t.Fatalf("durable state leaked %q: %s", forbidden, durable)
		}
	}
	closeRequest := runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("8")}
	if err := fixture.adapter.Close(context.Background(), closeRequest); err != nil {
		t.Fatal(err)
	}
	if err := fixture.adapter.Close(context.Background(), closeRequest); err != nil {
		t.Fatalf("close replay: %v", err)
	}
}

func TestAdapterFailsClosedForDisabledUnsupportedAndChangedInput(t *testing.T) {
	fixture := newAdapterFixture(t, false)
	if _, err := fixture.adapter.Negotiate(context.Background(), runtimecontract.NegotiationRequest{Contract: fixture.prepare.Contract}); !runtimecontract.IsCode(err, runtimecontract.CodeUnavailable) {
		t.Fatalf("disabled error=%v", err)
	}

	fixture = newAdapterFixture(t, true)
	narrow := fixture.prepare.Contract
	narrow.Permissions.WritePaths = []string{"src"}
	narrow = signContract(narrow)
	if _, err := fixture.adapter.Negotiate(context.Background(), runtimecontract.NegotiationRequest{Contract: narrow, RequiredCapabilities: narrow.Permissions.Capabilities}); !runtimecontract.IsCode(err, runtimecontract.CodeCapabilityUnavailable) {
		t.Fatalf("narrow scope error=%v", err)
	}
	changed := fixture.prepare
	changed.InstructionBundle = []byte("changed")
	if _, err := fixture.adapter.Prepare(context.Background(), changed); !runtimecontract.IsCode(err, runtimecontract.CodeInvalidRequest) {
		t.Fatalf("changed input error=%v", err)
	}

	overlap := fixture.config
	overlap.CodexHome = overlap.StateRoot
	if _, err := New(overlap); !runtimecontract.IsCode(err, runtimecontract.CodeInvalidRequest) {
		t.Fatalf("overlapping roots error=%v", err)
	}

	fixture = newAdapterFixture(t, true)
	fixture.adapter.config.Workspace = func(context.Context, WorkspaceBinding) (string, error) { return fixture.config.StateRoot, nil }
	session, err := fixture.adapter.Prepare(context.Background(), fixture.prepare)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("6")}); err != nil {
		t.Fatal(err)
	}
	observed := waitForStatus(t, fixture.adapter, session.SessionID, runtimecontract.StatusFailed)
	if observed.ErrorCode != runtimecontract.CodeUnavailable {
		t.Fatalf("overlapping workspace observation=%+v", observed)
	}
}

func TestAdapterClassifiesMalformedRefusalRateLimitDisconnectAndBudget(t *testing.T) {
	cases := []struct {
		name      string
		execution Execution
		outcome   string
		runErr    error
		events    [][]byte
		status    runtimecontract.Status
		code      runtimecontract.ErrorCode
	}{
		{"malformed", Execution{FinalMessage: []byte(`{"unexpected":true}`)}, "", nil, nil, runtimecontract.StatusFailed, runtimecontract.CodeMalformedOutput},
		{"forged-binding", Execution{FinalMessage: []byte(`{"result_id":"result:forged","envelope_digest":"` + strings.Repeat("9", 64) + `","claimed_outcome":"succeeded","contract_digest":"` + strings.Repeat("f", 64) + `","session_id":"runtime-session:forged","attempt_id":"attempt:one","lease_generation":1,"fencing_digest":"` + strings.Repeat("3", 64) + `"}`)}, "", nil, nil, runtimecontract.StatusFailed, runtimecontract.CodeMalformedOutput},
		{"refusal", Execution{}, "refused", nil, nil, runtimecontract.StatusFailed, runtimecontract.CodeRefused},
		{"worker-failed", Execution{}, "failed", nil, nil, runtimecontract.StatusFailed, runtimecontract.CodeExecutionFailed},
		{"rate-limit", Execution{}, "", nil, [][]byte{[]byte(`{"type":"error","message":"429 rate limit exceeded"}`)}, runtimecontract.StatusFailed, runtimecontract.CodeRateLimited},
		{"disconnect", Execution{}, "", errors.New("connection lost"), nil, runtimecontract.StatusFailed, runtimecontract.CodeDisconnected},
		{"tool-budget", Execution{}, "succeeded", nil, [][]byte{[]byte(`{"type":"item.started","item":{"type":"command_execution"}}`), []byte(`{"type":"item.started","item":{"type":"command_execution"}}`)}, runtimecontract.StatusFailed, runtimecontract.CodeBudgetExceeded},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAdapterFixture(t, true)
			fixture.prepare.Contract.Budget.MaxToolCalls = 1
			fixture.prepare.Contract = signContract(fixture.prepare.Contract)
			fixture.executor.execution, fixture.executor.err, fixture.executor.events = test.execution, test.runErr, test.events
			session, err := fixture.adapter.Prepare(context.Background(), fixture.prepare)
			if err != nil {
				t.Fatal(err)
			}
			if test.outcome != "" {
				fixture.executor.execution = Execution{FinalMessage: boundFinal(session, "result:"+test.name, test.outcome)}
			}
			if _, err := fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("6")}); err != nil {
				t.Fatal(err)
			}
			observed := waitForStatus(t, fixture.adapter, session.SessionID, test.status)
			if observed.ErrorCode != test.code {
				t.Fatalf("observation=%+v", observed)
			}
			wantRetryable := test.code == runtimecontract.CodeRateLimited || test.code == runtimecontract.CodeDisconnected
			if observed.Retryable != wantRetryable {
				t.Fatalf("retryability=%v want=%v observation=%+v", observed.Retryable, wantRetryable, observed)
			}
			if test.name == "disconnect" && observed.Usage != nil {
				t.Fatalf("missing provider usage must remain nullable: %+v", observed.Usage)
			}
		})
	}
}

func TestAdapterEnforcesConcurrencyBeforeLaunchingASecondSession(t *testing.T) {
	fixture := newAdapterFixture(t, true)
	fixture.executor.block = true
	first, err := fixture.adapter.Prepare(context.Background(), fixture.prepare)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := fixture.prepare
	secondRequest.AttemptID = "attempt:two"
	secondRequest.LeaseGeneration = 2
	secondRequest.FencingDigest = repeat("4")
	secondRequest.IdempotencyKeyDigest = repeat("5")
	secondRequest.ResumeKeyDigest = repeat("6")
	second, err := fixture.adapter.Prepare(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: first.SessionID, IdempotencyKeyDigest: repeat("7")}); err != nil {
		t.Fatal(err)
	}
	<-fixture.executor.started
	if _, err := fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: second.SessionID, IdempotencyKeyDigest: repeat("8")}); !runtimecontract.IsCode(err, runtimecontract.CodeUnavailable) {
		t.Fatalf("concurrency error=%v", err)
	}
	if _, err := fixture.adapter.Cancel(context.Background(), runtimecontract.ActionRequest{SessionID: first.SessionID, IdempotencyKeyDigest: repeat("9")}); err != nil {
		t.Fatal(err)
	}
	<-fixture.executor.finished
}

func TestAdapterCancellationTimeoutAndRestartUncertainty(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		fixture := newAdapterFixture(t, true)
		fixture.executor.block = true
		session, _ := fixture.adapter.Prepare(context.Background(), fixture.prepare)
		fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("6")})
		<-fixture.executor.started
		canceled, err := fixture.adapter.Cancel(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("7")})
		if err != nil || canceled.Status != runtimecontract.StatusCanceled {
			t.Fatalf("canceled=%+v err=%v", canceled, err)
		}
		<-fixture.executor.finished
		waitForStatus(t, fixture.adapter, session.SessionID, runtimecontract.StatusCanceled)
	})
	t.Run("timeout", func(t *testing.T) {
		fixture := newAdapterFixture(t, true)
		fixture.executor.block = true
		fixture.prepare.Contract.Budget.MaxWallClockSeconds = 1
		fixture.prepare.Contract.Deadline = time.Now().UTC().Add(40 * time.Millisecond)
		fixture.prepare.Contract = signContract(fixture.prepare.Contract)
		session, _ := fixture.adapter.Prepare(context.Background(), fixture.prepare)
		fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("6")})
		observed := waitForStatus(t, fixture.adapter, session.SessionID, runtimecontract.StatusFailed)
		if observed.ErrorCode != runtimecontract.CodeTimedOut {
			t.Fatalf("timeout=%+v", observed)
		}
	})
	t.Run("restart", func(t *testing.T) {
		fixture := newAdapterFixture(t, true)
		fixture.executor.block = true
		session, _ := fixture.adapter.Prepare(context.Background(), fixture.prepare)
		fixture.adapter.Start(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("6")})
		<-fixture.executor.started
		restarted, err := New(fixture.config)
		if err != nil {
			t.Fatal(err)
		}
		observed, err := restarted.Observe(context.Background(), session.SessionID)
		if err != nil || observed.Status != runtimecontract.StatusFailed || observed.ErrorCode != runtimecontract.CodeUncertainTermination {
			t.Fatalf("restart=%+v err=%v", observed, err)
		}
		replayed, err := restarted.Prepare(context.Background(), fixture.prepare)
		if err != nil || replayed.SessionID != session.SessionID || replayed.Status != runtimecontract.StatusFailed {
			t.Fatalf("restart prepare replay=%+v err=%v", replayed, err)
		}
		fixture.adapter.Cancel(context.Background(), runtimecontract.ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: repeat("7")})
		<-fixture.executor.finished
	})
}

type adapterFixture struct {
	adapter  *Adapter
	executor *scriptedExecutor
	config   Config
	prepare  runtimecontract.PrepareRequest
}

func newAdapterFixture(t *testing.T, enabled bool) adapterFixture {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	state := filepath.Join(root, "state")
	home := filepath.Join(root, "codex-home")
	for _, path := range []string{workspace, state, home} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runtimeRef := ref("runtime:codex-cli", "a")
	provider := ref("provider:openai", "b")
	model := ref("model:gpt-5.6-sol", "c")
	node := ref("node:local", "d")
	instruction := []byte("bounded instruction")
	contextBytes, contextDigest := testContextBundle(t)
	now := time.Now().UTC()
	contract := executioncontract.Contract{Schema: executioncontract.Schema, ContractID: "contract:codex", Version: 1, ProjectID: "project-one", TaskID: "task:one", TaskRevision: 1, GraphRevision: 1, WorkPackageID: "wp-one", ExecutionID: "execution:one", Runtime: runtimeRef, Provider: provider, Model: model, Node: node, Instruction: executioncontract.BindingReference{ID: "instruction:one", Version: 1, Digest: byteDigest(instruction)}, ContextDigest: contextDigest, Permissions: executioncontract.EffectivePermissions{Capabilities: []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityWrite, executioncontract.CapabilityShell, executioncontract.CapabilityTest, executioncontract.CapabilityBranch}, InspectPaths: []string{"."}, WritePaths: []string{"."}}, Budget: executioncontract.BudgetLimits{MaxTokens: 1000, MaxCostMicros: 1000, MaxWallClockSeconds: 60, MaxRetries: 0, MaxToolCalls: 20, MaxConcurrentWorkers: 1}, NotBefore: now, Deadline: now.Add(time.Minute)}
	contract = signContract(contract)
	executor := &scriptedExecutor{started: make(chan struct{}, 1), finished: make(chan struct{}, 1)}
	config := Config{Enabled: enabled, Executable: filepath.Join(root, "codex.exe"), CodexHome: home, StateRoot: state, Runtime: runtimeRef, Provider: provider, Model: model, Node: node, Capabilities: contract.Permissions.Capabilities, MaxConcurrent: 1, Workspace: func(_ context.Context, binding WorkspaceBinding) (string, error) {
		if binding.WorkspaceID != "workspace:one" || binding.AttemptID == "" || binding.LeaseGeneration < 1 || !validDigest(binding.FencingDigest) {
			return "", errors.New("invalid workspace lease binding")
		}
		return workspace, nil
	}, Now: func() time.Time { return time.Now().UTC() }, Executor: executor}
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return adapterFixture{adapter: adapter, executor: executor, config: config, prepare: runtimecontract.PrepareRequest{Contract: contract, AttemptID: "attempt:one", LeaseGeneration: 1, FencingDigest: repeat("3"), WorkspaceID: "workspace:one", ContextBundle: contextBytes, InstructionBundle: instruction, IdempotencyKeyDigest: repeat("1"), ResumeKeyDigest: repeat("2")}}
}

type scriptedExecutor struct {
	mu         sync.Mutex
	invocation Invocation
	execution  Execution
	err        error
	events     [][]byte
	block      bool
	started    chan struct{}
	finished   chan struct{}
}

func (executor *scriptedExecutor) Run(ctx context.Context, invocation Invocation, sink EventSink) (Execution, error) {
	defer func() {
		select {
		case executor.finished <- struct{}{}:
		default:
		}
	}()
	executor.mu.Lock()
	executor.invocation = invocation
	events := append([][]byte(nil), executor.events...)
	execution, runErr, block := executor.execution, executor.err, executor.block
	executor.mu.Unlock()
	select {
	case executor.started <- struct{}{}:
	default:
	}
	for _, event := range events {
		if err := sink(event); err != nil {
			return Execution{}, err
		}
	}
	if block {
		<-ctx.Done()
		return Execution{}, ctx.Err()
	}
	return execution, runErr
}
func (executor *scriptedExecutor) lastInvocation() Invocation {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.invocation
}

func waitForStatus(t *testing.T, adapter *Adapter, sessionID string, want runtimecontract.Status) runtimecontract.Observation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		observed, err := adapter.Observe(context.Background(), sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if observed.Status == want {
			return observed
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session did not reach %s", want)
	return runtimecontract.Observation{}
}
func boundFinal(session runtimecontract.Session, id, outcome string) []byte {
	raw, _ := json.Marshal(codexFinal{CollectedResult: runtimecontract.CollectedResult{ResultID: id, EnvelopeDigest: repeat("9"), ClaimedOutcome: outcome}, ContractDigest: session.Contract.Digest, SessionID: session.SessionID, AttemptID: session.AttemptID, LeaseGeneration: session.LeaseGeneration, FencingDigest: session.FencingDigest})
	return raw
}
func testContextBundle(t *testing.T) ([]byte, string) {
	t.Helper()
	bundle := contextcompiler.Bundle{Manifest: contextcompiler.Manifest{CompilerVersion: contextcompiler.CompilerVersion, ProjectID: "project-one", WorkPackageID: "wp-one", TradeReference: project.RegistryReference{ID: "trade:go", Version: 1, Digest: repeat("8")}}, Briefing: "# Project Context\n"}
	unsigned, _ := json.Marshal(bundle)
	bundle.Manifest.ContextDigest = byteDigest(unsigned)
	raw, _ := json.Marshal(bundle)
	return raw, bundle.Manifest.ContextDigest
}
func signContract(contract executioncontract.Contract) executioncontract.Contract {
	contract.Digest = ""
	raw, _ := json.Marshal(contract)
	contract.Digest = byteDigest(raw)
	return contract
}
func ref(id, seed string) executioncontract.BindingReference {
	return executioncontract.BindingReference{ID: id, Version: 1, Digest: repeat(seed)}
}
func repeat(seed string) string    { return strings.Repeat(seed, 64) }
func byteDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
