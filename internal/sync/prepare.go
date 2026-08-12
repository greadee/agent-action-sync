package sync

import (
	"context"
	"errors"
	"fmt"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

var (
	ErrInvalidChangePreparation  = errors.New("invalid one-way change preparation request")
	ErrSourceRevisionMismatch    = errors.New("source revision does not match change request")
	ErrUnsupportedSourceRevision = errors.New("unsupported source revision")
	ErrChangeAuthorization       = errors.New("one-way change authorization failed")
	ErrAuthenticatedPeerMismatch = errors.New("authenticated peer does not match one-way source")
)

// OneWayChangePreparationRequest contains the authenticated request context
// and the source revision already obtained from the validated advertisement.
// It is intentionally free of filesystem paths or open handles.
type OneWayChangePreparationRequest struct {
	AuthenticatedPeerID core.DeviceID
	Change              OneWayChangeRequest
	Source              OneWaySourcePolicy
	Target              OneWayTargetPolicy
	SourceRevision      core.Revision
	TargetRevisionID    core.RevisionID
	Remote              bool
}

// PreparedOneWayChange is a validated descriptor for later transfer work. It
// does not create files, transfer rows, revisions, or file-index updates.
type PreparedOneWayChange struct {
	AuthenticatedPeerID core.DeviceID
	Change              OneWayChangeRequest
	SourceRevision      core.Revision
	TargetRevisionID    core.RevisionID
	RelativePath        string
	Decision            OneWayDecision
}

type OneWayChangePreparationService struct {
	Shares storage.ShareStore
}

func (service OneWayChangePreparationService) Prepare(ctx context.Context, request OneWayChangePreparationRequest) (PreparedOneWayChange, error) {
	if service.Shares == nil {
		return PreparedOneWayChange{}, fmt.Errorf("%w: share store is required", ErrInvalidChangePreparation)
	}
	if err := validatePreparationRequest(request); err != nil {
		return PreparedOneWayChange{}, err
	}

	requiredCapability, err := oneWayActionCapability(request.Change.Action)
	if err != nil {
		return PreparedOneWayChange{}, err
	}
	permission := core.SharePermission{
		ShareID:  request.Change.ShareID,
		DeviceID: request.Change.SourceDeviceID,
		Capabilities: map[core.Capability]bool{
			core.CapabilitySync: true,
			requiredCapability:  true,
		},
	}
	decision, err := DecideOneWayChange(OneWayDecisionRequest{
		Source:                 request.Source,
		Target:                 request.Target,
		Permission:             permission,
		Action:                 request.Change.Action,
		ExpectedBaseRevisionID: request.Change.ExpectedBaseRevisionID,
		TargetRevisionID:       request.TargetRevisionID,
	})
	if err != nil {
		return PreparedOneWayChange{}, err
	}
	for _, capability := range []core.Capability{core.CapabilitySync, requiredCapability} {
		if err := service.Shares.Authorize(ctx, request.Change.SourceDeviceID, request.Change.ShareID, capability, request.Remote); err != nil {
			return PreparedOneWayChange{}, fmt.Errorf("%w: %s capability: %v", ErrChangeAuthorization, capability, err)
		}
	}

	return PreparedOneWayChange{
		AuthenticatedPeerID: request.AuthenticatedPeerID,
		Change:              request.Change,
		SourceRevision:      request.SourceRevision,
		TargetRevisionID:    request.TargetRevisionID,
		RelativePath:        request.SourceRevision.RelativePath,
		Decision:            decision,
	}, nil
}

func validatePreparationRequest(request OneWayChangePreparationRequest) error {
	if request.AuthenticatedPeerID == "" {
		return fmt.Errorf("%w: authenticated peer ID is required", ErrInvalidChangePreparation)
	}
	if err := request.Change.Validate(); err != nil {
		return fmt.Errorf("%w: change: %v", ErrInvalidChangePreparation, err)
	}
	if request.AuthenticatedPeerID != request.Change.SourceDeviceID {
		return fmt.Errorf("%w: authenticated %s, request names %s", ErrAuthenticatedPeerMismatch, request.AuthenticatedPeerID, request.Change.SourceDeviceID)
	}
	if request.Source.ShareID != request.Change.ShareID || request.Target.ShareID != request.Change.ShareID || request.Source.DeviceID != request.Change.SourceDeviceID {
		return fmt.Errorf("%w: policy and request scope do not match", ErrInvalidChangePreparation)
	}
	if err := validateSourceRevision(request.SourceRevision, request.Change); err != nil {
		return err
	}
	return nil
}

func validateSourceRevision(revision core.Revision, change OneWayChangeRequest) error {
	manifestEntry := RevisionManifestEntry{
		RevisionID:       revision.ID,
		ParentRevisionID: revision.ParentRevisionID,
		RelativePath:     revision.RelativePath,
		EntryType:        revision.EntryType,
		Size:             revision.Size,
		ContentHash:      revision.ContentHash,
		HashAlgorithm:    revision.HashAlgorithm,
		Sequence:         revision.Sequence,
		IsDeleted:        revision.IsDeleted,
	}
	if err := manifestEntry.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupportedSourceRevision, err)
	}
	if revision.ID != change.RevisionID || revision.ShareID != change.ShareID || revision.OriginDeviceID != change.SourceDeviceID {
		return fmt.Errorf("%w: identity or share does not match request", ErrSourceRevisionMismatch)
	}
	if revision.ParentRevisionID != change.ExpectedBaseRevisionID {
		return fmt.Errorf("%w: expected base %q, source parent %q", ErrSourceRevisionMismatch, change.ExpectedBaseRevisionID, revision.ParentRevisionID)
	}
	switch change.Action {
	case OneWayActionAdd, OneWayActionModify:
		if revision.IsDeleted {
			return fmt.Errorf("%w: %s action cannot use a deletion revision", ErrSourceRevisionMismatch, change.Action)
		}
	case OneWayActionDelete:
		if !revision.IsDeleted {
			return fmt.Errorf("%w: delete action requires a deletion revision", ErrSourceRevisionMismatch)
		}
	}
	return nil
}
