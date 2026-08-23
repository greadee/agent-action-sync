package runtimecontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"syncgate/internal/executioncontract"
)

func TestDeterministicFakeLifecycleIdempotencyAndResultCollection(t *testing.T) {
	when := time.Date(2026, time.August, 17, 19, 0, 0, 0, time.UTC)
	runtimeRef := runtimeBinding("runtime:fake", "a")
	nodeRef := runtimeBinding("node:fake", "b")
	contract := runtimeContract(runtimeRef, nodeRef, []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityTest})
	fake, err := NewDeterministicFake(FakeConfig{Runtime: runtimeRef, Node: nodeRef, Capabilities: contract.Permissions.Capabilities, Now: func() time.Time { return when }})
	if err != nil {
		t.Fatal(err)
	}
	prepare := PrepareRequest{Contract: contract, AttemptID: "attempt:one", LeaseGeneration: 1, FencingDigest: runtimeDigest("9"), WorkspaceID: "workspace:one", IdempotencyKeyDigest: runtimeDigest("1"), ResumeKeyDigest: runtimeDigest("2")}
	session, err := fake.Prepare(context.Background(), prepare)
	if err != nil || session.Status != StatusPrepared || strings.Contains(session.SessionID, "provider") {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	replayed, err := fake.Prepare(context.Background(), prepare)
	if err != nil || replayed != session {
		t.Fatalf("prepare replay=%+v err=%v", replayed, err)
	}
	changed := prepare
	changed.WorkspaceID = "workspace:other"
	if _, err := fake.Prepare(context.Background(), changed); !IsCode(err, CodeConflict) {
		t.Fatalf("prepare conflict=%v", err)
	}

	started, err := fake.Start(context.Background(), ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: runtimeDigest("3")})
	if err != nil || started.Status != StatusRunning {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	paused, err := fake.Pause(context.Background(), ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: runtimeDigest("4")})
	if err != nil || paused.Status != StatusPaused {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	startReplay, err := fake.Start(context.Background(), ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: runtimeDigest("3")})
	if err != nil || startReplay != started {
		t.Fatalf("start replay=%+v err=%v", startReplay, err)
	}
	if _, err := fake.Resume(context.Background(), ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: runtimeDigest("5")}); err != nil {
		t.Fatal(err)
	}
	result := CollectedResult{ResultID: "result:one", EnvelopeDigest: runtimeDigest("6"), ClaimedOutcome: "succeeded"}
	completed, err := fake.Complete(session.SessionID, result, true)
	if err != nil || completed.Status != StatusSucceeded {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}
	collected, err := fake.CollectResult(context.Background(), ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: runtimeDigest("7")})
	if err != nil || collected != result {
		t.Fatalf("collect=%+v err=%v", collected, err)
	}
	if err := fake.Close(context.Background(), ActionRequest{SessionID: session.SessionID, IdempotencyKeyDigest: runtimeDigest("8")}); err != nil {
		t.Fatal(err)
	}
	if observed, err := fake.Observe(context.Background(), session.SessionID); err != nil || observed.Status != StatusClosed {
		t.Fatalf("closed observation=%+v err=%v", observed, err)
	}
}

func TestDeterministicFakeFailsClosedOnCapabilityAndBindingDrift(t *testing.T) {
	when := time.Date(2026, time.August, 17, 19, 0, 0, 0, time.UTC)
	runtimeRef := runtimeBinding("runtime:fake", "a")
	nodeRef := runtimeBinding("node:fake", "b")
	contract := runtimeContract(runtimeRef, nodeRef, []executioncontract.Capability{executioncontract.CapabilityInspect, executioncontract.CapabilityShell})
	fake, _ := NewDeterministicFake(FakeConfig{Runtime: runtimeRef, Node: nodeRef, Capabilities: []executioncontract.Capability{executioncontract.CapabilityInspect}, Now: func() time.Time { return when }})
	if _, err := fake.Prepare(context.Background(), PrepareRequest{Contract: contract, AttemptID: "attempt:one", LeaseGeneration: 1, FencingDigest: runtimeDigest("9"), WorkspaceID: "workspace:one", IdempotencyKeyDigest: runtimeDigest("1"), ResumeKeyDigest: runtimeDigest("2")}); !IsCode(err, CodeCapabilityUnavailable) {
		t.Fatalf("capability drift error=%v", err)
	}
	forged := contract
	forged.Runtime = runtimeBinding("runtime:other", "c")
	if _, err := fake.Negotiate(context.Background(), NegotiationRequest{Contract: forged}); !IsCode(err, CodeInvalidRequest) {
		t.Fatalf("forged binding error=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fake.Observe(canceled, "runtime-session:missing"); !IsCode(err, CodeCanceled) {
		t.Fatalf("canceled error=%v", err)
	}
}

func runtimeContract(runtimeRef, nodeRef executioncontract.BindingReference, capabilities []executioncontract.Capability) executioncontract.Contract {
	contract := executioncontract.Contract{
		Schema: executioncontract.Schema, ContractID: "contract:runtime", Version: 1, ProjectID: "project-one",
		TaskID: "task:one", TaskRevision: 1, GraphRevision: 1, WorkPackageID: "work-one", ExecutionID: "execution:one",
		Runtime: runtimeRef, Node: nodeRef, Permissions: executioncontract.EffectivePermissions{Capabilities: capabilities},
	}
	unsigned, _ := json.Marshal(contract)
	contract.Digest = runtimeHash(unsigned)
	return contract
}

func runtimeBinding(id, seed string) executioncontract.BindingReference {
	return executioncontract.BindingReference{ID: id, Version: 1, Digest: runtimeDigest(seed)}
}

func runtimeDigest(seed string) string { return strings.Repeat(seed, 64) }

func runtimeHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
