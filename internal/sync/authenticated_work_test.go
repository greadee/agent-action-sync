package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestAuthenticatedOneWayWorkBindsSessionPeerToDurableWork(t *testing.T) {
	shares := &recordingShareStore{}
	work := &recordingOneWayWorkStore{}
	now := time.Unix(100, 0).UTC()
	service := AuthenticatedOneWayWorkService{
		Preparation:   OneWayChangePreparationService{Shares: shares},
		Work:          work,
		Now:           func() time.Time { return now },
		NewTransferID: func() (core.TransferID, error) { return "transfer-authenticated", nil },
		NewJobID:      func() (string, error) { return "job-authenticated", nil },
	}
	request := validPreparationRequest(OneWayActionModify)
	request.AuthenticatedPeerID = "CALLER-CONTROLLED"
	request.Remote = true

	result, err := service.PrepareAndQueue(context.Background(), authenticatedTestSession("DEVICE-1"), AuthenticatedOneWayWorkRequest{Advertisement: advertisementForPreparation(request), Preparation: request, ChunkSize: 1024})
	if err != nil {
		t.Fatalf("PrepareAndQueue: %v", err)
	}
	if result.Prepared.AuthenticatedPeerID != "DEVICE-1" || result.Transfer.PeerDeviceID != "DEVICE-1" || result.Job.PeerDeviceID != "DEVICE-1" {
		t.Fatalf("authenticated identity was not preserved: %+v", result)
	}
	if result.Transfer.ID != "transfer-authenticated" || result.Job.TransferID != result.Transfer.ID || result.Job.RevisionID != result.Prepared.SourceRevision.ID {
		t.Fatalf("durable work does not match prepared change: %+v", result)
	}
	if result.Job.RequiredCapability != core.CapabilityModify || len(work.calls) != 1 {
		t.Fatalf("work calls = %+v", work.calls)
	}
	got := work.calls[0]
	if len(got.RequiredCapabilities) != 2 || got.RequiredCapabilities[0] != core.CapabilitySync || got.RequiredCapabilities[1] != core.CapabilityModify || !got.Remote {
		t.Fatalf("required authorization = %+v", got)
	}
}

func TestAuthenticatedOneWayWorkRejectsManifestIdentityDriftBeforeAuthorization(t *testing.T) {
	shares := &recordingShareStore{}
	work := &recordingOneWayWorkStore{}
	service := AuthenticatedOneWayWorkService{Preparation: OneWayChangePreparationService{Shares: shares}, Work: work}

	request := validPreparationRequest(OneWayActionModify)
	_, err := service.PrepareAndQueue(context.Background(), authenticatedTestSession("DEVICE-OTHER"), AuthenticatedOneWayWorkRequest{Advertisement: advertisementForPreparation(request), Preparation: request, ChunkSize: 1024})
	if !errors.Is(err, ErrAuthenticatedPeerMismatch) {
		t.Fatalf("error = %v, want %v", err, ErrAuthenticatedPeerMismatch)
	}
	if len(shares.calls) != 0 || len(work.calls) != 0 {
		t.Fatalf("identity drift reached authorization or durable work: auth=%+v work=%+v", shares.calls, work.calls)
	}
}

func TestAuthenticatedOneWayWorkDoesNotQueueReportOnlyDecision(t *testing.T) {
	shares := &recordingShareStore{}
	work := &recordingOneWayWorkStore{}
	request := validPreparationRequest(OneWayActionModify)
	request.TargetRevisionID = "revision-drift"
	request.Target.DriftPolicy = TargetDriftReportOnly

	result, err := (AuthenticatedOneWayWorkService{Preparation: OneWayChangePreparationService{Shares: shares}, Work: work}).PrepareAndQueue(
		context.Background(), authenticatedTestSession("DEVICE-1"), AuthenticatedOneWayWorkRequest{Advertisement: advertisementForPreparation(request), Preparation: request, ChunkSize: 1024},
	)
	if err != nil {
		t.Fatalf("PrepareAndQueue: %v", err)
	}
	if result.Prepared.Decision.Apply || result.Transfer.ID != "" || result.Job.ID != "" || len(work.calls) != 0 {
		t.Fatalf("report-only result queued work: %+v calls=%+v", result, work.calls)
	}
}

func TestAuthenticatedOneWayWorkRejectsAdvertisementRevisionDrift(t *testing.T) {
	shares := &recordingShareStore{}
	work := &recordingOneWayWorkStore{}
	request := validPreparationRequest(OneWayActionModify)
	advertisement := advertisementForPreparation(request)
	advertisement.Revisions[0].ContentHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	_, err := (AuthenticatedOneWayWorkService{Preparation: OneWayChangePreparationService{Shares: shares}, Work: work}).PrepareAndQueue(
		context.Background(), authenticatedTestSession("DEVICE-1"), AuthenticatedOneWayWorkRequest{Advertisement: advertisement, Preparation: request, ChunkSize: 1024},
	)
	if err == nil {
		t.Fatal("advertisement revision drift was accepted")
	}
	if len(shares.calls) != 0 || len(work.calls) != 0 {
		t.Fatalf("advertisement drift reached authorization or durable work: auth=%+v work=%+v", shares.calls, work.calls)
	}
}

func TestAuthorizeAuthenticatedOneWayJobRechecksTransferScopeAndPermissions(t *testing.T) {
	job := core.OneWayJob{ID: "job-1", TransferID: "transfer-1", PeerDeviceID: "DEVICE-1", ShareID: "share-1", RevisionID: "revision-1", RelativePath: "docs/report.txt", RequiredCapability: core.CapabilityModify}
	transfers := &recordingTransferStore{transfer: core.Transfer{ID: job.TransferID, Direction: core.TransferReceive, PeerDeviceID: job.PeerDeviceID, ShareID: job.ShareID, RelativePath: job.RelativePath}}
	shares := &recordingShareStore{}

	if err := AuthorizeAuthenticatedOneWayJob(context.Background(), transfers, shares, job, true); err != nil {
		t.Fatalf("AuthorizeAuthenticatedOneWayJob: %v", err)
	}
	if len(shares.calls) != 2 || shares.calls[0].capability != core.CapabilitySync || shares.calls[1].capability != core.CapabilityModify {
		t.Fatalf("authorization calls = %+v", shares.calls)
	}

	job.PeerDeviceID = "DEVICE-OTHER"
	shares.calls = nil
	if err := AuthorizeAuthenticatedOneWayJob(context.Background(), transfers, shares, job, true); err == nil {
		t.Fatal("expected transfer identity mismatch to fail")
	}
	if len(shares.calls) != 0 {
		t.Fatalf("mismatched work reached permission check: %+v", shares.calls)
	}
}

type authenticatedTestSession core.DeviceID

func (session authenticatedTestSession) RemoteDeviceID() core.DeviceID { return core.DeviceID(session) }

func advertisementForPreparation(request OneWayChangePreparationRequest) RevisionAdvertisementRequest {
	revision := request.SourceRevision
	return RevisionAdvertisementRequest{
		ProtocolVersion: request.Change.ProtocolVersion,
		RequestID:       "advertisement-" + request.Change.RequestID,
		SourceDeviceID:  request.Change.SourceDeviceID,
		TargetDeviceID:  request.Change.TargetDeviceID,
		ShareID:         request.Change.ShareID,
		Limits:          RevisionManifestLimits{MaxEntries: 1, MaxBytes: 4096},
		Revisions: []RevisionManifestEntry{{
			RevisionID: revision.ID, ParentRevisionID: revision.ParentRevisionID, RelativePath: revision.RelativePath,
			EntryType: revision.EntryType, Size: revision.Size, ContentHash: revision.ContentHash,
			HashAlgorithm: revision.HashAlgorithm, Sequence: revision.Sequence, IsDeleted: revision.IsDeleted,
		}},
	}
}

type recordingOneWayWorkStore struct {
	calls []storage.AuthenticatedOneWayWork
	err   error
}

func (store *recordingOneWayWorkStore) CreateAuthenticatedOneWayWork(_ context.Context, work storage.AuthenticatedOneWayWork) (storage.AuthenticatedOneWayWorkResult, error) {
	store.calls = append(store.calls, work)
	return storage.AuthenticatedOneWayWorkResult{}, store.err
}

type recordingTransferStore struct {
	transfer core.Transfer
	err      error
}

func (store *recordingTransferStore) SaveTransfer(context.Context, core.Transfer) error { return nil }
func (store *recordingTransferStore) GetTransfer(context.Context, core.TransferID) (core.Transfer, error) {
	return store.transfer, store.err
}
func (store *recordingTransferStore) SaveChunk(context.Context, core.TransferChunk) error { return nil }
func (store *recordingTransferStore) VerifiedChunks(context.Context, core.TransferID) ([]core.TransferChunk, error) {
	return nil, nil
}
