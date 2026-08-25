package advancedfeatures

import "sort"

type GateStatus string

const GateDisabled GateStatus = "disabled"

type EvidenceRequirement struct {
	Code         string `json:"code"`
	MinimumCount int64  `json:"minimum_count"`
	Description  string `json:"description"`
}
type EvaluationRequirement struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}
type EvidenceGate struct {
	Feature          Feature                 `json:"feature"`
	Version          int64                   `json:"version"`
	Status           GateStatus              `json:"status"`
	Evidence         []EvidenceRequirement   `json:"evidence"`
	Evaluations      []EvaluationRequirement `json:"evaluations"`
	ApprovalRequired bool                    `json:"approval_required"`
}

// EvidenceGates returns enablement prerequisites, not evidence that any gate
// passed. All features remain disabled until a later release changes code and
// records a separate approval decision.
func EvidenceGates() []EvidenceGate {
	gates := []EvidenceGate{
		gate(FeatureSimilarity,
			[]EvidenceRequirement{{"approved_digest_corpus", 100, "representative, privacy-approved work-package and artifact digests"}, {"deletion_and_staleness_cases", 20, "revoked, deleted, and stale corpus cases"}},
			[]EvaluationRequirement{{"retrieval_quality", "versioned recall and precision targets on held-out approved queries"}, {"privacy_deletion", "deleted or unapproved material is absent and stale indexes fail closed"}}),
		gate(FeatureRetrospective,
			[]EvidenceRequirement{{"approved_source_sets", 50, "source-digest sets with explicit artifact approval"}, {"human_usefulness_reviews", 50, "independent usefulness and factuality reviews"}},
			[]EvaluationRequirement{{"provenance_factuality", "every claim remains attributable to approved source digests"}, {"privacy_retention", "consent, redaction, access, retention, and deletion controls pass"}, {"non_authority", "proposal cannot satisfy gates or publish without explicit authority approval"}}),
		gate(FeatureEstimator,
			[]EvidenceRequirement{{"complete_cohort_samples", 100, "reviewer-verified outcomes per supported cohort"}, {"missingness_cases", 30, "token, cost, and duration missingness profiles"}},
			[]EvaluationRequirement{{"calibration", "held-out duration, cost, and success intervals meet declared coverage"}, {"error_bounds", "versioned error thresholds and cohort drift alarms pass"}, {"fallback", "unsupported or undersampled cohorts return a declared fallback reason"}}),
		gate(FeatureCrew,
			[]EvidenceRequirement{{"reviewed_worker_outcomes", 100, "reviewer-verified outcomes across supported trades"}, {"cold_start_cases", 30, "new worker, trade, model, and provider cases"}},
			[]EvaluationRequirement{{"selection_quality", "held-out alternatives are useful without fabricating certainty"}, {"fairness_confounds", "provider, model, task difficulty, and opportunity confounds are reviewed"}, {"fallback", "missing advice requires explicit user selection or deterministic policy"}}),
		gate(FeatureDashboard,
			[]EvidenceRequirement{{"projection_rebuild_fixtures", 50, "portable histories with clean-rebuild expected read models"}, {"privacy_boundary_cases", 30, "secret, path, prompt, output, and artifact-byte exclusion cases"}},
			[]EvaluationRequirement{{"rebuild_parity", "read model and event timeline match accepted projection watermarks"}, {"bounded_load", "pagination, cancellation, staleness, and maximum response bounds pass"}}),
		gate(FeatureConflictEngine,
			[]EvidenceRequirement{{"protocol_adr", 1, "approved multi-writer authority, causality, and conflict protocol"}, {"conflict_corpus", 100, "concurrent edit, deletion, rename, replay, and stale-writer cases"}, {"recovery_drills", 20, "rollback, crash, partition, and restore drills"}},
			[]EvaluationRequirement{{"convergence", "property and replay tests converge without authority ambiguity"}, {"safety", "conflicts never auto-merge or publish without explicit review and rollback"}, {"security", "forged causality, replay, downgrade, and cross-project isolation tests pass"}}),
	}
	sort.Slice(gates, func(i, j int) bool { return gates[i].Feature < gates[j].Feature })
	return gates
}

func gate(feature Feature, evidence []EvidenceRequirement, evaluations []EvaluationRequirement) EvidenceGate {
	return EvidenceGate{Feature: feature, Version: ContractVersion, Status: GateDisabled, Evidence: evidence, Evaluations: evaluations, ApprovalRequired: true}
}
