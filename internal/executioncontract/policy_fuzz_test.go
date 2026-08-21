package executioncontract

import (
	"errors"
	"testing"

	"syncgate/internal/project"
)

func FuzzAuthorizePathFailsClosed(f *testing.F) {
	f.Add("src/main.go", true)
	f.Add("src/blocked/key.txt", true)
	f.Add("../escape", false)
	f.Add("docs/design.md", false)

	f.Fuzz(func(t *testing.T, path string, write bool) {
		contract := mustBuild(t, contractFixture())
		capability := CapabilityInspect
		if write {
			capability = CapabilityWrite
		}
		first := AuthorizePath(contract, capability, path)
		second := AuthorizePath(contract, capability, path)
		if (first == nil) != (second == nil) {
			t.Fatalf("authorization was not deterministic for %q", path)
		}
		if first == nil {
			if err := project.ValidateProjectRelativePath(path); err != nil {
				t.Fatalf("authorized invalid path %q: %v", path, err)
			}
			for _, forbidden := range contract.Permissions.Forbidden {
				if within(path, forbidden) {
					t.Fatalf("authorized forbidden path %q under %q", path, forbidden)
				}
			}
		} else if !errors.Is(first, ErrPrivilegeEscalation) {
			t.Fatalf("unexpected authorization error for %q: %v", path, first)
		}
	})
}
