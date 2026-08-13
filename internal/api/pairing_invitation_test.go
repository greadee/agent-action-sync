package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/pairing"
	"syncgate/internal/storage/sqlite"
)

func TestPairingInvitationEndpointsCreateAndInspectVerifiedMetadata(t *testing.T) {
	store := apiPairingStore(t)
	local := apiPairingIdentity(t, 21)
	now := time.Unix(10_000, 0).UTC()
	coordinator := &PairingCoordinator{
		Service: pairing.Service{
			Audit: store.Audit(), Now: func() time.Time { return now },
			Random: bytes.NewReader(bytes.Repeat([]byte{22}, 64)),
		},
		CurrentIdentity: func() (identity.DeviceIdentity, string, error) {
			return local, "Local Laptop", nil
		},
	}
	handler := NewPairingInvitationHandler(coordinator)

	createdRecorder := postPairingJSON(t, handler, "/api/v1/pairing/invitations", `{"ttl_seconds":60,"capabilities":["read"]}`)
	if createdRecorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", createdRecorder.Code, createdRecorder.Body.String())
	}
	if createdRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create cache control = %q", createdRecorder.Header().Get("Cache-Control"))
	}
	var created InvitationDTO
	if err := json.Unmarshal(createdRecorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode creation response: %v", err)
	}
	if created.Invitation == "" || created.OneTimeCode == "" || created.Fingerprint != local.Fingerprint {
		t.Fatalf("creation response = %+v", created)
	}
	if strings.Contains(created.Invitation, created.OneTimeCode) {
		t.Fatal("encoded invitation disclosed its one-time code")
	}

	inspectionRecorder := postPairingJSON(t, handler, "/api/v1/pairing/inspect", `{"invitation":"`+created.Invitation+`"}`)
	if inspectionRecorder.Code != http.StatusOK {
		t.Fatalf("inspect status = %d, body=%s", inspectionRecorder.Code, inspectionRecorder.Body.String())
	}
	var inspection InvitationInspection
	if err := json.Unmarshal(inspectionRecorder.Body.Bytes(), &inspection); err != nil {
		t.Fatalf("decode inspection response: %v", err)
	}
	if !inspection.Valid || inspection.DeviceID != string(local.DeviceID) || inspection.DeviceName != "Local Laptop" || inspection.Fingerprint != local.Fingerprint {
		t.Fatalf("inspection response = %+v", inspection)
	}
	if len(inspection.Capabilities) != 1 || inspection.Capabilities[0] != string(core.CapabilityRead) {
		t.Fatalf("inspection capabilities = %v", inspection.Capabilities)
	}
	for _, secret := range []string{created.OneTimeCode, "code_hash", "signature", "public_key"} {
		if strings.Contains(inspectionRecorder.Body.String(), secret) {
			t.Fatalf("inspection response disclosed %q", secret)
		}
	}

	events, err := store.Audit().ListRecent(context.Background(), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("audit events = %+v, err=%v", events, err)
	}
	for key, value := range events[0].Metadata {
		if strings.Contains(key, "code") || strings.Contains(value, created.OneTimeCode) || strings.Contains(value, created.Invitation) {
			t.Fatalf("audit metadata disclosed invitation secret: %s=%q", key, value)
		}
	}
}

func TestPairingInvitationEndpointsRejectInvalidAndTamperedInput(t *testing.T) {
	store := apiPairingStore(t)
	local := apiPairingIdentity(t, 23)
	now := time.Unix(20_000, 0).UTC()
	coordinator := &PairingCoordinator{
		Service:         pairing.Service{Audit: store.Audit(), Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{24}, 64))},
		CurrentIdentity: func() (identity.DeviceIdentity, string, error) { return local, "Local", nil },
	}
	handler := NewPairingInvitationHandler(coordinator)

	for name, body := range map[string]string{
		"zero ttl":               `{"ttl_seconds":0}`,
		"excessive ttl":          `{"ttl_seconds":86401}`,
		"unsupported capability": `{"ttl_seconds":60,"capabilities":["admin"]}`,
		"duplicate capability":   `{"ttl_seconds":60,"capabilities":["read","read"]}`,
		"caller identity":        `{"ttl_seconds":60,"local_device_id":"attacker"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := postPairingJSON(t, handler, "/api/v1/pairing/invitations", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
		})
	}

	created, err := coordinator.CreatePairingInvitation(context.Background(), InvitationRequest{TTLSeconds: 60})
	if err != nil {
		t.Fatalf("CreatePairingInvitation: %v", err)
	}
	tampered := created.Invitation[:len(created.Invitation)-1] + "x"
	response := postPairingJSON(t, handler, "/api/v1/pairing/inspect", `{"invitation":"`+tampered+`"}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "pairing_invitation_invalid") {
		t.Fatalf("tampered response = %d, %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), tampered) || strings.Contains(response.Body.String(), created.OneTimeCode) {
		t.Fatal("tampered invitation error disclosed request secrets")
	}
}

func apiPairingStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "syncgate.db"))
	if err != nil {
		t.Fatalf("open pairing store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate pairing store: %v", err)
	}
	return store
}

func apiPairingIdentity(t *testing.T, value byte) identity.DeviceIdentity {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{value}, ed25519.SeedSize))
	deviceIdentity, err := identity.FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("build pairing identity: %v", err)
	}
	return deviceIdentity
}

func postPairingJSON(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
