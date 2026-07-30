package sync

import (
	"errors"
	"testing"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestDecideOneWayChangeAppliesAuthorizedActionsWithoutDrift(t *testing.T) {
	tests := []struct {
		name       string
		action     OneWayAction
		capability core.Capability
	}{
		{name: "add", action: OneWayActionAdd, capability: core.CapabilityUpload},
		{name: "modify", action: OneWayActionModify, capability: core.CapabilityModify},
		{name: "delete", action: OneWayActionDelete, capability: core.CapabilityDelete},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validOneWayDecisionRequest(test.action, TargetDriftReject)
			request.Permission.Capabilities[test.capability] = true

			decision, err := DecideOneWayChange(request)
			if err != nil {
				t.Fatalf("DecideOneWayChange: %v", err)
			}
			if decision.Outcome != OneWayDecisionApply || !decision.Apply || decision.TargetDrift || decision.PreserveConflictCopy {
				t.Fatalf("decision = %+v", decision)
			}
			if decision.RequiredCapability != test.capability {
				t.Fatalf("required capability = %q, want %q", decision.RequiredCapability, test.capability)
			}
		})
	}
}

func TestDecideOneWayChangeHandlesEveryTargetDriftPolicy(t *testing.T) {
	tests := []struct {
		name                 string
		policy               TargetDriftPolicy
		outcome              OneWayDecisionOutcome
		apply                bool
		preserveConflictCopy bool
	}{
		{name: "reject", policy: TargetDriftReject, outcome: OneWayDecisionRejectDrift},
		{name: "preserve conflict copy", policy: TargetDriftPreserveConflictCopy, outcome: OneWayDecisionPreserveConflictCopy, apply: true, preserveConflictCopy: true},
		{name: "report only", policy: TargetDriftReportOnly, outcome: OneWayDecisionReportOnly},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validOneWayDecisionRequest(OneWayActionModify, test.policy)
			request.Permission.Capabilities[core.CapabilityModify] = true
			request.TargetRevisionID = "target-drifted"

			decision, err := DecideOneWayChange(request)
			if err != nil {
				t.Fatalf("DecideOneWayChange: %v", err)
			}
			if decision.Outcome != test.outcome || decision.Apply != test.apply || decision.PreserveConflictCopy != test.preserveConflictCopy || !decision.TargetDrift {
				t.Fatalf("decision = %+v", decision)
			}
		})
	}
}

func TestDecideOneWayChangeRejectsUnsupportedPolicyConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		change func(*OneWayDecisionRequest)
		want   error
	}{
		{
			name: "source mode",
			change: func(request *OneWayDecisionRequest) {
				request.Source.Mode = storage.ShareOneWayTarget
			},
			want: ErrUnsupportedSourceMode,
		},
		{
			name: "target mode",
			change: func(request *OneWayDecisionRequest) {
				request.Target.Mode = storage.ShareTwoWayPlanned
			},
			want: ErrUnsupportedTargetMode,
		},
		{
			name: "target drift policy",
			change: func(request *OneWayDecisionRequest) {
				request.Target.DriftPolicy = "merge"
			},
			want: ErrUnsupportedTargetDriftPolicy,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validOneWayDecisionRequest(OneWayActionModify, TargetDriftReject)
			request.Permission.Capabilities[core.CapabilityModify] = true
			test.change(&request)

			decision, err := DecideOneWayChange(request)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if decision.Apply {
				t.Fatalf("rejected decision permits apply: %+v", decision)
			}
		})
	}
}

func TestDecideOneWayChangeRejectsInvalidPolicyScope(t *testing.T) {
	tests := []struct {
		name   string
		change func(*OneWayDecisionRequest)
	}{
		{
			name: "missing source share",
			change: func(request *OneWayDecisionRequest) {
				request.Source.ShareID = ""
			},
		},
		{
			name: "missing source device",
			change: func(request *OneWayDecisionRequest) {
				request.Source.DeviceID = ""
			},
		},
		{
			name: "missing target share",
			change: func(request *OneWayDecisionRequest) {
				request.Target.ShareID = ""
			},
		},
		{
			name: "mismatched shares",
			change: func(request *OneWayDecisionRequest) {
				request.Target.ShareID = "share-2"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validOneWayDecisionRequest(OneWayActionDelete, TargetDriftReject)
			request.Permission.Capabilities[core.CapabilityDelete] = true
			test.change(&request)

			decision, err := DecideOneWayChange(request)
			if !errors.Is(err, ErrInvalidOneWayPolicy) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidOneWayPolicy)
			}
			if decision.Apply {
				t.Fatalf("rejected decision permits apply: %+v", decision)
			}
		})
	}
}

func TestDecideOneWayChangeRejectsUnsupportedAction(t *testing.T) {
	request := validOneWayDecisionRequest("rename", TargetDriftReject)

	decision, err := DecideOneWayChange(request)
	if !errors.Is(err, ErrUnsupportedOneWayAction) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedOneWayAction)
	}
	if decision.Apply {
		t.Fatalf("rejected decision permits apply: %+v", decision)
	}
}

func TestDecideOneWayChangeRejectsUnauthorizedActions(t *testing.T) {
	tests := []struct {
		name   string
		action OneWayAction
		change func(*OneWayDecisionRequest)
	}{
		{
			name:   "missing sync capability",
			action: OneWayActionModify,
			change: func(request *OneWayDecisionRequest) {
				request.Permission.Capabilities[core.CapabilityModify] = true
				delete(request.Permission.Capabilities, core.CapabilitySync)
			},
		},
		{
			name:   "missing upload capability",
			action: OneWayActionAdd,
			change: func(*OneWayDecisionRequest) {},
		},
		{
			name:   "missing modify capability",
			action: OneWayActionModify,
			change: func(*OneWayDecisionRequest) {},
		},
		{
			name:   "missing delete capability",
			action: OneWayActionDelete,
			change: func(*OneWayDecisionRequest) {},
		},
		{
			name:   "wrong permission share",
			action: OneWayActionModify,
			change: func(request *OneWayDecisionRequest) {
				request.Permission.ShareID = "share-2"
				request.Permission.Capabilities[core.CapabilityModify] = true
			},
		},
		{
			name:   "wrong permission device",
			action: OneWayActionDelete,
			change: func(request *OneWayDecisionRequest) {
				request.Permission.DeviceID = "DEVICE-2"
				request.Permission.Capabilities[core.CapabilityDelete] = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validOneWayDecisionRequest(test.action, TargetDriftPreserveConflictCopy)
			request.TargetRevisionID = "target-drifted"
			test.change(&request)

			decision, err := DecideOneWayChange(request)
			if !errors.Is(err, ErrUnauthorizedOneWayAction) {
				t.Fatalf("error = %v, want %v", err, ErrUnauthorizedOneWayAction)
			}
			if decision.Apply || decision.PreserveConflictCopy {
				t.Fatalf("unauthorized decision permits filesystem work: %+v", decision)
			}
		})
	}
}

func validOneWayDecisionRequest(action OneWayAction, driftPolicy TargetDriftPolicy) OneWayDecisionRequest {
	return OneWayDecisionRequest{
		Source: OneWaySourcePolicy{
			ShareID:  "share-1",
			DeviceID: "DEVICE-1",
			Mode:     storage.ShareOneWaySource,
		},
		Target: OneWayTargetPolicy{
			ShareID:     "share-1",
			Mode:        storage.ShareOneWayTarget,
			DriftPolicy: driftPolicy,
		},
		Permission: core.SharePermission{
			ShareID:  "share-1",
			DeviceID: "DEVICE-1",
			Capabilities: map[core.Capability]bool{
				core.CapabilitySync: true,
			},
		},
		Action:                 action,
		ExpectedBaseRevisionID: "revision-base",
		TargetRevisionID:       "revision-base",
	}
}
