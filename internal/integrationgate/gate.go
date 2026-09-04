// Package integrationgate owns authority-side result collection, deterministic
// gates, review evidence, and explicit human acceptance. It never merges code.
package integrationgate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/resultintake"
	"syncgate/internal/runtimecontract"
	"syncgate/internal/storage"
	"syncgate/internal/workhistory"
	"syncgate/internal/workspace"
)

const (
	PreviewGateID     = "gate:integration-preview"
	MaxCollectedBytes = 256 << 20
	MaxHandoffBytes   = 1 << 20
	DecisionApprove   = "approve"
	DecisionReject    = "reject"
)

var (
	ErrInvalidRequest = errors.New("invalid integration gate request")
	ErrGateFailed     = errors.New("integration gate failed")
	ErrStaleResult    = errors.New("stale integration result")
	ErrHumanRequired  = errors.New("human integration decision required")
)

type Control interface {
	GetAssignment(context.Context, string) (storage.OrchestrationSnapshot, error)
	Transition(context.Context, orchestration.TransitionRequest) (storage.OrchestrationWriteResult, error)
	RecordGateStatus(context.Context, orchestration.GateStatusRequest) (storage.RegistryWriteResult, error)
	RecordOperatorDecision(context.Context, orchestration.OperatorDecisionRequest) (storage.RegistryWriteResult, error)
}

type RuntimeResolver interface {
	ResolveRuntime(executioncontract.BindingReference) (runtimecontract.Adapter, error)
}
type ResultSource interface {
	GetEnvelope(context.Context, string) ([]byte, error)
}

type Content struct {
	Name      string
	MediaType string
	Bytes     []byte
}
type ContentSource interface {
	GetContent(context.Context, resultintake.ContentReference) (Content, error)
}

type WorkspaceInspector interface {
	InspectChanges(context.Context, string, executioncontract.Contract) (workspace.ChangeManifest, error)
	PreviewIntegration(context.Context, string, string) (workspace.IntegrationPreview, error)
}

type TestCommand struct {
	GateID        string
	GateVersion   int64
	GateDigest    string
	CommandID     string
	CommandDigest string
}
type TestResult struct {
	Outcome              project.TestOutcome
	ExitCode             int64
	DurationMilliseconds int64
	EvidenceID           string
	EvidenceDigest       string
}
type TestRunner interface {
	RunAuthorized(context.Context, executioncontract.Contract, TestCommand) (TestResult, error)
}

type ReviewResult struct {
	Outcome    project.ReviewOutcome `json:"outcome"`
	ReviewerID string                `json:"reviewer_id"`
	Summary    string                `json:"summary,omitempty"`
}
type Reviewer interface {
	Review(context.Context, executioncontract.Contract, IntegrationSummary) (ReviewResult, error)
}
type HistoryRootResolver interface{ HistoryRoot(string) (string, error) }

type Service struct {
	Contracts          storage.ExecutionContractStore
	Intake             resultintake.Service
	Control            Control
	History            *workhistory.Service
	HistoryRoots       HistoryRootResolver
	Runtimes           RuntimeResolver
	Results            ResultSource
	Contents           ContentSource
	Workspaces         WorkspaceInspector
	Tests              TestRunner
	Reviewer           Reviewer
	TestPlans          map[string]TestCommand
	AllowedBinaryPaths []string
}

type EvaluateRequest struct {
	AssignmentID  string
	AttemptID     string
	Assignment    resultintake.AssignmentReference
	FencingDigest string
	ActorID       string
	CollectDigest string
}

type GateEvidence struct {
	GateID               string              `json:"gate_id"`
	GateVersion          int64               `json:"gate_version"`
	GateDigest           string              `json:"gate_digest"`
	CommandID            string              `json:"command_id,omitempty"`
	CommandDigest        string              `json:"command_digest,omitempty"`
	Outcome              project.TestOutcome `json:"outcome,omitempty"`
	ExitCode             *int64              `json:"exit_code,omitempty"`
	DurationMilliseconds int64               `json:"duration_milliseconds,omitempty"`
	EvidenceID           string              `json:"evidence_id,omitempty"`
	EvidenceDigest       string              `json:"evidence_digest,omitempty"`
}

type ArtifactSummary struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	MediaType  string `json:"media_type"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
}

// IntegrationSummary is the only presentation shape returned to an API. It
// cannot represent local roots, runtime sessions, provider data, or raw bytes.
type IntegrationSummary struct {
	AssignmentID     string                  `json:"assignment_id"`
	AttemptID        string                  `json:"attempt_id"`
	ResultID         string                  `json:"result_id"`
	ProjectID        string                  `json:"project_id"`
	WorkPackageID    string                  `json:"work_package_id"`
	ExecutionID      string                  `json:"execution_id"`
	BaseCommit       string                  `json:"base_commit"`
	CurrentCommit    string                  `json:"current_commit"`
	HeadCommit       string                  `json:"head_commit"`
	Files            []workspace.ChangedFile `json:"files"`
	ManifestDigest   string                  `json:"manifest_digest"`
	PreviewDigest    string                  `json:"preview_digest"`
	Tests            []GateEvidence          `json:"tests"`
	Review           *ReviewResult           `json:"review,omitempty"`
	Artifacts        []ArtifactSummary       `json:"artifacts,omitempty"`
	Limitations      []string                `json:"limitations,omitempty"`
	UnresolvedIssues []string                `json:"unresolved_issues,omitempty"`
	EvidenceAt       time.Time               `json:"evidence_at"`
	ReadyForDecision bool                    `json:"ready_for_decision"`
	Digest           string                  `json:"digest"`
}

type DecisionRequest struct {
	AssignmentID  string
	AttemptID     string
	FencingDigest string
	ActorID       string
	Decision      string
	ReasonCode    string
	Summary       IntegrationSummary
}

type handoffCandidate struct {
	HandoffID                 string               `json:"handoff_id"`
	CompletedWork             []string             `json:"completed_work"`
	Decisions                 []string             `json:"decisions,omitempty"`
	Tests                     []project.TestResult `json:"tests,omitempty"`
	Limitations               []string             `json:"limitations,omitempty"`
	UnresolvedIssues          []string             `json:"unresolved_issues,omitempty"`
	Assumptions               []string             `json:"assumptions,omitempty"`
	FollowUpWork              []string             `json:"follow_up_work,omitempty"`
	ReviewRequirements        []string             `json:"review_requirements,omitempty"`
	IntegrationConsiderations []string             `json:"integration_considerations,omitempty"`
	Confidence                project.Confidence   `json:"confidence"`
	FailureConditions         []string             `json:"failure_conditions,omitempty"`
}

func (service Service) Evaluate(ctx context.Context, request EvaluateRequest) (IntegrationSummary, error) {
	if err := service.validate(); err != nil || ctx == nil || request.AssignmentID == "" || request.AttemptID == "" || request.ActorID == "" || !validDigest(request.FencingDigest) || !validDigest(request.CollectDigest) {
		return IntegrationSummary{}, ErrInvalidRequest
	}
	snapshot, contract, err := service.loadAuthority(ctx, request.AssignmentID, request.AttemptID)
	if err != nil {
		return IntegrationSummary{}, err
	}
	if snapshot.Attempt.State != storage.AssignmentCollecting && snapshot.Attempt.State != storage.AssignmentAwaitingGates {
		return IntegrationSummary{}, ErrStaleResult
	}
	if snapshot.Resources == nil || snapshot.Resources.WorkspaceID == "" || snapshot.Resources.RuntimeSessionID == "" || snapshot.Lease == nil || snapshot.Lease.State != storage.LeaseActive || snapshot.Lease.FencingDigest != request.FencingDigest {
		return IntegrationSummary{}, ErrStaleResult
	}
	adapter, err := service.Runtimes.ResolveRuntime(contract.Runtime)
	if err != nil {
		return IntegrationSummary{}, err
	}
	collected, err := adapter.CollectResult(ctx, runtimecontract.ActionRequest{SessionID: snapshot.Resources.RuntimeSessionID, IdempotencyKeyDigest: request.CollectDigest})
	if err != nil {
		return IntegrationSummary{}, err
	}
	rawEnvelope, err := service.Results.GetEnvelope(ctx, collected.ResultID)
	if err != nil {
		return IntegrationSummary{}, fmt.Errorf("%w: result envelope reference", ErrGateFailed)
	}
	envelope, _, err := resultintake.DecodeEnvelope(rawEnvelope)
	if err != nil || envelope.ResultID != collected.ResultID || envelope.Digest != collected.EnvelopeDigest || envelope.ClaimedOutcome != collected.ClaimedOutcome {
		return IntegrationSummary{}, fmt.Errorf("%w: runtime result claim", ErrGateFailed)
	}
	decision, err := service.Intake.IntakeResult(ctx, resultintake.Request{EnvelopeJSON: rawEnvelope, ExpectedAssignment: request.Assignment, ExpectedWorkspaceID: snapshot.Resources.WorkspaceID, ExpectedProjectID: contract.ProjectID, ExpectedExecutionID: contract.ExecutionID, DecidedBy: request.ActorID})
	if err != nil || !decision.AcceptedForIntake {
		return IntegrationSummary{}, fmt.Errorf("%w: authority intake rejected result", ErrGateFailed)
	}
	root, err := service.HistoryRoots.HistoryRoot(contract.ProjectID)
	if err != nil {
		return IntegrationSummary{}, err
	}
	base := service.metadata(filepath.Clean(root), contract, envelope.CreatedAt, request.ActorID)
	if err := service.startHistory(ctx, base, contract); err != nil {
		return IntegrationSummary{}, err
	}
	manifest, err := service.Workspaces.InspectChanges(ctx, snapshot.Resources.WorkspaceID, contract)
	if err != nil {
		return IntegrationSummary{}, service.fail(ctx, request, base, contract, "change_manifest_rejected", err)
	}
	if err := service.validateManifest(manifest); err != nil {
		return IntegrationSummary{}, service.fail(ctx, request, base, contract, "change_manifest_rejected", err)
	}
	preview, err := service.Workspaces.PreviewIntegration(ctx, snapshot.Resources.WorkspaceID, manifest.BaseCommit)
	if err != nil || preview.StaleBase || preview.HasConflicts || preview.HeadCommit != manifest.HeadCommit {
		return IntegrationSummary{}, service.fail(ctx, request, base, contract, "integration_preview_failed", ErrGateFailed)
	}
	handoff, artifacts, err := service.verifyReferencedContent(ctx, envelope)
	if err != nil {
		return IntegrationSummary{}, service.fail(ctx, request, base, contract, "content_reference_rejected", err)
	}
	tests, err := service.runGates(ctx, request, base, contract)
	if err != nil {
		return IntegrationSummary{}, service.fail(ctx, request, base, contract, "deterministic_gate_failed", err)
	}
	if err := service.completeHistory(ctx, base, contract, manifest, handoff, artifacts); err != nil {
		return IntegrationSummary{}, err
	}
	if snapshot.Attempt.State == storage.AssignmentCollecting {
		if _, err := service.Control.Transition(ctx, service.transition(request, snapshot, storage.AssignmentAwaitingGates, "", "awaiting-gates")); err != nil {
			return IntegrationSummary{}, err
		}
	}
	summary := IntegrationSummary{AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, ResultID: envelope.ResultID, ProjectID: contract.ProjectID, WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID, BaseCommit: manifest.BaseCommit, CurrentCommit: preview.CurrentCommit, HeadCommit: manifest.HeadCommit, Files: append([]workspace.ChangedFile(nil), manifest.Files...), ManifestDigest: manifest.Digest, PreviewDigest: preview.Digest, Tests: tests, Artifacts: artifacts, EvidenceAt: envelope.CreatedAt, ReadyForDecision: true}
	if handoff != nil {
		summary.Limitations = safeList(handoff.Limitations)
		summary.UnresolvedIssues = safeList(handoff.UnresolvedIssues)
	}
	if service.Reviewer != nil {
		review, reviewErr := service.Reviewer.Review(ctx, contract, summary)
		review.Summary = safeText(review.Summary)
		if reviewErr != nil || review.Outcome != project.ReviewApproved {
			if reviewErr == nil {
				_ = service.recordReview(ctx, base, contract, review, "automated-review")
			}
			service.recordReviewGateStatuses(ctx, request.AssignmentID, request.AttemptID, request.ActorID, contract, storage.GateFailed, "evidence:review-output", "review_changes_requested")
			return IntegrationSummary{}, service.fail(ctx, request, base, contract, "review_changes_requested", ErrGateFailed)
		}
		if err := service.recordReview(ctx, base, contract, review, "automated-review"); err != nil {
			return IntegrationSummary{}, err
		}
		if err := service.recordReviewGateStatuses(ctx, request.AssignmentID, request.AttemptID, request.ActorID, contract, storage.GatePending, "evidence:review-output", "human_review_pending"); err != nil {
			return IntegrationSummary{}, err
		}
		summary.Review = &review
	}
	summary.Digest = summaryDigest(summary)
	if _, err := service.Control.RecordGateStatus(ctx, orchestration.GateStatusRequest{AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, GateID: PreviewGateID, GateVersion: 1, GateDigest: summary.Digest, Status: storage.GatePending, EvidenceID: "evidence:integration-preview", ReasonCode: "human_decision_pending", AuditID: derivedID("audit:", summary.Digest, "preview"), ActorID: request.ActorID}); err != nil {
		return IntegrationSummary{}, err
	}
	return summary, ErrHumanRequired
}

func (service Service) Decide(ctx context.Context, request DecisionRequest) (IntegrationSummary, error) {
	if err := service.validate(); err != nil || ctx == nil || (request.Decision != DecisionApprove && request.Decision != DecisionReject) || request.ActorID == "" || request.Summary.Digest == "" || summaryDigest(request.Summary) != request.Summary.Digest || !validDigest(request.FencingDigest) {
		return IntegrationSummary{}, ErrInvalidRequest
	}
	snapshot, contract, err := service.loadAuthority(ctx, request.AssignmentID, request.AttemptID)
	if err != nil || snapshot.Lease == nil || snapshot.Lease.FencingDigest != request.FencingDigest {
		return IntegrationSummary{}, ErrStaleResult
	}
	decisionDigest := hash([]byte(request.Decision + "\x00" + request.ReasonCode + "\x00" + request.Summary.Digest + "\x00" + request.ActorID))
	if snapshot.Attempt.State == storage.AssignmentAccepted && request.Decision == DecisionApprove {
		previewGate, ok := gateByID(snapshot.Gates, PreviewGateID)
		if ok && previewGate.Status == storage.GateSatisfied && previewGate.GateDigest == request.Summary.Digest && hasDecision(snapshot.Decisions, DecisionApprove, decisionDigest) {
			return request.Summary, nil
		}
		return IntegrationSummary{}, ErrStaleResult
	}
	if snapshot.Attempt.State != storage.AssignmentAwaitingGates || snapshot.Resources == nil {
		return IntegrationSummary{}, ErrStaleResult
	}
	if request.Summary.AssignmentID != request.AssignmentID || request.Summary.AttemptID != request.AttemptID || request.Summary.ProjectID != contract.ProjectID || request.Summary.WorkPackageID != contract.WorkPackageID || request.Summary.EvidenceAt.IsZero() || !request.Summary.ReadyForDecision {
		return IntegrationSummary{}, ErrInvalidRequest
	}
	previewGate, ok := gateByID(snapshot.Gates, PreviewGateID)
	if !ok || previewGate.GateDigest != request.Summary.Digest || previewGate.Status != storage.GatePending {
		return IntegrationSummary{}, ErrStaleResult
	}
	manifest, err := service.Workspaces.InspectChanges(ctx, snapshot.Resources.WorkspaceID, contract)
	if err != nil || manifest.Digest != request.Summary.ManifestDigest || manifest.BaseCommit != request.Summary.BaseCommit || manifest.HeadCommit != request.Summary.HeadCommit {
		return IntegrationSummary{}, ErrStaleResult
	}
	preview, err := service.Workspaces.PreviewIntegration(ctx, snapshot.Resources.WorkspaceID, manifest.BaseCommit)
	if err != nil || preview.Digest != request.Summary.PreviewDigest || preview.StaleBase || preview.HasConflicts {
		return IntegrationSummary{}, ErrStaleResult
	}
	for _, gate := range contract.RequiredGates {
		if humanGate(gate.GateID) {
			continue
		}
		status, exists := gateByID(snapshot.Gates, gate.GateID)
		if !exists || status.Status != storage.GateSatisfied || status.GateDigest != gate.Digest || status.GateVersion != gate.Version {
			return IntegrationSummary{}, ErrGateFailed
		}
	}
	if _, err := service.Control.RecordOperatorDecision(ctx, orchestration.OperatorDecisionRequest{DecisionID: derivedID("decision:", request.Summary.Digest, request.ActorID), AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, Decision: request.Decision, ReasonCode: reason(request.ReasonCode, "integration_decision"), ActorID: request.ActorID, IdempotencyDigest: decisionDigest, AuditID: derivedID("audit:", request.Summary.Digest, "decision")}); err != nil {
		return IntegrationSummary{}, err
	}
	root, err := service.HistoryRoots.HistoryRoot(contract.ProjectID)
	if err != nil {
		return IntegrationSummary{}, err
	}
	base := service.metadata(filepath.Clean(root), contract, request.Summary.EvidenceAt, request.ActorID)
	gateState := storage.GateFailed
	if request.Decision == DecisionApprove {
		gateState = storage.GateSatisfied
	}
	if _, err := service.Control.RecordGateStatus(ctx, orchestration.GateStatusRequest{AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, GateID: PreviewGateID, GateVersion: 1, GateDigest: request.Summary.Digest, Status: gateState, EvidenceID: "evidence:integration-preview", ReasonCode: reason(request.ReasonCode, request.Decision), AuditID: derivedID("audit:", request.Summary.Digest, "gate-decision"), ActorID: request.ActorID}); err != nil {
		return IntegrationSummary{}, err
	}
	if err := service.recordReviewGateStatuses(ctx, request.AssignmentID, request.AttemptID, request.ActorID, contract, gateState, "evidence:human-review", reason(request.ReasonCode, request.Decision)); err != nil {
		return IntegrationSummary{}, err
	}
	if request.Decision == DecisionReject {
		_ = service.recordReview(ctx, base, contract, ReviewResult{Outcome: project.ReviewRejected, ReviewerID: request.ActorID, Summary: request.ReasonCode}, "human-review")
		_, err = service.Control.Transition(ctx, service.transitionDecision(request, snapshot, storage.AssignmentFailed, "human_rejected", "rejected"))
		return request.Summary, err
	}
	if err := service.recordReview(ctx, base, contract, ReviewResult{Outcome: project.ReviewApproved, ReviewerID: request.ActorID, Summary: request.ReasonCode}, "human-review"); err != nil {
		return IntegrationSummary{}, fmt.Errorf("record human review: %w", err)
	}
	if _, err := service.History.AcceptWork(ctx, workhistory.AcceptWorkRequest{Metadata: withKey(base, "accept"), WorkPackageID: contract.WorkPackageID, AcceptedBy: request.ActorID, Summary: "approved for integration"}); err != nil {
		return IntegrationSummary{}, fmt.Errorf("accept canonical work: %w", err)
	}
	if _, err := service.Control.Transition(ctx, service.transitionDecision(request, snapshot, storage.AssignmentAccepted, "", "accepted")); err != nil {
		return IntegrationSummary{}, err
	}
	return request.Summary, nil
}
