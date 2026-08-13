package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/pairing"
	"syncgate/internal/storage"
)

func TestPairingAcceptanceAndRevocationEndpointsAreExplicitIdempotentAndReplaySafe(t *testing.T) {
	ctx := context.Background()
	store := apiPairingStore(t)
	local := apiPairingIdentity(t, 31)
	peer := apiPairingIdentity(t, 32)
	shareID := core.ShareID("drop")
	seedAPIPairingState(t, store, local, shareID)
	now := time.Unix(30_000, 0).UTC()
	invite := apiPairingInvite(t, peer, now)
	encoded, err := identity.EncodePairingInvite(invite)
	if err != nil {
		t.Fatalf("encode pairing invite: %v", err)
	}
	coordinator := &PairingCoordinator{
		Service:         pairing.Service{Pairings: store.Pairings(), Now: func() time.Time { return now }},
		CurrentIdentity: func() (identity.DeviceIdentity, string, error) { return local, "Local", nil },
	}
	administration, err := NewAdministrationService(AdministrationServiceOptions{
		Queries: store, Ready: func() bool { return true }, Pairing: coordinator,
	})
	if err != nil {
		t.Fatalf("create administration service: %v", err)
	}
	handler := NewAdminV1Handler(administration)
	requestBody := acceptanceJSON(t, AcceptanceRequest{
		Invitation: encoded, ExpectedFingerprint: peer.Fingerprint, OneTimeCode: invite.OneTimeCode,
		Grants: []PairingGrantRequest{{ShareID: string(shareID), Capabilities: []string{string(core.CapabilityUpload)}}},
	})

	acceptedRecorder := postPairingJSON(t, handler, "/api/v1/pairing/acceptances", requestBody)
	if acceptedRecorder.Code != http.StatusOK {
		t.Fatalf("accept status = %d, body=%s", acceptedRecorder.Code, acceptedRecorder.Body.String())
	}
	var accepted Acceptance
	if err := json.Unmarshal(acceptedRecorder.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode acceptance response: %v", err)
	}
	if !accepted.Accepted || accepted.AlreadyAccepted || accepted.DeviceID != string(peer.DeviceID) || accepted.Fingerprint != peer.Fingerprint {
		t.Fatalf("acceptance response = %+v", accepted)
	}
	if acceptedRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("acceptance cache control = %q", acceptedRecorder.Header().Get("Cache-Control"))
	}
	for _, secret := range []string{encoded, invite.OneTimeCode} {
		if strings.Contains(acceptedRecorder.Body.String(), secret) {
			t.Fatalf("acceptance response disclosed secret %q", secret)
		}
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityUpload, false); err != nil {
		t.Fatalf("explicit upload grant was denied: %v", err)
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityUpload, true); err == nil {
		t.Fatal("omitted lan_only did not default to LAN-only")
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityRead, false); err == nil {
		t.Fatal("acceptance granted an unrequested capability")
	}

	repeatRequest := AcceptanceRequest{
		Invitation: encoded, ExpectedFingerprint: peer.Fingerprint, OneTimeCode: invite.OneTimeCode,
		Grants: []PairingGrantRequest{{ShareID: string(shareID), Capabilities: []string{string(core.CapabilityRead)}, LANOnly: boolPointer(false)}},
	}
	repeatedRecorder := postPairingJSON(t, handler, "/api/v1/pairing/acceptances", acceptanceJSON(t, repeatRequest))
	var repeated Acceptance
	if repeatedRecorder.Code != http.StatusOK || json.Unmarshal(repeatedRecorder.Body.Bytes(), &repeated) != nil || !repeated.AlreadyAccepted {
		t.Fatalf("repeat acceptance = %d, %s", repeatedRecorder.Code, repeatedRecorder.Body.String())
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityRead, false); err == nil {
		t.Fatal("duplicate acceptance changed the original grant")
	}

	revokedRecorder := postPairingJSON(t, handler, "/api/v1/devices/"+string(peer.DeviceID)+"/revocation", `{}`)
	var revoked Revocation
	if revokedRecorder.Code != http.StatusOK || json.Unmarshal(revokedRecorder.Body.Bytes(), &revoked) != nil || !revoked.Revoked || revoked.AlreadyRevoked {
		t.Fatalf("revocation = %d, %s", revokedRecorder.Code, revokedRecorder.Body.String())
	}
	replayedRecorder := postPairingJSON(t, handler, "/api/v1/pairing/acceptances", requestBody)
	var replayed Acceptance
	if replayedRecorder.Code != http.StatusOK || json.Unmarshal(replayedRecorder.Body.Bytes(), &replayed) != nil || !replayed.AlreadyAccepted {
		t.Fatalf("revoked replay = %d, %s", replayedRecorder.Code, replayedRecorder.Body.String())
	}
	if err := store.Shares().Authorize(ctx, peer.DeviceID, shareID, core.CapabilityUpload, false); err == nil {
		t.Fatal("revoked invitation replay restored access")
	}
	repeatedRevocationRecorder := postPairingJSON(t, handler, "/api/v1/devices/"+string(peer.DeviceID)+"/revocation", `{}`)
	var repeatedRevocation Revocation
	if repeatedRevocationRecorder.Code != http.StatusOK || json.Unmarshal(repeatedRevocationRecorder.Body.Bytes(), &repeatedRevocation) != nil || !repeatedRevocation.AlreadyRevoked {
		t.Fatalf("repeat revocation = %d, %s", repeatedRevocationRecorder.Code, repeatedRevocationRecorder.Body.String())
	}
}

func TestPairingAcceptanceEndpointRejectsConfirmationTamperingAndOverbroadRequests(t *testing.T) {
	ctx := context.Background()
	store := apiPairingStore(t)
	local := apiPairingIdentity(t, 33)
	peer := apiPairingIdentity(t, 34)
	shareID := core.ShareID("drop")
	seedAPIPairingState(t, store, local, shareID)
	now := time.Unix(40_000, 0).UTC()
	invite := apiPairingInvite(t, peer, now)
	encoded, _ := identity.EncodePairingInvite(invite)
	coordinator := &PairingCoordinator{
		Service:         pairing.Service{Pairings: store.Pairings(), Now: func() time.Time { return now }},
		CurrentIdentity: func() (identity.DeviceIdentity, string, error) { return local, "Local", nil },
	}
	handler := NewPairingInvitationHandler(coordinator)
	valid := AcceptanceRequest{
		Invitation: encoded, ExpectedFingerprint: peer.Fingerprint, OneTimeCode: invite.OneTimeCode,
		Grants: []PairingGrantRequest{{ShareID: string(shareID), Capabilities: []string{string(core.CapabilityRead)}}},
	}

	tests := []struct {
		name string
		body string
	}{
		{name: "caller local identity", body: strings.TrimSuffix(acceptanceJSON(t, valid), "}") + `,"local_device_id":"attacker"}`},
		{name: "wrong fingerprint", body: acceptanceJSON(t, withAcceptanceFingerprint(valid, "WRONG"))},
		{name: "wrong code", body: acceptanceJSON(t, withAcceptanceCode(valid, "WRONG-CODE"))},
		{name: "duplicate share grant", body: acceptanceJSON(t, withAcceptanceGrants(valid,
			[]PairingGrantRequest{{ShareID: string(shareID), Capabilities: []string{"read"}}, {ShareID: string(shareID), Capabilities: []string{"upload"}}}))},
		{name: "duplicate capability", body: acceptanceJSON(t, withAcceptanceGrants(valid,
			[]PairingGrantRequest{{ShareID: string(shareID), Capabilities: []string{"read", "read"}}}))},
		{name: "unsupported capability", body: acceptanceJSON(t, withAcceptanceGrants(valid,
			[]PairingGrantRequest{{ShareID: string(shareID), Capabilities: []string{"admin"}}}))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := postPairingJSON(t, handler, "/api/v1/pairing/acceptances", test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), encoded) || strings.Contains(response.Body.String(), invite.OneTimeCode) {
				t.Fatal("failure response disclosed pairing secrets")
			}
		})
	}

	tampered := valid
	tampered.Invitation = encoded[:len(encoded)-1] + "x"
	response := postPairingJSON(t, handler, "/api/v1/pairing/acceptances", acceptanceJSON(t, tampered))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "pairing_invitation_invalid") {
		t.Fatalf("tampered response = %d, %s", response.Code, response.Body.String())
	}

	selfInvite := apiPairingInvite(t, local, now)
	selfEncoded, _ := identity.EncodePairingInvite(selfInvite)
	selfRequest := valid
	selfRequest.Invitation, selfRequest.ExpectedFingerprint, selfRequest.OneTimeCode = selfEncoded, local.Fingerprint, selfInvite.OneTimeCode
	response = postPairingJSON(t, handler, "/api/v1/pairing/acceptances", acceptanceJSON(t, selfRequest))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("self-pairing response = %d, %s", response.Code, response.Body.String())
	}

	expiredCoordinator := &PairingCoordinator{
		Service:         pairing.Service{Pairings: store.Pairings(), Now: func() time.Time { return now.Add(2 * time.Minute) }},
		CurrentIdentity: coordinator.CurrentIdentity,
	}
	response = postPairingJSON(t, NewPairingInvitationHandler(expiredCoordinator), "/api/v1/pairing/acceptances", acceptanceJSON(t, valid))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "pairing_invitation_expired") {
		t.Fatalf("expired response = %d, %s", response.Code, response.Body.String())
	}
	if _, err := store.Devices().GetDevice(ctx, peer.DeviceID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("failed acceptance changed peer trust: %v", err)
	}
	events, err := store.Audit().ListRecent(ctx, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("failed acceptance audit state = %+v, err=%v", events, err)
	}
}

func TestPairingAcceptanceSerializesConcurrentDuplicateRequests(t *testing.T) {
	store := apiPairingStore(t)
	local := apiPairingIdentity(t, 35)
	peer := apiPairingIdentity(t, 36)
	shareID := core.ShareID("drop")
	seedAPIPairingState(t, store, local, shareID)
	now := time.Unix(50_000, 0).UTC()
	invite := apiPairingInvite(t, peer, now)
	encoded, _ := identity.EncodePairingInvite(invite)
	coordinator := &PairingCoordinator{
		Service:         pairing.Service{Pairings: store.Pairings(), Now: func() time.Time { return now }},
		CurrentIdentity: func() (identity.DeviceIdentity, string, error) { return local, "Local", nil },
	}
	request := AcceptanceRequest{
		Invitation: encoded, ExpectedFingerprint: peer.Fingerprint, OneTimeCode: invite.OneTimeCode,
		Grants: []PairingGrantRequest{{ShareID: string(shareID), Capabilities: []string{"read"}}},
	}

	results := make(chan Acceptance, 2)
	errors := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			result, err := coordinator.AcceptPairingInvitation(context.Background(), request)
			results <- result
			errors <- err
		}()
	}
	start.Done()
	alreadyAccepted := 0
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatalf("concurrent acceptance: %v", err)
		}
		if result := <-results; result.AlreadyAccepted {
			alreadyAccepted++
		}
	}
	if alreadyAccepted != 1 {
		t.Fatalf("already accepted responses = %d, want 1", alreadyAccepted)
	}
	events, err := store.Audit().ListRecent(context.Background(), 10)
	if err != nil || len(events) != 1 || events[0].EventName != pairing.AuditPairingAccepted {
		t.Fatalf("concurrent acceptance audit events = %+v, err=%v", events, err)
	}
}

func seedAPIPairingState(t *testing.T, store interface {
	Devices() storage.DeviceStore
	Shares() storage.ShareStore
}, local identity.DeviceIdentity, shareID core.ShareID) {
	t.Helper()
	ctx := context.Background()
	if err := store.Devices().TrustDevice(ctx, storage.Device{
		ID: local.DeviceID, DisplayName: "Local", PublicKey: local.PublicKey,
		Fingerprint: local.Fingerprint, TrustState: storage.TrustTrusted,
	}); err != nil {
		t.Fatalf("trust local device: %v", err)
	}
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: shareID, Name: "Drop", RootPath: t.TempDir(), Mode: storage.ShareUploadOnly}); err != nil {
		t.Fatalf("save pairing share: %v", err)
	}
}

func apiPairingInvite(t *testing.T, peer identity.DeviceIdentity, now time.Time) identity.PairingInvite {
	t.Helper()
	invite, err := identity.NewPairingInviteAt(peer, "Peer Laptop", time.Minute, []core.Capability{core.CapabilityRead}, bytes.NewReader(bytes.Repeat([]byte{37}, identity.PairingCodeByteCount)), now)
	if err != nil {
		t.Fatalf("create pairing invite: %v", err)
	}
	return invite
}

func acceptanceJSON(t *testing.T, request AcceptanceRequest) string {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal acceptance request: %v", err)
	}
	return string(raw)
}

func boolPointer(value bool) *bool { return &value }

func withAcceptanceFingerprint(request AcceptanceRequest, value string) AcceptanceRequest {
	request.ExpectedFingerprint = value
	return request
}

func withAcceptanceCode(request AcceptanceRequest, value string) AcceptanceRequest {
	request.OneTimeCode = value
	return request
}

func withAcceptanceGrants(request AcceptanceRequest, value []PairingGrantRequest) AcceptanceRequest {
	request.Grants = value
	return request
}
