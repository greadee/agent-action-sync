package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrowserSessionBootstrapIsOneUseAndSessionCSRFCanRotate(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	entropy := make([]byte, browserTokenBytes*4)
	for index := range entropy {
		entropy[index] = byte(index + 1)
	}
	manager := NewBrowserSessionManager(BrowserSessionManagerOptions{Now: func() time.Time { return now }, Entropy: bytes.NewReader(entropy)})
	ticket, err := manager.Issue(context.Background())
	if err != nil || ticket.BootstrapToken == "" || ticket.ExpiresAt != now.Add(browserBootstrapTTL) {
		t.Fatalf("Issue = %+v, err=%v", ticket, err)
	}
	exchange, err := manager.Exchange(context.Background(), ticket.BootstrapToken)
	if err != nil || exchange.SessionToken == "" || exchange.CSRFToken == "" {
		t.Fatalf("Exchange = %+v, err=%v", exchange, err)
	}
	if _, err := manager.Exchange(context.Background(), ticket.BootstrapToken); err == nil {
		t.Fatal("bootstrap token was reusable")
	}
	request := httptest.NewRequest("GET", "http://127.0.0.1:47820/api/v1/status", nil)
	request.AddCookie(browserSessionCookie(exchange))
	if _, err := manager.Authenticate(request); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	request.Header.Set(browserCSRFHeader, exchange.CSRFToken)
	if err := manager.ValidateCSRF(request); err != nil {
		t.Fatalf("ValidateCSRF: %v", err)
	}
	rotated, expiresAt, err := manager.RotateCSRF(request)
	if err != nil || rotated == exchange.CSRFToken || expiresAt != exchange.ExpiresAt {
		t.Fatalf("RotateCSRF = %q, %v, err=%v", rotated, expiresAt, err)
	}
	if err := manager.ValidateCSRF(request); err == nil {
		t.Fatal("rotated CSRF token left the old token valid")
	}
}

func TestBrowserSessionExpiryAndCapacityAreBounded(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	entropy := make([]byte, browserTokenBytes*(maxBrowserBootstraps+1))
	for block := 0; block < maxBrowserBootstraps+1; block++ {
		for offset := 0; offset < browserTokenBytes; offset++ {
			entropy[block*browserTokenBytes+offset] = byte(block + 1)
		}
	}
	manager := NewBrowserSessionManager(BrowserSessionManagerOptions{Now: func() time.Time { return now }, Entropy: bytes.NewReader(entropy)})
	var first BrowserSessionTicket
	for index := 0; index < maxBrowserBootstraps; index++ {
		ticket, err := manager.Issue(context.Background())
		if err != nil {
			t.Fatalf("Issue %d: %v", index, err)
		}
		if index == 0 {
			first = ticket
		}
	}
	if _, err := manager.Issue(context.Background()); err == nil {
		t.Fatal("manager exceeded the pending bootstrap capacity")
	}
	now = now.Add(browserBootstrapTTL + time.Second)
	if _, err := manager.Exchange(context.Background(), first.BootstrapToken); err == nil {
		t.Fatal("expired bootstrap token was accepted")
	}
}

func browserSessionCookie(exchange BrowserSessionExchange) *http.Cookie {
	return &http.Cookie{Name: browserSessionCookieName, Value: exchange.SessionToken}
}
