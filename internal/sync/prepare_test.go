package sync

import (
	"context"
	"errors"
	"testing"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestOneWayChangePreparationAuthorizesAndReturnsDescriptor(t *testing.T) {
	shares := &recordingShareStore{}
	request := validPreparationRequest(OneWayActionModify)
	service := OneWayChangePreparationService{Shares: shares}

	prepared, err := service.Prepare(context.Background(), request)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if prepared.RelativePath != "docs/report.txt" || prepared.SourceRevision.ID != "revision-2" || prepared.TargetRevisionID != "revision-1" {
		t.Fatalf("prepared = %+v", prepared)
	}
	if !prepared.Decision.Apply || prepared.Decision.TargetDrift {
		t.Fatalf("decision = %+v", prepared.Decision)
	}
	if len(shares.calls) != 2 || shares.calls[0].capability != core.CapabilitySync || shares.calls[1].capability != core.CapabilityModify {
		t.Fatalf("authorization calls = %+v", shares.calls)
	}
}

func TestOneWayChangePreparationReturnsDriftDecisionWithoutCreatingWork(t *testing.T) {
	shares := &recordingShareStore{}
	request := validPreparationRequest(OneWayActionModify)
	request.TargetRevisionID = "revision-target-drift"
	request.Target.DriftPolicy = TargetDriftReportOnly

	prepared, err := (OneWayChangePreparationService{Shares: shares}).Prepare(context.Background(), request)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if prepared.Decision.Outcome != OneWayDecisionReportOnly || prepared.Decision.Apply {
		t.Fatalf("decision = %+v", prepared.Decision)
	}
}

func TestOneWayChangePreparationRejectsMalformedBeforeAuthorization(t *testing.T) {
	tests := []struct {
		name   string
		change func(*OneWayChangePreparationRequest)
		want   error
	}{
		{name: "unsafe source path", change: func(request *OneWayChangePreparationRequest) { request.SourceRevision.RelativePath = "../secret.txt" }, want: ErrUnsupportedSourceRevision},
		{name: "wrong source revision ID", change: func(request *OneWayChangePreparationRequest) { request.SourceRevision.ID = "revision-other" }, want: ErrSourceRevisionMismatch},
		{name: "wrong source parent", change: func(request *OneWayChangePreparationRequest) {
			request.Change.ExpectedBaseRevisionID = "revision-other"
		}, want: ErrSourceRevisionMismatch},
		{name: "wrong source revision type", change: func(request *OneWayChangePreparationRequest) {
			request.SourceRevision.EntryType = core.EntrySymlinkUnsupported
		}, want: ErrUnsupportedSourceRevision},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shares := &recordingShareStore{}
			request := validPreparationRequest(OneWayActionModify)
			test.change(&request)
			_, err := (OneWayChangePreparationService{Shares: shares}).Prepare(context.Background(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(shares.calls) != 0 {
				t.Fatalf("malformed request reached authorization: %+v", shares.calls)
			}
		})
	}
}

func TestOneWayChangePreparationRejectsUnauthorizedWithoutPreparing(t *testing.T) {
	tests := []struct {
		name       string
		action     OneWayAction
		capability core.Capability
	}{
		{name: "missing sync", action: OneWayActionModify, capability: core.CapabilitySync},
		{name: "missing modify", action: OneWayActionModify, capability: core.CapabilityModify},
		{name: "missing delete", action: OneWayActionDelete, capability: core.CapabilityDelete},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shares := &recordingShareStore{deny: map[core.Capability]error{test.capability: errors.New("denied")}}
			request := validPreparationRequest(test.action)
			if test.action == OneWayActionDelete {
				request.SourceRevision.EntryType = core.EntryDeleted
				request.SourceRevision.IsDeleted = true
				request.SourceRevision.ContentHash = ""
				request.SourceRevision.HashAlgorithm = ""
				request.SourceRevision.Size = 0
				request.Change.Action = OneWayActionDelete
			}
			_, err := (OneWayChangePreparationService{Shares: shares}).Prepare(context.Background(), request)
			if !errors.Is(err, ErrChangeAuthorization) {
				t.Fatalf("error = %v, want authorization error", err)
			}
			if len(shares.calls) == 0 || len(shares.calls) > 2 {
				t.Fatalf("authorization calls = %+v", shares.calls)
			}
		})
	}
}

func TestOneWayChangePreparationRejectsPolicyBeforeAuthorization(t *testing.T) {
	shares := &recordingShareStore{}
	request := validPreparationRequest(OneWayActionModify)
	request.Target.Mode = "two_way"

	_, err := (OneWayChangePreparationService{Shares: shares}).Prepare(context.Background(), request)
	if !errors.Is(err, ErrUnsupportedTargetMode) {
		t.Fatalf("error = %v, want unsupported target mode", err)
	}
	if len(shares.calls) != 0 {
		t.Fatalf("unsupported policy reached authorization: %+v", shares.calls)
	}
}

func validPreparationRequest(action OneWayAction) OneWayChangePreparationRequest {
	return OneWayChangePreparationRequest{
		Change: OneWayChangeRequest{
			ProtocolVersion:        RevisionManifestProtocolVersion,
			RequestID:              "request-prepare-1",
			SourceDeviceID:         "DEVICE-1",
			TargetDeviceID:         "DEVICE-2",
			ShareID:                "share-1",
			RevisionID:             "revision-2",
			ExpectedBaseRevisionID: "revision-1",
			Action:                 action,
		},
		Source: OneWaySourcePolicy{ShareID: "share-1", DeviceID: "DEVICE-1", Mode: "one_way_source"},
		Target: OneWayTargetPolicy{ShareID: "share-1", Mode: "one_way_target", DriftPolicy: TargetDriftReject},
		SourceRevision: core.Revision{
			ID: "revision-2", ShareID: "share-1", RelativePath: "docs/report.txt",
			EntryType: core.EntryFile, Size: 5, ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			HashAlgorithm: "sha256", ParentRevisionID: "revision-1", OriginDeviceID: "DEVICE-1", Sequence: 2,
		},
		TargetRevisionID: "revision-1",
	}
}

type authorizationCall struct {
	deviceID   core.DeviceID
	shareID    core.ShareID
	capability core.Capability
	remote     bool
}

type recordingShareStore struct {
	calls []authorizationCall
	deny  map[core.Capability]error
}

func (store *recordingShareStore) SaveShare(context.Context, storage.Share) error { return nil }
func (store *recordingShareStore) SetPermission(context.Context, core.SharePermission) error {
	return nil
}
func (store *recordingShareStore) Authorize(_ context.Context, deviceID core.DeviceID, shareID core.ShareID, capability core.Capability, remote bool) error {
	store.calls = append(store.calls, authorizationCall{deviceID: deviceID, shareID: shareID, capability: capability, remote: remote})
	if err := store.deny[capability]; err != nil {
		return err
	}
	return nil
}
