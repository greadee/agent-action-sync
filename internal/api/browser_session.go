package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	browserSessionCookieName = "syncgate_browser_session"
	browserTokenBytes        = 32
	browserBootstrapTTL      = 2 * time.Minute
	browserSessionTTL        = 8 * time.Hour
	maxBrowserBootstraps     = 16
	maxBrowserSessions       = 8
)

var (
	errBrowserBootstrapInvalid = errors.New("browser bootstrap is invalid")
	errBrowserSessionInvalid   = errors.New("browser session is invalid")
	errBrowserSessionCapacity  = errors.New("browser session capacity reached")
)

type browserBootstrapRecord struct {
	ExpiresAt time.Time
	CreatedAt time.Time
}

type browserSessionRecord struct {
	CSRFDigest [sha256.Size]byte
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

type BrowserSessionManager struct {
	mu       sync.Mutex
	now      func() time.Time
	entropy  io.Reader
	tickets  map[[sha256.Size]byte]browserBootstrapRecord
	sessions map[[sha256.Size]byte]browserSessionRecord
}

type BrowserSessionManagerOptions struct {
	Now     func() time.Time
	Entropy io.Reader
}

type BrowserSessionTicket struct {
	BootstrapToken string
	ExpiresAt      time.Time
}

type BrowserSessionExchange struct {
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

func NewBrowserSessionManager(options BrowserSessionManagerOptions) *BrowserSessionManager {
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.Entropy == nil {
		options.Entropy = rand.Reader
	}
	return &BrowserSessionManager{
		now: options.Now, entropy: options.Entropy,
		tickets: map[[sha256.Size]byte]browserBootstrapRecord{}, sessions: map[[sha256.Size]byte]browserSessionRecord{},
	}
}

func (manager *BrowserSessionManager) Issue(ctx context.Context) (BrowserSessionTicket, error) {
	if manager == nil || ctx == nil {
		return BrowserSessionTicket{}, errBrowserSessionInvalid
	}
	if err := ctx.Err(); err != nil {
		return BrowserSessionTicket{}, err
	}
	token, err := manager.randomToken()
	if err != nil {
		return BrowserSessionTicket{}, err
	}
	now := manager.now().UTC()
	digest := sha256.Sum256([]byte(token))
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.pruneLocked(now)
	if len(manager.tickets) >= maxBrowserBootstraps {
		return BrowserSessionTicket{}, errBrowserSessionCapacity
	}
	expiresAt := now.Add(browserBootstrapTTL)
	manager.tickets[digest] = browserBootstrapRecord{CreatedAt: now, ExpiresAt: expiresAt}
	return BrowserSessionTicket{BootstrapToken: token, ExpiresAt: expiresAt}, nil
}

func (manager *BrowserSessionManager) Exchange(ctx context.Context, bootstrapToken string) (BrowserSessionExchange, error) {
	if manager == nil || ctx == nil || bootstrapToken == "" {
		return BrowserSessionExchange{}, errBrowserBootstrapInvalid
	}
	if err := ctx.Err(); err != nil {
		return BrowserSessionExchange{}, err
	}
	now := manager.now().UTC()
	bootstrapDigest := sha256.Sum256([]byte(bootstrapToken))
	manager.mu.Lock()
	manager.pruneLocked(now)
	bootstrap, ok := manager.tickets[bootstrapDigest]
	if !ok || !bootstrap.ExpiresAt.After(now) {
		manager.mu.Unlock()
		return BrowserSessionExchange{}, errBrowserBootstrapInvalid
	}
	if len(manager.sessions) >= maxBrowserSessions {
		manager.mu.Unlock()
		return BrowserSessionExchange{}, errBrowserSessionCapacity
	}
	delete(manager.tickets, bootstrapDigest)
	manager.mu.Unlock()

	sessionToken, err := manager.randomToken()
	if err != nil {
		return BrowserSessionExchange{}, err
	}
	csrfToken, err := manager.randomToken()
	if err != nil {
		return BrowserSessionExchange{}, err
	}
	sessionDigest := sha256.Sum256([]byte(sessionToken))
	csrfDigest := sha256.Sum256([]byte(csrfToken))
	expiresAt := now.Add(browserSessionTTL)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.pruneLocked(now)
	if len(manager.sessions) >= maxBrowserSessions {
		return BrowserSessionExchange{}, errBrowserSessionCapacity
	}
	manager.sessions[sessionDigest] = browserSessionRecord{CSRFDigest: csrfDigest, CreatedAt: now, ExpiresAt: expiresAt}
	return BrowserSessionExchange{SessionToken: sessionToken, CSRFToken: csrfToken, ExpiresAt: expiresAt}, nil
}

func (manager *BrowserSessionManager) Authenticate(request *http.Request) (context.Context, error) {
	if manager == nil || request == nil {
		return nil, errBrowserSessionInvalid
	}
	_, _, err := manager.session(request)
	if err != nil {
		return nil, err
	}
	return context.WithValue(request.Context(), adminAuthenticationContextKey{}, true), nil
}

func (manager *BrowserSessionManager) ValidateCSRF(request *http.Request) error {
	record, _, err := manager.session(request)
	if err != nil {
		return err
	}
	provided := request.Header.Get(browserCSRFHeader)
	providedDigest := sha256.Sum256([]byte(provided))
	if provided == "" || subtle.ConstantTimeCompare(record.CSRFDigest[:], providedDigest[:]) != 1 {
		return errBrowserSessionInvalid
	}
	return nil

}

func (manager *BrowserSessionManager) RotateCSRF(request *http.Request) (string, time.Time, error) {
	_, digest, err := manager.session(request)
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := manager.randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	csrfDigest := sha256.Sum256([]byte(token))
	now := manager.now().UTC()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.pruneLocked(now)
	record, ok := manager.sessions[digest]
	if !ok || !record.ExpiresAt.After(now) {
		return "", time.Time{}, errBrowserSessionInvalid
	}
	record.CSRFDigest = csrfDigest
	manager.sessions[digest] = record
	return token, record.ExpiresAt, nil
}

func (manager *BrowserSessionManager) session(request *http.Request) (browserSessionRecord, [sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	if manager == nil || request == nil {
		return browserSessionRecord{}, empty, errBrowserSessionInvalid
	}
	cookie, err := request.Cookie(browserSessionCookieName)
	if err != nil || cookie.Value == "" {
		return browserSessionRecord{}, empty, errBrowserSessionInvalid
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	now := manager.now().UTC()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.pruneLocked(now)
	record, ok := manager.sessions[digest]
	if !ok || !record.ExpiresAt.After(now) {
		return browserSessionRecord{}, empty, errBrowserSessionInvalid
	}
	return record, digest, nil
}

func (manager *BrowserSessionManager) pruneLocked(now time.Time) {
	for digest, ticket := range manager.tickets {
		if !ticket.ExpiresAt.After(now) {
			delete(manager.tickets, digest)
		}
	}
	for digest, session := range manager.sessions {
		if !session.ExpiresAt.After(now) {
			delete(manager.sessions, digest)
		}
	}
}

func (manager *BrowserSessionManager) randomToken() (string, error) {
	raw := make([]byte, browserTokenBytes)
	if _, err := io.ReadFull(manager.entropy, raw); err != nil {
		return "", err
	}
	defer clearCredential(raw)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func setBrowserSessionCookie(writer http.ResponseWriter, exchange BrowserSessionExchange) {
	http.SetCookie(writer, &http.Cookie{
		Name: browserSessionCookieName, Value: exchange.SessionToken, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Expires: exchange.ExpiresAt, MaxAge: int(browserSessionTTL / time.Second),
	})
}
