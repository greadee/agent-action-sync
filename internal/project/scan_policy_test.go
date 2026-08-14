package project

import "testing"

func TestNewProjectScanPolicyKeepsOnlyMandatoryProjectSafetyExclusions(t *testing.T) {
	policy := NewProjectScanPolicy([]string{" cache/** ", ".git", "cache/**"})
	want := []string{
		".agent-project/local", ".agent-project/local/**", ".git", ".git/**", "cache/**",
	}
	if len(policy.EffectiveIgnorePatterns) != len(want) {
		t.Fatalf("effective patterns = %#v", policy.EffectiveIgnorePatterns)
	}
	for index, pattern := range want {
		if policy.EffectiveIgnorePatterns[index] != pattern {
			t.Fatalf("effective patterns = %#v, want %#v", policy.EffectiveIgnorePatterns, want)
		}
	}
	for _, pattern := range policy.EffectiveIgnorePatterns {
		if pattern == "node_modules/**" || pattern == "__pycache__/**" {
			t.Fatalf("generic language cache exclusion was added: %#v", policy.EffectiveIgnorePatterns)
		}
	}
}
