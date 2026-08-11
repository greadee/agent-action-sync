//go:build !windows

package identity

type unavailableCredentialBackend struct{}

func newPlatformCredentialBackend() credentialBackend { return unavailableCredentialBackend{} }

func (unavailableCredentialBackend) Read(string) ([]byte, error) {
	return nil, ErrCredentialStoreUnavailable
}
func (unavailableCredentialBackend) Write(string, string, []byte) error {
	return ErrCredentialStoreUnavailable
}
func (unavailableCredentialBackend) Delete(string) error {
	return ErrCredentialStoreUnavailable
}
