package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

type AuthenticatedPeerSession interface {
	RemoteDeviceID() core.DeviceID
}

type AuthenticatedOneWayWorkService struct {
	Preparation   OneWayChangePreparationService
	Work          storage.OneWayWorkStore
	Now           func() time.Time
	NewTransferID func() (core.TransferID, error)
	NewJobID      func() (string, error)
}

type AuthenticatedOneWayWorkRequest struct {
	Advertisement RevisionAdvertisementRequest
	Preparation   OneWayChangePreparationRequest
	ChunkSize     int64
}

type AuthenticatedOneWayWorkResult struct {
	Prepared       PreparedOneWayChange
	Transfer       core.Transfer
	Job            core.OneWayJob
	AlreadyCreated bool
}

func (service AuthenticatedOneWayWorkService) PrepareAndQueue(ctx context.Context, session AuthenticatedPeerSession, request AuthenticatedOneWayWorkRequest) (AuthenticatedOneWayWorkResult, error) {
	if ctx == nil {
		return AuthenticatedOneWayWorkResult{}, errors.New("authenticated one-way context is required")
	}
	if err := ctx.Err(); err != nil {
		return AuthenticatedOneWayWorkResult{}, err
	}
	if session == nil {
		return AuthenticatedOneWayWorkResult{}, errors.New("verified peer session is required")
	}
	authenticatedPeerID := session.RemoteDeviceID()
	if authenticatedPeerID == "" {
		return AuthenticatedOneWayWorkResult{}, errors.New("verified peer session is required")
	}
	if service.Work == nil {
		return AuthenticatedOneWayWorkResult{}, errors.New("authenticated one-way work store is required")
	}
	if request.ChunkSize <= 0 {
		return AuthenticatedOneWayWorkResult{}, errors.New("authenticated one-way chunk size must be positive")
	}
	if err := validateAuthenticatedAdvertisement(authenticatedPeerID, request.Advertisement, request.Preparation); err != nil {
		return AuthenticatedOneWayWorkResult{}, err
	}

	request.Preparation.AuthenticatedPeerID = authenticatedPeerID
	prepared, err := service.Preparation.Prepare(ctx, request.Preparation)
	if err != nil {
		return AuthenticatedOneWayWorkResult{}, err
	}
	result := AuthenticatedOneWayWorkResult{Prepared: prepared}
	if !prepared.Decision.Apply {
		return result, nil
	}
	requiredCapability, err := oneWayActionCapability(prepared.Change.Action)
	if err != nil {
		return AuthenticatedOneWayWorkResult{}, err
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	if now.IsZero() {
		return AuthenticatedOneWayWorkResult{}, errors.New("authenticated one-way clock returned zero time")
	}
	newTransferID := service.NewTransferID
	if newTransferID == nil {
		newTransferID = func() (core.TransferID, error) {
			value, err := newRandomID()
			return core.TransferID(value), err
		}
	}
	transferID, err := newTransferID()
	if err != nil {
		return AuthenticatedOneWayWorkResult{}, fmt.Errorf("create authenticated transfer ID: %w", err)
	}
	if transferID == "" {
		return AuthenticatedOneWayWorkResult{}, errors.New("create authenticated transfer ID: generated ID is empty")
	}
	newJobID := service.NewJobID
	if newJobID == nil {
		newJobID = newRandomID
	}
	jobID, err := newJobID()
	if err != nil {
		return AuthenticatedOneWayWorkResult{}, fmt.Errorf("create authenticated job ID: %w", err)
	}
	if jobID == "" {
		return AuthenticatedOneWayWorkResult{}, errors.New("create authenticated job ID: generated ID is empty")
	}
	transferRecord := core.Transfer{
		ID: transferID, Direction: core.TransferReceive, PeerDeviceID: prepared.AuthenticatedPeerID,
		ShareID: prepared.Change.ShareID, RelativePath: prepared.RelativePath, State: core.TransferQueued,
		Size: prepared.SourceRevision.Size, ChunkSize: request.ChunkSize,
		ContentHash: prepared.SourceRevision.ContentHash, HashAlgorithm: prepared.SourceRevision.HashAlgorithm,
		CreatedAt: now, UpdatedAt: now,
	}
	job := core.OneWayJob{
		ID: jobID, TransferID: transferID, PeerDeviceID: prepared.AuthenticatedPeerID,
		ShareID: prepared.Change.ShareID, RevisionID: prepared.SourceRevision.ID,
		RelativePath: prepared.RelativePath, RequiredCapability: requiredCapability,
		Remote: request.Preparation.Remote,
		State:  core.OneWayJobQueued, CreatedAt: now, UpdatedAt: now,
	}
	created, err := service.Work.CreateAuthenticatedOneWayWork(ctx, storage.AuthenticatedOneWayWork{
		Transfer: transferRecord, Job: job,
		RequiredCapabilities: []core.Capability{core.CapabilitySync, requiredCapability},
		Remote:               request.Preparation.Remote,
	})
	if err != nil {
		return AuthenticatedOneWayWorkResult{}, fmt.Errorf("create authenticated one-way work: %w", err)
	}
	result.Transfer = transferRecord
	result.Job = job
	result.AlreadyCreated = created.AlreadyCreated
	return result, nil
}

func validateAuthenticatedAdvertisement(authenticatedPeerID core.DeviceID, advertisement RevisionAdvertisementRequest, preparation OneWayChangePreparationRequest) error {
	if err := advertisement.Validate(); err != nil {
		return fmt.Errorf("invalid authenticated revision advertisement: %w", err)
	}
	if advertisement.SourceDeviceID != authenticatedPeerID {
		return fmt.Errorf("%w: authenticated %s, advertisement names %s", ErrAuthenticatedPeerMismatch, authenticatedPeerID, advertisement.SourceDeviceID)
	}
	change := preparation.Change
	if advertisement.SourceDeviceID != change.SourceDeviceID || advertisement.TargetDeviceID != change.TargetDeviceID || advertisement.ShareID != change.ShareID {
		return errors.New("authenticated advertisement and change scope do not match")
	}
	want := preparation.SourceRevision
	for _, entry := range advertisement.Revisions {
		if entry.RevisionID != change.RevisionID {
			continue
		}
		if entry.ParentRevisionID != want.ParentRevisionID || entry.RelativePath != want.RelativePath ||
			entry.EntryType != want.EntryType || entry.Size != want.Size || entry.ContentHash != want.ContentHash ||
			entry.HashAlgorithm != want.HashAlgorithm || entry.Sequence != want.Sequence || entry.IsDeleted != want.IsDeleted {
			return errors.New("authenticated advertisement revision does not match preparation")
		}
		return nil
	}
	return fmt.Errorf("authenticated advertisement does not contain revision %s", change.RevisionID)
}

// AuthorizeAuthenticatedOneWayJob rechecks receiver-side work immediately
// before execution. Send jobs are outside this receiver authorization gate.
func AuthorizeAuthenticatedOneWayJob(ctx context.Context, transfers storage.TransferStore, shares storage.ShareStore, job core.OneWayJob, remote bool) error {
	if transfers == nil || shares == nil {
		return errors.New("transfer and share stores are required for one-way job authorization")
	}
	transferRecord, err := transfers.GetTransfer(ctx, job.TransferID)
	if err != nil {
		return fmt.Errorf("load one-way job transfer: %w", err)
	}
	if transferRecord.Direction != core.TransferReceive {
		return nil
	}
	if job.PeerDeviceID == "" || job.RequiredCapability == "" ||
		transferRecord.PeerDeviceID != job.PeerDeviceID || transferRecord.ShareID != job.ShareID ||
		transferRecord.RelativePath != job.RelativePath {
		return errors.New("receive job authenticated scope does not match its transfer")
	}
	for _, capability := range []core.Capability{core.CapabilitySync, job.RequiredCapability} {
		if err := shares.Authorize(ctx, job.PeerDeviceID, job.ShareID, capability, remote); err != nil {
			return fmt.Errorf("authorize receive job %s capability: %w", capability, err)
		}
	}
	return nil
}
