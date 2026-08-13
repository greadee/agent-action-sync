package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/pairing"
)

type CurrentPairingIdentity func() (identity.DeviceIdentity, string, error)

type PairingCoordinator struct {
	Service         pairing.Service
	CurrentIdentity CurrentPairingIdentity
}

type PairingInvitationManager interface {
	CreatePairingInvitation(ctx context.Context, request InvitationRequest) (InvitationDTO, error)
	InspectPairingInvitation(ctx context.Context, request EncodedInvitation) (InvitationInspection, error)
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
		default:
			writeError(writer, request, errNotFound)
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
	default:
		return err
	}
}
