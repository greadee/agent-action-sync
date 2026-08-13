package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminAuthenticatorAcceptsValidBearerCredential(t *testing.T) {
	credential := testAdminCredential(0x19)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	called := false
	handler := authenticator.Authenticate(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		if !IsAdminAuthenticated(request.Context()) {
			t.Error("authenticated context marker is missing")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if !called || recorder.Code != http.StatusNoContent {
		t.Fatalf("valid credential response = %d, called = %t", recorder.Code, called)
	}
}

func TestAdminAuthenticatorRejectsMissingMalformedWrongAndQueryCredentials(t *testing.T) {
	credential := testAdminCredential(0x25)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	handler := authenticator.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("protected handler was called")
	}))
	tests := []struct {
		name    string
		headers []string
		target  string
	}{
		{name: "missing", target: "/api/v1/status"},
		{name: "wrong scheme", headers: []string{"Basic " + string(credential)}, target: "/api/v1/status"},
		{name: "missing token", headers: []string{"Bearer"}, target: "/api/v1/status"},
		{name: "extra token", headers: []string{"Bearer " + string(credential) + " extra"}, target: "/api/v1/status"},
		{name: "wrong token", headers: []string{"Bearer " + string(testAdminCredential(0x26))}, target: "/api/v1/status"},
		{name: "duplicate headers", headers: []string{"Bearer " + string(credential), "Bearer " + string(credential)}, target: "/api/v1/status"},
		{name: "query token", target: "/api/v1/status?access_token=" + string(credential)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request.Header.Set("X-Request-ID", "auth-test")
			for _, header := range test.headers {
				request.Header.Add("Authorization", header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", recorder.Code)
			}
			if recorder.Header().Get("WWW-Authenticate") != `Bearer realm="local-admin"` {
				t.Fatalf("challenge = %q", recorder.Header().Get("WWW-Authenticate"))
			}
			if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Request-ID") != "auth-test" {
				t.Fatalf("security headers = %#v", recorder.Header())
			}
			body := recorder.Body.String()
			if strings.Contains(body, string(credential)) || strings.Contains(body, "access_token") {
				t.Fatalf("credential leaked in response: %s", body)
			}
			if !strings.Contains(body, `"code":"unauthorized"`) {
				t.Fatalf("unstable authentication response: %s", body)
			}
		})
	}
}

func TestAdminAuthenticatorDoesNotRetainPlaintextCredential(t *testing.T) {
	credential := testAdminCredential(0x35)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	if strings.Contains(string(authenticator.digest[:]), string(credential)) {
		t.Fatal("authenticator retained plaintext credential")
	}
}

func TestAdminAuthenticatorLoadsSameCredentialAsLocalClient(t *testing.T) {
	credential := testAdminCredential(0x45)
	store := &memoryAdminCredentialStore{credential: credential}
	authenticator, err := NewAdminAuthenticatorFromStore(store)
	if err != nil {
		t.Fatalf("load authenticator: %v", err)
	}
	clientCredential, err := LoadAdminCredential(store)
	if err != nil {
		t.Fatalf("load client credential: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+string(clientCredential))
	recorder := httptest.NewRecorder()
	authenticator.Authenticate(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("shared credential status = %d", recorder.Code)
	}
}

func TestAdminAuthenticatorRejectsInvalidStoredCredential(t *testing.T) {
	store := &memoryAdminCredentialStore{credential: []byte("short")}
	if _, err := NewAdminAuthenticatorFromStore(store); err == nil {
		t.Fatal("expected malformed stored credential rejection")
	}
}
