//go:build !windows

package api

type unavailableAdminCredentialBackend struct{}

func newAdminCredentialBackend() adminCredentialBackend { return unavailableAdminCredentialBackend{} }

func (unavailableAdminCredentialBackend) Read(string) ([]byte, error) {
	return nil, ErrAdminCredentialStoreUnavailable
}

func (unavailableAdminCredentialBackend) Write(string, string, []byte) error {
	return ErrAdminCredentialStoreUnavailable
}

func (unavailableAdminCredentialBackend) Delete(string) error {
	return ErrAdminCredentialStoreUnavailable
}
