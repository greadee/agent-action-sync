package sync

import (
	"errors"
	"fmt"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

var (
	ErrInvalidOneWayPolicy          = errors.New("invalid one-way policy")
	ErrUnsupportedSourceMode        = errors.New("unsupported one-way source mode")
	ErrUnsupportedTargetMode        = errors.New("unsupported one-way target mode")
	ErrUnsupportedTargetDriftPolicy = errors.New("unsupported target drift policy")
	ErrUnsupportedOneWayAction      = errors.New("unsupported one-way action")
	ErrUnauthorizedOneWayAction     = errors.New("unauthorized one-way action")
)

type TargetDriftPolicy string

const (
	TargetDriftReject               TargetDriftPolicy = "reject"
	TargetDriftPreserveConflictCopy TargetDriftPolicy = "preserve_conflict_copy"
	TargetDriftReportOnly           TargetDriftPolicy = "report_only"
)

type OneWayAction string

const (
	OneWayActionAdd    OneWayAction = "add"
	OneWayActionModify OneWayAction = "modify"
	OneWayActionDelete OneWayAction = "delete"
)

type OneWayDecisionOutcome string

const (
	OneWayDecisionApply                OneWayDecisionOutcome = "apply"
	OneWayDecisionRejectDrift          OneWayDecisionOutcome = "reject_drift"
	OneWayDecisionPreserveConflictCopy OneWayDecisionOutcome = "preserve_conflict_copy"
	OneWayDecisionReportOnly           OneWayDecisionOutcome = "report_only"
)

// OneWaySourcePolicy identifies the only device and share allowed to provide
// authoritative changes for a one-way decision.
type OneWaySourcePolicy struct {
	ShareID  core.ShareID
	DeviceID core.DeviceID
	Mode     storage.ShareMode
}

// OneWayTargetPolicy defines the receiver-side mode and its explicit response
// when the target revision has drifted from the source's expected base.
type OneWayTargetPolicy struct {
	ShareID     core.ShareID
	Mode        storage.ShareMode
	DriftPolicy TargetDriftPolicy
}

type OneWayDecisionRequest struct {
	Source                 OneWaySourcePolicy
	Target                 OneWayTargetPolicy
	Permission             core.SharePermission
	Action                 OneWayAction
	ExpectedBaseRevisionID core.RevisionID
	TargetRevisionID       core.RevisionID
}

// OneWayDecision is safe to hand to later preparation or scheduling code.
// Filesystem work is permitted only when Apply is true.
type OneWayDecision struct {
	Outcome              OneWayDecisionOutcome
	Action               OneWayAction
	RequiredCapability   core.Capability
	TargetDrift          bool
	Apply                bool
	PreserveConflictCopy bool
}

func DecideOneWayChange(request OneWayDecisionRequest) (OneWayDecision, error) {
	if err := validateOneWayPolicies(request.Source, request.Target); err != nil {
		return OneWayDecision{}, err
	}

	requiredCapability, err := oneWayActionCapability(request.Action)
	if err != nil {
		return OneWayDecision{}, err
	}
	if err := authorizeOneWayAction(request, requiredCapability); err != nil {
		return OneWayDecision{}, err
	}

	decision := OneWayDecision{
		Outcome:            OneWayDecisionApply,
		Action:             request.Action,
		RequiredCapability: requiredCapability,
		Apply:              true,
	}
	if request.TargetRevisionID == request.ExpectedBaseRevisionID {
		return decision, nil
	}

	decision.TargetDrift = true
	switch request.Target.DriftPolicy {
	case TargetDriftReject:
		decision.Outcome = OneWayDecisionRejectDrift
		decision.Apply = false
	case TargetDriftPreserveConflictCopy:
		decision.Outcome = OneWayDecisionPreserveConflictCopy
		decision.PreserveConflictCopy = true
	case TargetDriftReportOnly:
		decision.Outcome = OneWayDecisionReportOnly
		decision.Apply = false
	}
	return decision, nil
}

func validateOneWayPolicies(source OneWaySourcePolicy, target OneWayTargetPolicy) error {
	if source.ShareID == "" || source.DeviceID == "" || target.ShareID == "" {
		return fmt.Errorf("%w: source share, source device, and target share are required", ErrInvalidOneWayPolicy)
	}
	if source.ShareID != target.ShareID {
		return fmt.Errorf("%w: source share %s does not match target share %s", ErrInvalidOneWayPolicy, source.ShareID, target.ShareID)
	}
	if source.Mode != storage.ShareOneWaySource {
		return fmt.Errorf("%w: %q", ErrUnsupportedSourceMode, source.Mode)
	}
	if target.Mode != storage.ShareOneWayTarget {
		return fmt.Errorf("%w: %q", ErrUnsupportedTargetMode, target.Mode)
	}
	switch target.DriftPolicy {
	case TargetDriftReject, TargetDriftPreserveConflictCopy, TargetDriftReportOnly:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedTargetDriftPolicy, target.DriftPolicy)
	}
}

func oneWayActionCapability(action OneWayAction) (core.Capability, error) {
	switch action {
	case OneWayActionAdd:
		return core.CapabilityUpload, nil
	case OneWayActionModify:
		return core.CapabilityModify, nil
	case OneWayActionDelete:
		return core.CapabilityDelete, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedOneWayAction, action)
	}
}

func authorizeOneWayAction(request OneWayDecisionRequest, required core.Capability) error {
	permission := request.Permission
	if permission.ShareID != request.Target.ShareID || permission.DeviceID != request.Source.DeviceID {
		return fmt.Errorf("%w: permission scope does not match source device and target share", ErrUnauthorizedOneWayAction)
	}
	if !permission.Capabilities[core.CapabilitySync] {
		return fmt.Errorf("%w: missing %s capability", ErrUnauthorizedOneWayAction, core.CapabilitySync)
	}
	if !permission.Capabilities[required] {
		return fmt.Errorf("%w: missing %s capability", ErrUnauthorizedOneWayAction, required)
	}
	return nil
}
