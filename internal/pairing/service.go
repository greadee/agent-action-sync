package pairing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/storage"
)

const (
	AuditInvitationCreated = "pairing.invitation_created"
	AuditPairingAccepted   = "pairing.accepted"
	AuditPairingRevoked    = "pairing.revoked"
)

type Service struct {
	Pairings storage.PairingStore
	Audit    storage.AuditStore
	Now      func() time.Time
	Random   io.Reader
}

type CreatedInvitation struct {
	Invite  identity.PairingInvite
	Encoded string
}

type Grant struct {
	ShareID      core.ShareID
	Capabilities []core.Capability
	LANOnly      bool
}

type AcceptRequest struct {
	LocalDeviceID       core.DeviceID
	EncodedInvite       string
	ExpectedFingerprint string
	OneTimeCode         string
	Grants              []Grant
}

type AcceptResult struct {
	Peer            identity.PairingPeer
	AlreadyAccepted bool
}

type RevokeResult struct {
	AlreadyRevoked bool
}

func (service Service) CreateInvitation(ctx context.Context, localIdentity identity.DeviceIdentity, displayName string, ttl time.Duration, requested []core.Capability) (CreatedInvitation, error) {
	if ctx == nil {
		return CreatedInvitation{}, errors.New("pairing invitation context is required")
	}
	if err := ctx.Err(); err != nil {
		return CreatedInvitation{}, err
	}
	if service.Audit == nil {
		return CreatedInvitation{}, errors.New("pairing audit store is required")
	}
	now := service.currentTime()
	invite, err := identity.NewPairingInviteAt(localIdentity, displayName, ttl, requested, service.randomReader(), now)
	if err != nil {
		return CreatedInvitation{}, err
	}
	encoded, err := identity.EncodePairingInvite(invite)
	if err != nil {
		return CreatedInvitation{}, err
	}
	auditID, err := service.newAuditID()
	if err != nil {
		return CreatedInvitation{}, err
	}
	if err := service.Audit.Record(ctx, storage.AuditEvent{
		ID:        auditID,
		EventName: AuditInvitationCreated,
		DeviceID:  localIdentity.DeviceID,
		Severity:  "info",
		Metadata: map[string]string{
			"invite_id":              invite.InviteID,
			"expires_at":             invite.ExpiresAt.UTC().Format(time.RFC3339Nano),
			"requested_capabilities": capabilityList(requested),
		},
		OccurredAt: now,
	}); err != nil {
		return CreatedInvitation{}, fmt.Errorf("audit pairing invitation: %w", err)
	}
	return CreatedInvitation{Invite: invite, Encoded: encoded}, nil
}

func (service Service) InspectInvitation(encoded string) (identity.PairingInvite, error) {
	return identity.DecodePairingInviteAt(encoded, service.currentTime())
}

func (service Service) Accept(ctx context.Context, request AcceptRequest) (AcceptResult, error) {
	if ctx == nil {
		return AcceptResult{}, errors.New("pairing acceptance context is required")
	}
	if err := ctx.Err(); err != nil {
		return AcceptResult{}, err
	}
	if service.Pairings == nil {
		return AcceptResult{}, errors.New("pairing store is required")
	}
	if request.LocalDeviceID == "" {
		return AcceptResult{}, errors.New("local device ID is required")
	}
	now := service.currentTime()
	invite, err := identity.DecodePairingInviteAt(strings.TrimSpace(request.EncodedInvite), now)
	if err != nil {
		return AcceptResult{}, err
	}
	peer, err := identity.ConfirmPairingInvite(invite, request.ExpectedFingerprint, request.OneTimeCode, now)
	if err != nil {
		return AcceptResult{}, err
	}
	if peer.DeviceID == request.LocalDeviceID {
		return AcceptResult{}, errors.New("cannot pair a device with itself")
	}
	permissions, err := permissionsForPeer(peer.DeviceID, request.Grants)
	if err != nil {
		return AcceptResult{}, err
	}
	auditID, err := service.newAuditID()
	if err != nil {
		return AcceptResult{}, err
	}
	result, err := service.Pairings.Accept(ctx, storage.PairingAcceptance{
		InviteID: invite.InviteID,
		Device: storage.Device{
			ID: peer.DeviceID, DisplayName: peer.DisplayName, PublicKey: peer.PublicKey,
			Fingerprint: peer.Fingerprint, TrustState: storage.TrustTrusted,
		},
		Permissions: permissions,
		AuditEvent: storage.AuditEvent{
			ID: auditID, EventName: AuditPairingAccepted, DeviceID: request.LocalDeviceID,
			PeerDeviceID: peer.DeviceID, Severity: "info",
			Metadata: map[string]string{
				"invite_id": invite.InviteID,
				"grants":    grantSummary(request.Grants),
			},
			OccurredAt: now,
		},
		AcceptedAt: now,
	})
	if err != nil {
		return AcceptResult{}, fmt.Errorf("accept pairing invitation: %w", err)
	}
	return AcceptResult{Peer: peer, AlreadyAccepted: result.AlreadyAccepted}, nil
}

func (service Service) Revoke(ctx context.Context, localDeviceID, peerDeviceID core.DeviceID) (RevokeResult, error) {
	if ctx == nil {
		return RevokeResult{}, errors.New("pairing revocation context is required")
	}
	if err := ctx.Err(); err != nil {
		return RevokeResult{}, err
	}
	if service.Pairings == nil {
		return RevokeResult{}, errors.New("pairing store is required")
	}
	if localDeviceID == "" || peerDeviceID == "" {
		return RevokeResult{}, errors.New("local and peer device IDs are required")
	}
	if localDeviceID == peerDeviceID {
		return RevokeResult{}, errors.New("cannot revoke the local device through pairing management")
	}
	auditID, err := service.newAuditID()
	if err != nil {
		return RevokeResult{}, err
	}
	now := service.currentTime()
	result, err := service.Pairings.Revoke(ctx, storage.PairingRevocation{
		DeviceID: peerDeviceID,
		AuditEvent: storage.AuditEvent{
			ID: auditID, EventName: AuditPairingRevoked, DeviceID: localDeviceID,
			PeerDeviceID: peerDeviceID, Severity: "warning", OccurredAt: now,
		},
		RevokedAt: now,
	})
	if err != nil {
		return RevokeResult{}, fmt.Errorf("revoke paired device: %w", err)
	}
	return RevokeResult{AlreadyRevoked: result.AlreadyRevoked}, nil
}

func permissionsForPeer(peerDeviceID core.DeviceID, grants []Grant) ([]core.SharePermission, error) {
	seenShares := make(map[core.ShareID]bool, len(grants))
	permissions := make([]core.SharePermission, 0, len(grants))
	for _, grant := range grants {
		if grant.ShareID == "" {
			return nil, errors.New("pairing grant share ID is required")
		}
		if seenShares[grant.ShareID] {
			return nil, fmt.Errorf("duplicate pairing grant for share %s", grant.ShareID)
		}
		seenShares[grant.ShareID] = true
		if len(grant.Capabilities) == 0 {
			return nil, fmt.Errorf("pairing grant for share %s has no capabilities", grant.ShareID)
		}
		capabilities := make(map[core.Capability]bool, len(grant.Capabilities))
		for _, capability := range grant.Capabilities {
			if !core.IsShareCapability(capability) {
				return nil, fmt.Errorf("unsupported pairing capability %q", capability)
			}
			if capabilities[capability] {
				return nil, fmt.Errorf("duplicate pairing capability %q for share %s", capability, grant.ShareID)
			}
			capabilities[capability] = true
		}
		permissions = append(permissions, core.SharePermission{
			ShareID: grant.ShareID, DeviceID: peerDeviceID, Capabilities: capabilities, LANOnly: grant.LANOnly,
		})
	}
	return permissions, nil
}

func (service Service) currentTime() time.Time {
	if service.Now == nil {
		return time.Now().UTC()
	}
	return service.Now().UTC()
}

func (service Service) randomReader() io.Reader {
	if service.Random == nil {
		return rand.Reader
	}
	return service.Random
}

func (service Service) newAuditID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := io.ReadFull(service.randomReader(), buffer); err != nil {
		return "", fmt.Errorf("generate pairing audit ID: %w", err)
	}
	return "audit-" + hex.EncodeToString(buffer), nil
}

func capabilityList(capabilities []core.Capability) string {
	values := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		values = append(values, string(capability))
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func grantSummary(grants []Grant) string {
	values := make([]string, 0, len(grants))
	for _, grant := range grants {
		scope := "remote_allowed"
		if grant.LANOnly {
			scope = "lan_only"
		}
		values = append(values, string(grant.ShareID)+"="+capabilityList(grant.Capabilities)+"@"+scope)
	}
	sort.Strings(values)
	return strings.Join(values, ";")
}
