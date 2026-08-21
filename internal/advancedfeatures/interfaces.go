package advancedfeatures

import "context"

type SimilarityRequest struct {
	Project        StableReference  `json:"project"`
	Query          ApprovedDigest   `json:"query"`
	ApprovedCorpus []ApprovedDigest `json:"approved_corpus"`
	Limit          int              `json:"limit"`
}
type SimilarityHit struct {
	Reference       StableReference `json:"reference"`
	Rank            int             `json:"rank"`
	ExplanationCode string          `json:"explanation_code"`
}
type SimilarityResult struct {
	Availability Availability     `json:"availability"`
	Index        *StableReference `json:"index,omitempty"`
	Hits         []SimilarityHit  `json:"hits,omitempty"`
}
type SimilarityIndex interface {
	Search(context.Context, SimilarityRequest) (SimilarityResult, error)
}

type RetrospectiveRequest struct {
	Project         StableReference  `json:"project"`
	WorkPackage     StableReference  `json:"work_package"`
	ApprovedSources []ApprovedDigest `json:"approved_sources"`
}
type ProposedArtifact struct {
	ProposalID       string          `json:"proposal_id"`
	Generator        StableReference `json:"generator"`
	SourceDigest     string          `json:"source_digest"`
	MediaType        string          `json:"media_type"`
	ContentDigest    string          `json:"content_digest"`
	NonAuthoritative bool            `json:"non_authoritative"`
}
type RetrospectiveResult struct {
	Availability Availability      `json:"availability"`
	Proposal     *ProposedArtifact `json:"proposal,omitempty"`
}
type RetrospectiveGenerator interface {
	Propose(context.Context, RetrospectiveRequest) (RetrospectiveResult, error)
}

type EstimateRequest struct {
	Project            StableReference `json:"project"`
	Contract           StableReference `json:"contract"`
	Trade              StableReference `json:"trade"`
	Worker             StableReference `json:"worker"`
	Runtime            StableReference `json:"runtime"`
	Provider           StableReference `json:"provider"`
	Model              StableReference `json:"model"`
	Node               StableReference `json:"node"`
	TelemetryWatermark string          `json:"telemetry_watermark"`
}
type Int64Range struct {
	Low  int64 `json:"low"`
	High int64 `json:"high"`
}
type ProbabilityRange struct {
	LowPPM  int64 `json:"low_ppm"`
	HighPPM int64 `json:"high_ppm"`
}
type Estimate struct {
	Availability         Availability      `json:"availability"`
	Estimator            *StableReference  `json:"estimator,omitempty"`
	DurationMilliseconds *Int64Range       `json:"duration_milliseconds,omitempty"`
	CostMicros           *Int64Range       `json:"cost_micros,omitempty"`
	SuccessProbability   *ProbabilityRange `json:"success_probability,omitempty"`
	Confidence           string            `json:"confidence"`
	SampleCount          int64             `json:"sample_count"`
	Version              int64             `json:"version"`
	FallbackReason       FallbackReason    `json:"fallback_reason,omitempty"`
}
type OutcomeEstimator interface {
	Estimate(context.Context, EstimateRequest) (Estimate, error)
}

type CrewRequest struct {
	Project     StableReference   `json:"project"`
	WorkPackage StableReference   `json:"work_package"`
	Trade       StableReference   `json:"trade"`
	Candidates  []StableReference `json:"candidates"`
}
type CrewAlternative struct {
	Worker           StableReference `json:"worker"`
	ExplanationCodes []string        `json:"explanation_codes"`
}
type CrewRecommendation struct {
	Availability Availability      `json:"availability"`
	Recommender  *StableReference  `json:"recommender,omitempty"`
	Alternatives []CrewAlternative `json:"alternatives,omitempty"`
}
type CrewRecommender interface {
	Recommend(context.Context, CrewRequest) (CrewRecommendation, error)
}

type DashboardRequest struct {
	Project             StableReference `json:"project"`
	ProjectionWatermark string          `json:"projection_watermark"`
	Limit               int             `json:"limit"`
}
type DashboardSnapshot struct {
	Availability        Availability     `json:"availability"`
	Projection          *StableReference `json:"projection,omitempty"`
	ProjectionWatermark string           `json:"projection_watermark,omitempty"`
	Cards               []DashboardCard  `json:"cards,omitempty"`
}
type DashboardCard struct {
	MetricName        string `json:"metric_name"`
	DefinitionVersion int64  `json:"definition_version"`
	ValueDigest       string `json:"value_digest"`
}
type TimelinePage struct {
	Availability Availability   `json:"availability"`
	Items        []TimelineItem `json:"items,omitempty"`
	NextCursor   string         `json:"next_cursor,omitempty"`
}
type TimelineItem struct {
	EventID      string `json:"event_id"`
	EventType    string `json:"event_type"`
	RecordDigest string `json:"record_digest"`
}
type DashboardReader interface {
	Read(context.Context, DashboardRequest) (DashboardSnapshot, error)
	Timeline(context.Context, DashboardRequest, string) (TimelinePage, error)
}

type ConflictPreviewRequest struct {
	Project           StableReference `json:"project"`
	Base              StableReference `json:"base"`
	Left              StableReference `json:"left"`
	Right             StableReference `json:"right"`
	ConflictSetDigest string          `json:"conflict_set_digest"`
}
type ConflictPreview struct {
	Availability   Availability `json:"availability"`
	ConflictCount  int64        `json:"conflict_count"`
	PreviewDigest  string       `json:"preview_digest,omitempty"`
	AutomaticMerge bool         `json:"automatic_merge"`
}

// ConflictEngine deliberately exposes preview only. Canonical merge and
// publication are outside this seam and require a separate authority ADR.
type ConflictEngine interface {
	Preview(context.Context, ConflictPreviewRequest) (ConflictPreview, error)
}

type Suite struct {
	Similarity    SimilarityIndex
	Retrospective RetrospectiveGenerator
	Estimator     OutcomeEstimator
	Crew          CrewRecommender
	Dashboard     DashboardReader
	Conflicts     ConflictEngine
}
