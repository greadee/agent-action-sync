package project

import (
	"path/filepath"
	"strings"
)

// RequiredProjectIgnorePatterns are safety boundaries for a project share.
// They deliberately cover only local Agent Project state and Git metadata.
// Language caches and build directories remain an operator choice because a
// generic project share may intentionally contain them.
var RequiredProjectIgnorePatterns = []string{
	LocalDirectory,
	LocalDirectory + "/**",
	".git",
	".git/**",
}

// ProjectScanPolicy separates mandatory project safety exclusions from
// configured share exclusions. Callers pass EffectiveIgnorePatterns to the
// sync scanner and can expose both sources in diagnostics or future migration
// tooling without changing existing share configuration semantics.
type ProjectScanPolicy struct {
	MandatoryIgnorePatterns  []string
	ConfiguredIgnorePatterns []string
	EffectiveIgnorePatterns  []string
}

func NewProjectScanPolicy(configured []string) ProjectScanPolicy {
	mandatory := append([]string{}, RequiredProjectIgnorePatterns...)
	custom := normalizeIgnorePatterns(configured)
	effective := append([]string{}, mandatory...)
	seen := make(map[string]struct{}, len(effective)+len(custom))
	for _, pattern := range effective {
		seen[pattern] = struct{}{}
	}
	for _, pattern := range custom {
		if _, exists := seen[pattern]; exists {
			continue
		}
		seen[pattern] = struct{}{}
		effective = append(effective, pattern)
	}
	return ProjectScanPolicy{
		MandatoryIgnorePatterns:  mandatory,
		ConfiguredIgnorePatterns: custom,
		EffectiveIgnorePatterns:  effective,
	}
}

func normalizeIgnorePatterns(patterns []string) []string {
	result := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if _, exists := seen[pattern]; exists {
			continue
		}
		seen[pattern] = struct{}{}
		result = append(result, pattern)
	}
	return result
}
