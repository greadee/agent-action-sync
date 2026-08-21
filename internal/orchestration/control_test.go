package orchestration

import (
	"errors"
	"testing"

	"syncgate/internal/storage"
)

func TestReduceAssignmentStateAcceptsOnlyDeclaredTransitions(t *testing.T) {
	allowed := []struct {
		from storage.AssignmentState
		to   storage.AssignmentState
	}{
		{storage.AssignmentPlanned, storage.AssignmentLeased},
		{storage.AssignmentLeased, storage.AssignmentPreparing},
		{storage.AssignmentPreparing, storage.AssignmentRunning},
		{storage.AssignmentRunning, storage.AssignmentPaused},
		{storage.AssignmentPaused, storage.AssignmentRunning},
		{storage.AssignmentRunning, storage.AssignmentCollecting},
		{storage.AssignmentCollecting, storage.AssignmentAwaitingGates},
		{storage.AssignmentAwaitingGates, storage.AssignmentAccepted},
		{storage.AssignmentRunning, storage.AssignmentFailed},
		{storage.AssignmentRunning, storage.AssignmentCanceled},
		{storage.AssignmentRunning, storage.AssignmentExpired},
	}
	for _, transition := range allowed {
		if err := ReduceAssignmentState(transition.from, transition.to); err != nil {
			t.Errorf("%s -> %s: %v", transition.from, transition.to, err)
		}
	}

	denied := []struct {
		from storage.AssignmentState
		to   storage.AssignmentState
	}{
		{storage.AssignmentPlanned, storage.AssignmentRunning},
		{storage.AssignmentRunning, storage.AssignmentAccepted},
		{storage.AssignmentAccepted, storage.AssignmentRunning},
		{storage.AssignmentFailed, storage.AssignmentPlanned},
		{storage.AssignmentCanceled, storage.AssignmentRunning},
		{storage.AssignmentExpired, storage.AssignmentLeased},
		{storage.AssignmentRunning, storage.AssignmentRunning},
	}
	for _, transition := range denied {
		if err := ReduceAssignmentState(transition.from, transition.to); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s -> %s error = %v", transition.from, transition.to, err)
		}
	}
}

func TestReduceGateStateIsOneWay(t *testing.T) {
	for _, target := range []storage.GateState{storage.GateSatisfied, storage.GateFailed, storage.GateWaived} {
		if err := ReduceGateState(storage.GatePending, target); err != nil {
			t.Errorf("pending -> %s: %v", target, err)
		}
		if err := ReduceGateState(target, target); err != nil {
			t.Errorf("%s replay: %v", target, err)
		}
	}
	if err := ReduceGateState(storage.GateSatisfied, storage.GateFailed); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal gate rewrite error = %v", err)
	}
}

func TestRecoveryForStateNeverAutomaticallyResumesAmbiguousRuntimeWork(t *testing.T) {
	cases := []struct {
		state        storage.AssignmentState
		hasResources bool
		want         storage.RecoveryDisposition
	}{
		{storage.AssignmentPlanned, false, storage.RecoveryResume},
		{storage.AssignmentLeased, false, storage.RecoveryReconcile},
		{storage.AssignmentLeased, true, storage.RecoveryNeedsOperator},
		{storage.AssignmentPreparing, true, storage.RecoveryNeedsOperator},
		{storage.AssignmentRunning, true, storage.RecoveryNeedsOperator},
		{storage.AssignmentPaused, true, storage.RecoveryNeedsOperator},
		{storage.AssignmentCollecting, true, storage.RecoveryNeedsOperator},
		{storage.AssignmentAwaitingGates, true, storage.RecoveryResume},
		{storage.AssignmentAccepted, true, storage.RecoveryNone},
	}
	for _, test := range cases {
		if got := RecoveryForState(test.state, test.hasResources); got != test.want {
			t.Errorf("RecoveryForState(%s, %t) = %s, want %s", test.state, test.hasResources, got, test.want)
		}
	}
}

func TestTransitionAuditActionsDistinguishStartAndResume(t *testing.T) {
	if got := transitionAction(storage.AssignmentPreparing, storage.AssignmentRunning); got != "start" {
		t.Fatalf("preparing -> running action = %q", got)
	}
	if got := transitionAction(storage.AssignmentPaused, storage.AssignmentRunning); got != "resume" {
		t.Fatalf("paused -> running action = %q", got)
	}
	if got := transitionAction(storage.AssignmentRunning, storage.AssignmentPaused); got != "pause" {
		t.Fatalf("running -> paused action = %q", got)
	}
}
