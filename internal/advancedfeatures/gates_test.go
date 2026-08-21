package advancedfeatures

import "testing"

func TestEveryAdvancedFeatureHasDisabledEvidenceAndEvaluationGate(t *testing.T) {
	want := map[Feature]bool{FeatureSimilarity: true, FeatureRetrospective: true, FeatureEstimator: true, FeatureCrew: true, FeatureDashboard: true, FeatureConflictEngine: true}
	gates := EvidenceGates()
	if len(gates) != len(want) {
		t.Fatalf("gate count=%d", len(gates))
	}
	for _, gate := range gates {
		if !want[gate.Feature] || gate.Version != ContractVersion || gate.Status != GateDisabled || !gate.ApprovalRequired || len(gate.Evidence) == 0 || len(gate.Evaluations) == 0 {
			t.Fatalf("invalid gate=%+v", gate)
		}
		delete(want, gate.Feature)
		seen := map[string]bool{}
		for _, evidence := range gate.Evidence {
			if evidence.Code == "" || evidence.MinimumCount < 1 || evidence.Description == "" || seen[evidence.Code] {
				t.Fatalf("invalid evidence=%+v", evidence)
			}
			seen[evidence.Code] = true
		}
		for _, evaluation := range gate.Evaluations {
			if evaluation.Code == "" || evaluation.Description == "" || seen[evaluation.Code] {
				t.Fatalf("invalid evaluation=%+v", evaluation)
			}
			seen[evaluation.Code] = true
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing gates=%v", want)
	}
}
