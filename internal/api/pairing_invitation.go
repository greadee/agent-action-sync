package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/pairing"
)

type CurrentPairingIdentity func() (identity.DeviceIdentity, string, error)

type PairingCoordinator struct {
	Service         pairing.Service
	CurrentIdentity CurrentPairingIdentity
	mutationMu      sync.Mutex
}

type PairingInvitationManager interface {
	CreatePairingInvitation(ctx context.Context, request InvitationRequest) (InvitationDTO, error)
	InspectPairingInvitation(ctx context.Context, request EncodedInvitation) (InvitationInspection, error)
	AcceptPairingInvitation(ctx context.Context, request AcceptanceRequest) (Acceptance, error)
	RevokePairingDevice(ctx context.Context, deviceID core.DeviceID) (Revocation, error)
}

func (coordinator *PairingCoordinator) CreatePairingInvitation(ctx context.Context, request InvitationRequest) (InvitationDTO, error) {
	if coordinator == nil || coordinator.CurrentIdentity == nil {
		return InvitationDTO{}, errUnavailable
	}
	if err := request.Validate(); err != nil {
		return InvitationDTO{}, invalidPairingRequest(err)
	}
	localIdentity, displayName, err := coordinator.CurrentIdentity()
	if err != nil {
		return InvitationDTO{}, errUnavailable
	}
	capabilities := make([]core.Capability, len(request.Capabilities))
	for index, capability := range request.Capabilities {
		capabilities[index] = core.Capability(capability)
	}
	created, err := coordinator.Service.CreateInvitation(ctx, localIdentity, displayName, time.Duration(request.TTLSeconds)*time.Second, capabilities)
	if err != nil {
		return InvitationDTO{}, mapPairingFailure(err)
	}
	return InvitationDTO{
		Invitation: created.Encoded, Fingerprint: created.Invite.Fingerprint,
		OneTimeCode: created.Invite.OneTimeCode, ExpiresAt: created.Invite.ExpiresAt.UTC().Format(timeFormat),
	}, nil
}

func (coordinator *PairingCoordinator) InspectPairingInvitation(ctx context.Context, request EncodedInvitation) (InvitationInspection, error) {
	if err := ctx.Err(); err != nil {
		return InvitationInspection{}, err
	}
	if err := request.Validate(); err != nil {
		return InvitationInspection{}, invalidPairingRequest(err)
	}
	invite, err := coordinator.Service.InspectInvitation(request.Invitation)
	if err != nil {
		return InvitationInspection{}, mapInvitationFailure(err)
	}
	capabilities := make([]string, len(invite.RequestedCaps))
	for index, capability := range invite.RequestedCaps {
		capabilities[index] = string(capability)
	}
	return InvitationInspection{
		Valid: true, InviteID: invite.InviteID, DeviceID: string(invite.DeviceID), DeviceName: invite.DisplayName,
		Fingerprint: invite.Fingerprint, CreatedAt: invite.CreatedAt.UTC().Format(timeFormat),
		ExpiresAt: invite.ExpiresAt.UTC().Format(timeFormat), Capabilities: capabilities,
	}, nil
}

func (coordinator *PairingCoordinator) AcceptPairingInvitation(ctx context.Context, request AcceptanceRequest) (Acceptance, error) {
	if coordinator == nil || coordinator.CurrentIdentity == nil {
		return Acceptance{}, errUnavailable
	}
	if err := request.Validate(); err != nil {
		return Acceptance{}, invalidPairingRequest(err)
	}
	invite, err := coordinator.Service.InspectInvitation(request.Invitation)
	if err != nil {
		return Acceptance{}, mapInvitationFailure(err)
	}
	localIdentity, _, err := coordinator.CurrentIdentity()
	if err != nil {
		return Acceptance{}, errUnavailable
	}
	if invite.DeviceID == localIdentity.DeviceID {
		return Acceptance{}, invalidPairingRequest(errors.New("cannot pair a device with itself"))
	}
	grants := make([]pairing.Grant, len(request.Grants))
	for index, requested := range request.Grants {
		capabilities := make([]core.Capability, len(requested.Capabilities))
		for capabilityIndex, capability := range requested.Capabilities {
			capabilities[capabilityIndex] = core.Capability(capability)
		}
		lanOnly := true
		if requested.LANOnly != nil {
			lanOnly = *requested.LANOnly
		}
		grants[index] = pairing.Grant{ShareID: core.ShareID(requested.ShareID), Capabilities: capabilities, LANOnly: lanOnly}
	}

	coordinator.mutationMu.Lock()
	defer coordinator.mutationMu.Unlock()
	result, err := coordinator.Service.Accept(ctx, pairing.AcceptRequest{
		LocalDeviceID: localIdentity.DeviceID, EncodedInvite: request.Invitation,
		ExpectedFingerprint: request.ExpectedFingerprint, OneTimeCode: request.OneTimeCode, Grants: grants,
	})
	if err != nil {
		return Acceptance{}, mapPairingFailure(err)
	}
	return Acceptance{
		Accepted: true, AlreadyAccepted: result.AlreadyAccepted, DeviceID: string(result.Peer.DeviceID),
		DeviceName: result.Peer.DisplayName, Fingerprint: result.Peer.Fingerprint,
	}, nil
}

func (coordinator *PairingCoordinator) RevokePairingDevice(ctx context.Context, deviceID core.DeviceID) (Revocation, error) {
	if coordinator == nil || coordinator.CurrentIdentity == nil {
		return Revocation{}, errUnavailable
	}
	value := strings.TrimSpace(string(deviceID))
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "/\\") || value != string(deviceID) {
		return Revocation{}, invalidPairingRequest(errors.New("device ID is invalid"))
	}
	localIdentity, _, err := coordinator.CurrentIdentity()
	if err != nil {
		return Revocation{}, errUnavailable
	}
	if core.DeviceID(value) == localIdentity.DeviceID {
		return Revocation{}, invalidPairingRequest(errors.New("cannot revoke the local device"))
	}
	coordinator.mutationMu.Lock()
	defer coordinator.mutationMu.Unlock()
	result, err := coordinator.Service.Revoke(ctx, localIdentity.DeviceID, core.DeviceID(value))
	if err != nil {
		return Revocation{}, mapPairingFailure(err)
	}
	return Revocation{DeviceID: value, Revoked: true, AlreadyRevoked: result.AlreadyRevoked}, nil
}

func NewPairingInvitationHandler(manager PairingInvitationManager) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeError(writer, request, errMethodNotAllowed)
			return
		}
		if manager == nil {
			writeError(writer, request, errUnavailable)
			return
		}
		switch request.URL.Path {
		case "/api/v1/pairing/invitations":
			var command InvitationRequest
			if err := decodeJSON(request, &command); err != nil {
				writeError(writer, request, err)
				return
			}
			invitation, err := manager.CreatePairingInvitation(request.Context(), command)
			if err != nil {
				writeError(writer, request, err)
				return
			}
			writeJSON(writer, request, http.StatusCreated, invitation)
		case "/api/v1/pairing/inspect":
			var command EncodedInvitation
			if err := decodeJSON(request, &command); err != nil {
				writeError(writer, request, err)
				return
			}
			inspection, err := manager.InspectPairingInvitation(request.Context(), command)
			if err != nil {
				writeError(writer, request, err)
				return
			}
			writeJSON(writer, request, http.StatusOK, inspection)
		case "/api/v1/pairing/acceptances":
			var command AcceptanceRequest
			if err := decodeJSON(request, &command); err != nil {
				writeError(writer, request, err)
				return
			}
			acceptance, err := manager.AcceptPairingInvitation(request.Context(), command)
			if err != nil {
				writeError(writer, request, err)
				return
			}
			writeJSON(writer, request, http.StatusOK, acceptance)
		default:
			const prefix = "/api/v1/devices/"
			const suffix = "/revocation"
			if !strings.HasPrefix(request.URL.Path, prefix) || !strings.HasSuffix(request.URL.Path, suffix) {
				writeError(writer, request, errNotFound)
				return
			}
			deviceID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, prefix), suffix)
			revocation, err := manager.RevokePairingDevice(request.Context(), core.DeviceID(deviceID))
			if err != nil {
				writeError(writer, request, err)
				return
			}
			writeJSON(writer, request, http.StatusOK, revocation)
		}
	})
}

func mapInvitationFailure(err error) error {
	if errors.Is(err, identity.ErrPairingInviteExpired) {
		return mapPairingFailure(err)
	}
	return &APIError{Status: http.StatusBadRequest, Code: "pairing_invitation_invalid", Message: "pairing invitation is invalid", Internal: err}
}

func invalidPairingRequest(err error) error {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_pairing_request", Message: "pairing request is invalid", Internal: err}
}

func mapPairingFailure(err error) error {
	switch {
	case errors.Is(err, identity.ErrPairingInviteExpired):
		return &APIError{Status: http.StatusBadRequest, Code: "pairing_invitation_expired", Message: "pairing invitation has expired", Internal: err}
	case errors.Is(err, identity.ErrPairingInviteInvalid):
		return &APIError{Status: http.StatusBadRequest, Code: "pairing_invitation_invalid", Message: "pairing invitation is invalid", Internal: err}
	case errors.Is(err, identity.ErrPairingCodeMismatch), errors.Is(err, identity.ErrPairingFingerprintMismatch):
		return &APIError{Status: http.StatusBadRequest, Code: "pairing_confirmation_failed", Message: "pairing confirmation failed", Internal: err}
	case strings.Contains(err.Error(), "already accepted for a different identity"), strings.Contains(err.Error(), "replace existing device identity"):
		return &APIError{Status: http.StatusConflict, Code: "pairing_conflict", Message: "pairing request conflicts with current device state", Internal: err}
	default:
		return err
	}
}
