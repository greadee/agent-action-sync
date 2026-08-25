package advancedfeatures

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDisabledSuiteReturnsNoFabricatedOutputAndExplicitFallbacks(t *testing.T) {
	ctx := context.Background()
	suite := DisabledSuite()
	project := ref(ReferenceProject, "project-one", "1")
	work := ref(ReferenceWorkPackage, "wp-one", "2")
	artifact := ref(ReferenceArtifact, "artifact:one", "3")
	approvedWork := ApprovedDigest{Reference: work, ApprovalID: "approval:work"}
	approvedArtifact := ApprovedDigest{Reference: artifact, ApprovalID: "approval:artifact"}

	similarity, err := suite.Similarity.Search(ctx, SimilarityRequest{Project: project, Query: approvedWork, ApprovedCorpus: []ApprovedDigest{approvedArtifact}, Limit: 10})
	if err != nil || similarity.Availability.Available || similarity.Availability.FallbackAction != FallbackDeterministicPolicy || similarity.Index != nil || len(similarity.Hits) != 0 {
		t.Fatalf("similarity=%+v err=%v", similarity, err)
	}

	retrospective, err := suite.Retrospective.Propose(ctx, RetrospectiveRequest{Project: project, WorkPackage: work, ApprovedSources: []ApprovedDigest{approvedWork, approvedArtifact}})
	if err != nil || retrospective.Availability.Available || retrospective.Availability.FallbackAction != FallbackNoGeneratedArtifact || retrospective.Proposal != nil {
		t.Fatalf("retrospective=%+v err=%v", retrospective, err)
	}

	estimate, err := suite.Estimator.Estimate(ctx, EstimateRequest{Project: project, Contract: ref(ReferenceContract, "contract:one", "4"), Trade: ref(ReferenceTrade, "trade:one", "5"), Worker: ref(ReferenceWorker, "worker:one", "6"), Runtime: ref(ReferenceRuntime, "runtime:one", "7"), Provider: ref(ReferenceProvider, "provider:one", "8"), Model: ref(ReferenceModel, "model:one", "9"), Node: ref(ReferenceNode, "node:one", "a"), TelemetryWatermark: hash("b")})
	if err != nil || estimate.Availability.Available || estimate.Confidence != "none" || estimate.SampleCount != 0 || estimate.Version != ContractVersion || estimate.DurationMilliseconds != nil || estimate.CostMicros != nil || estimate.SuccessProbability != nil || estimate.FallbackReason != FallbackFeatureDisabled {
		t.Fatalf("estimate=%+v err=%v", estimate, err)
	}

	crew, err := suite.Crew.Recommend(ctx, CrewRequest{Project: project, WorkPackage: work, Trade: ref(ReferenceTrade, "trade:one", "5"), Candidates: []StableReference{ref(ReferenceWorker, "worker:one", "6")}})
	if err != nil || crew.Availability.Available || crew.Availability.FallbackAction != FallbackExplicitUserSelection || len(crew.Alternatives) != 0 {
		t.Fatalf("crew=%+v err=%v", crew, err)
	}

	dashboardRequest := DashboardRequest{Project: project, ProjectionWatermark: hash("c"), Limit: 25}
	dashboard, err := suite.Dashboard.Read(ctx, dashboardRequest)
	if err != nil || dashboard.Availability.Available || dashboard.Projection != nil || len(dashboard.Cards) != 0 {
		t.Fatalf("dashboard=%+v err=%v", dashboard, err)
	}
	timeline, err := suite.Dashboard.Timeline(ctx, dashboardRequest, "cursor:one")
	if err != nil || timeline.Availability.Available || len(timeline.Items) != 0 || timeline.NextCursor != "" {
		t.Fatalf("timeline=%+v err=%v", timeline, err)
	}

	conflict, err := suite.Conflicts.Preview(ctx, ConflictPreviewRequest{Project: project, Base: ref(ReferenceProjection, "projection:base", "d"), Left: ref(ReferenceProjection, "projection:left", "e"), Right: ref(ReferenceProjection, "projection:right", "f"), ConflictSetDigest: hash("1")})
	if err != nil || conflict.Availability.Available || conflict.AutomaticMerge || conflict.ConflictCount != 0 || conflict.PreviewDigest != "" || conflict.Availability.FallbackAction != FallbackManualReview {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestDisabledContractsRejectUnsafeIdentityAndHonorCancellation(t *testing.T) {
	project := ref(ReferenceProject, `C:\Users\owner\project`, "1")
	_, err := (DisabledCrewRecommender{}).Recommend(context.Background(), CrewRequest{Project: project})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("absolute path error=%v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (DisabledSimilarityIndex{}).Search(canceled, SimilarityRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestConflictInterfaceCannotMergeOrPublish(t *testing.T) {
	typeOf := reflect.TypeOf((*ConflictEngine)(nil)).Elem()
	if typeOf.NumMethod() != 1 || typeOf.Method(0).Name != "Preview" {
		t.Fatalf("conflict methods changed: %v", typeOf)
	}
	for _, forbidden := range []string{"Merge", "Publish", "Accept", "Schedule", "Permission"} {
		if _, exists := typeOf.MethodByName(forbidden); exists {
			t.Fatalf("conflict interface exposes %s", forbidden)
		}
	}
}

func ref(kind ReferenceKind, id, seed string) StableReference {
	return StableReference{Kind: kind, ID: id, Version: 1, Digest: hash(seed)}
}
func hash(seed string) string { return strings.Repeat(seed, 64) }
