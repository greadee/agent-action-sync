package project

import (
	"errors"
	"strings"
	"testing"
)

func FuzzValidateProjectRelativePath(f *testing.F) {
	for _, seed := range []string{
		"workspace/file.txt", ".agent-project/manifest.json", `workspace\file.txt`,
		"../secret", `C:\Users\owner\secret.txt`, "workspace/CON", "workspace/name.",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		err := ValidateProjectRelativePath(raw)
		if err != nil {
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("unclassifiable path error for %q: %v", raw, err)
			}
			return
		}
		if raw == "" || strings.Contains(raw, `\`) || strings.HasPrefix(raw, "/") || strings.Contains(raw, "/../") {
			t.Fatalf("unsafe canonical path accepted: %q", raw)
		}
	})
}

func FuzzPathBearingIdentifier(f *testing.F) {
	for _, seed := range []string{"WP-001", "wp.case", "CON", "../escape", "", strings.Repeat("a", MaxIdentifierBytes+1)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, identifier string) {
		relativePath, err := WorkPackageDefinitionRelativePath(identifier)
		if err != nil {
			return
		}
		if err := ValidateProjectRelativePath(relativePath); err != nil {
			t.Fatalf("constructed path is invalid: %q: %v", relativePath, err)
		}
		if relativePath != strings.ToLower(relativePath) {
			t.Fatalf("path key is not case folded: %q", relativePath)
		}
	})
}
