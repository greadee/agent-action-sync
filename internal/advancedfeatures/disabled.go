package advancedfeatures

import "context"

type DisabledSimilarityIndex struct{}
type DisabledRetrospectiveGenerator struct{}
type DisabledOutcomeEstimator struct{}
type DisabledCrewRecommender struct{}
type DisabledDashboardReader struct{}
type DisabledConflictEngine struct{}

func DisabledSuite() Suite {
	return Suite{
		Similarity: DisabledSimilarityIndex{}, Retrospective: DisabledRetrospectiveGenerator{},
		Estimator: DisabledOutcomeEstimator{}, Crew: DisabledCrewRecommender{},
		Dashboard: DisabledDashboardReader{}, Conflicts: DisabledConflictEngine{},
	}
}

func (DisabledSimilarityIndex) Search(ctx context.Context, request SimilarityRequest) (SimilarityResult, error) {
	if err := validateContext(ctx); err != nil {
		return SimilarityResult{}, err
	}
	if !validReference(request.Project, ReferenceProject) || !validApproved([]ApprovedDigest{request.Query}, ReferenceWorkPackage, ReferenceArtifact) || !validApproved(request.ApprovedCorpus, ReferenceWorkPackage, ReferenceArtifact) || request.Limit < 1 || request.Limit > MaxResults {
		return SimilarityResult{}, ErrInvalidRequest
	}
	return SimilarityResult{Availability: unavailable(FallbackDeterministicPolicy)}, nil
}

func (DisabledRetrospectiveGenerator) Propose(ctx context.Context, request RetrospectiveRequest) (RetrospectiveResult, error) {
	if err := validateContext(ctx); err != nil {
		return RetrospectiveResult{}, err
	}
	if !validReference(request.Project, ReferenceProject) || !validReference(request.WorkPackage, ReferenceWorkPackage) || !validApproved(request.ApprovedSources, ReferenceWorkPackage, ReferenceArtifact) {
		return RetrospectiveResult{}, ErrInvalidRequest
	}
	return RetrospectiveResult{Availability: unavailable(FallbackNoGeneratedArtifact)}, nil
}

func (DisabledOutcomeEstimator) Estimate(ctx context.Context, request EstimateRequest) (Estimate, error) {
	if err := validateContext(ctx); err != nil {
		return Estimate{}, err
	}
	if !validReference(request.Project, ReferenceProject) || !validReference(request.Contract, ReferenceContract) || !validReference(request.Trade, ReferenceTrade) || !validReference(request.Worker, ReferenceWorker) ||
		!validReference(request.Runtime, ReferenceRuntime) || !validReference(request.Provider, ReferenceProvider) || !validReference(request.Model, ReferenceModel) || !validReference(request.Node, ReferenceNode) || !digest(request.TelemetryWatermark) {
		return Estimate{}, ErrInvalidRequest
	}
	return Estimate{Availability: unavailable(FallbackDeterministicPolicy), Confidence: "none", SampleCount: 0, Version: ContractVersion, FallbackReason: FallbackFeatureDisabled}, nil
}

func (DisabledCrewRecommender) Recommend(ctx context.Context, request CrewRequest) (CrewRecommendation, error) {
	if err := validateContext(ctx); err != nil {
		return CrewRecommendation{}, err
	}
	if !validReference(request.Project, ReferenceProject) || !validReference(request.WorkPackage, ReferenceWorkPackage) || !validReference(request.Trade, ReferenceTrade) || len(request.Candidates) == 0 || len(request.Candidates) > MaxReferences {
		return CrewRecommendation{}, ErrInvalidRequest
	}
	seen := map[string]bool{}
	for _, candidate := range request.Candidates {
		if !validReference(candidate, ReferenceWorker) || seen[candidate.ID] {
			return CrewRecommendation{}, ErrInvalidRequest
		}
		seen[candidate.ID] = true
	}
	return CrewRecommendation{Availability: unavailable(FallbackExplicitUserSelection)}, nil
}

func (DisabledDashboardReader) Read(ctx context.Context, request DashboardRequest) (DashboardSnapshot, error) {
	if err := validateDashboardRequest(ctx, request); err != nil {
		return DashboardSnapshot{}, err
	}
	return DashboardSnapshot{Availability: unavailable(FallbackDeterministicPolicy)}, nil
}

func (DisabledDashboardReader) Timeline(ctx context.Context, request DashboardRequest, cursor string) (TimelinePage, error) {
	if err := validateDashboardRequest(ctx, request); err != nil {
		return TimelinePage{}, err
	}
	if cursor != "" && !namespaced(cursor, "cursor:") {
		return TimelinePage{}, ErrInvalidRequest
	}
	return TimelinePage{Availability: unavailable(FallbackDeterministicPolicy)}, nil
}

func (DisabledConflictEngine) Preview(ctx context.Context, request ConflictPreviewRequest) (ConflictPreview, error) {
	if err := validateContext(ctx); err != nil {
		return ConflictPreview{}, err
	}
	if !validReference(request.Project, ReferenceProject) || !validReference(request.Base, ReferenceProjection) || !validReference(request.Left, ReferenceProjection) || !validReference(request.Right, ReferenceProjection) || !digest(request.ConflictSetDigest) {
		return ConflictPreview{}, ErrInvalidRequest
	}
	return ConflictPreview{Availability: unavailable(FallbackManualReview), AutomaticMerge: false}, nil
}

func validateDashboardRequest(ctx context.Context, request DashboardRequest) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if !validReference(request.Project, ReferenceProject) || !digest(request.ProjectionWatermark) || request.Limit < 1 || request.Limit > MaxResults {
		return ErrInvalidRequest
	}
	return nil
}

func unavailable(action FallbackAction) Availability {
	return Availability{Available: false, Reason: FallbackFeatureDisabled, FallbackAction: action}
}

var (
	_ SimilarityIndex        = DisabledSimilarityIndex{}
	_ RetrospectiveGenerator = DisabledRetrospectiveGenerator{}
	_ OutcomeEstimator       = DisabledOutcomeEstimator{}
	_ CrewRecommender        = DisabledCrewRecommender{}
	_ DashboardReader        = DisabledDashboardReader{}
	_ ConflictEngine         = DisabledConflictEngine{}
)
