// Package advancedfeatures defines provider-neutral, non-authoritative seams
// for evidence-dependent features. Every implementation shipped in the setup
// sprint is disabled and this package has no canonical writer or scheduler
// dependency.
package advancedfeatures

import (
	"context"
	"errors"
	"strings"
)

const (
	ContractVersion = 1
	MaxReferences   = 256
	MaxResults      = 100
)

var ErrInvalidRequest = errors.New("invalid advanced-feature request")

type Feature string

const (
	FeatureSimilarity     Feature = "similarity_index"
	FeatureRetrospective  Feature = "retrospective_generator"
	FeatureEstimator      Feature = "outcome_estimator"
	FeatureCrew           Feature = "crew_recommender"
	FeatureDashboard      Feature = "dashboard_read_model"
	FeatureConflictEngine Feature = "multi_writer_conflict_engine"
)

type FallbackReason string

const (
	FallbackFeatureDisabled      FallbackReason = "feature_disabled"
	FallbackInsufficientEvidence FallbackReason = "insufficient_evidence"
	FallbackUnsupportedCohort    FallbackReason = "unsupported_cohort"
	FallbackStaleEvidence        FallbackReason = "stale_evidence"
)

type FallbackAction string

const (
	FallbackExplicitUserSelection FallbackAction = "explicit_user_selection"
	FallbackDeterministicPolicy   FallbackAction = "deterministic_policy"
	FallbackManualReview          FallbackAction = "manual_review"
	FallbackNoGeneratedArtifact   FallbackAction = "no_generated_artifact"
)

type Availability struct {
	Available      bool           `json:"available"`
	Reason         FallbackReason `json:"reason,omitempty"`
	FallbackAction FallbackAction `json:"fallback_action,omitempty"`
}

type ReferenceKind string

const (
	ReferenceProject     ReferenceKind = "project"
	ReferenceTask        ReferenceKind = "task"
	ReferenceWorkPackage ReferenceKind = "work_package"
	ReferenceArtifact    ReferenceKind = "artifact"
	ReferenceContract    ReferenceKind = "contract"
	ReferenceTrade       ReferenceKind = "trade"
	ReferenceWorker      ReferenceKind = "worker"
	ReferenceRuntime     ReferenceKind = "runtime"
	ReferenceProvider    ReferenceKind = "provider"
	ReferenceModel       ReferenceKind = "model"
	ReferenceNode        ReferenceKind = "node"
	ReferenceAdapter     ReferenceKind = "adapter"
	ReferenceProjection  ReferenceKind = "projection"
)

// StableReference is the only cross-slice identity accepted by these seams.
// It carries no provider payload, path, prompt, transcript, or artifact bytes.
type StableReference struct {
	Kind    ReferenceKind `json:"kind"`
	ID      string        `json:"id"`
	Version int64         `json:"version"`
	Digest  string        `json:"digest"`
}

type ApprovedDigest struct {
	Reference  StableReference `json:"reference"`
	ApprovalID string          `json:"approval_id"`
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	return ctx.Err()
}

func validReference(value StableReference, kinds ...ReferenceKind) bool {
	if value.Version < 1 || !identifier(value.ID) || !digest(value.Digest) {
		return false
	}
	for _, kind := range kinds {
		if value.Kind == kind && strings.HasPrefix(value.ID, referencePrefix(kind)) {
			return true
		}
	}
	return false
}

func validApproved(values []ApprovedDigest, kinds ...ReferenceKind) bool {
	if len(values) == 0 || len(values) > MaxReferences {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		key := string(value.Reference.Kind) + ":" + value.Reference.ID
		if !validReference(value.Reference, kinds...) || !namespaced(value.ApprovalID, "approval:") || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func referencePrefix(kind ReferenceKind) string {
	switch kind {
	case ReferenceProject:
		return ""
	case ReferenceTask:
		return "task:"
	case ReferenceWorkPackage:
		return ""
	case ReferenceArtifact:
		return ""
	case ReferenceContract:
		return "contract:"
	case ReferenceTrade:
		return "trade:"
	case ReferenceWorker:
		return "worker:"
	case ReferenceRuntime:
		return "runtime:"
	case ReferenceProvider:
		return "provider:"
	case ReferenceModel:
		return "model:"
	case ReferenceNode:
		return "node:"
	case ReferenceAdapter:
		return "adapter:"
	case ReferenceProjection:
		return "projection:"
	default:
		return ""
	}
}

func namespaced(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) > len(prefix) && identifier(value)
}
func digest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
func identifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == ':' || character == '-'
		if !valid || index == 0 && !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}
