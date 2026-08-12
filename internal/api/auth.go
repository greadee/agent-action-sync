package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

type AdminAuthenticator struct {
	digest [sha256.Size]byte
}

type adminAuthenticationContextKey struct{}

func NewAdminAuthenticator(credential []byte) (*AdminAuthenticator, error) {
	if err := validateAdminCredential(credential); err != nil {
		return nil, err
	}
	return &AdminAuthenticator{digest: sha256.Sum256(credential)}, nil
}

func NewAdminAuthenticatorFromStore(store AdminCredentialStore) (*AdminAuthenticator, error) {
	credential, err := LoadAdminCredential(store)
	if err != nil {
		return nil, err
	}
	defer clearCredential(credential)
	return NewAdminAuthenticator(credential)
}

func (authenticator *AdminAuthenticator) Authenticate(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if authenticator == nil {
			writeAuthenticationError(writer, request)
			return
		}
		provided, ok := bearerCredential(request)
		if !ok {
			writeAuthenticationError(writer, request)
			return
		}
		providedDigest := sha256.Sum256([]byte(provided))
		if subtle.ConstantTimeCompare(authenticator.digest[:], providedDigest[:]) != 1 {
			writeAuthenticationError(writer, request)
			return
		}
		context := context.WithValue(request.Context(), adminAuthenticationContextKey{}, true)
		next.ServeHTTP(writer, request.WithContext(context))
	})
}

func IsAdminAuthenticated(ctx context.Context) bool {
	authenticated, _ := ctx.Value(adminAuthenticationContextKey{}).(bool)
	return authenticated
}

func bearerCredential(request *http.Request) (string, bool) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func writeAuthenticationError(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("WWW-Authenticate", `Bearer realm="local-admin"`)
	writeError(writer, request, errUnauthorized)
}
