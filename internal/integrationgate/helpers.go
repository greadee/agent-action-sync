package integrationgate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"regexp"
	"sort"
	"strings"
	"time"

	"syncgate/internal/executioncontract"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/resultintake"
	"syncgate/internal/storage"
	"syncgate/internal/workhistory"
	"syncgate/internal/workspace"
)

var (
	secretPattern      = regexp.MustCompile(`(?i)(authorization|api[_ -]?key|token|password|secret)\s*[:=]\s*[^\s,;]+`)
	windowsPathPattern = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|\\\\)[^\r\n,;]+`)
	unixPathPattern    = regexp.MustCompile(`(^|\s)/(?:[^\s,;]+)`)
)

func (service Service) validate() error {
	if service.Contracts == nil || service.Control == nil || service.History == nil || service.HistoryRoots == nil || service.Runtimes == nil || service.Results == nil || service.Contents == nil || service.Workspaces == nil || service.Tests == nil || service.Intake.Contracts == nil || service.Intake.Intake == nil || service.Intake.Now == nil {
		return ErrInvalidRequest
	}
	return nil
}

func (service Service) loadAuthority(ctx context.Context, assignmentID, attemptID string) (storage.OrchestrationSnapshot, executioncontract.Contract, error) {
	snapshot, err := service.Control.GetAssignment(ctx, assignmentID)
	if err != nil || snapshot.Assignment.AssignmentID != assignmentID || snapshot.Attempt.AttemptID != attemptID || snapshot.Assignment.CurrentAttemptID != attemptID {
		return storage.OrchestrationSnapshot{}, executioncontract.Contract{}, ErrStaleResult
	}
	record, err := service.Contracts.GetExecutionContract(ctx, snapshot.Assignment.ContractID, snapshot.Assignment.ContractVersion)
	if err != nil || record.Digest != snapshot.Assignment.ContractDigest {
		return storage.OrchestrationSnapshot{}, executioncontract.Contract{}, ErrStaleResult
	}
	var contract executioncontract.Contract
	if json.Unmarshal(record.ContractJSON, &contract) != nil || executioncontract.VerifyDigest(contract) != nil || contract.Digest != record.Digest || contract.ExecutionID != snapshot.Assignment.ExecutionID || contract.WorkPackageID != snapshot.Assignment.WorkPackageID {
		return storage.OrchestrationSnapshot{}, executioncontract.Contract{}, ErrStaleResult
	}
	return snapshot, contract, nil
}

func (service Service) validateManifest(manifest workspace.ChangeManifest) error {
	for _, file := range manifest.Files {
		if !file.Binary {
			continue
		}
		allowed := false
		for _, pattern := range service.AllowedBinaryPaths {
			pattern = strings.ReplaceAll(pattern, "\\", "/")
			path := strings.ReplaceAll(file.RelativePath, "\\", "/")
			if path == pattern || strings.HasSuffix(pattern, "/**") && strings.HasPrefix(path, strings.TrimSuffix(pattern, "**")) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%w: unexpected binary", ErrGateFailed)
		}
	}
	return nil
}

func (service Service) verifyReferencedContent(ctx context.Context, envelope resultintake.Envelope) (*handoffCandidate, []ArtifactSummary, error) {
	var total int64
	verify := func(ref resultintake.ContentReference) (Content, error) {
		content, err := service.Contents.GetContent(ctx, ref)
		if err != nil || int64(len(content.Bytes)) != ref.Size || hash(content.Bytes) != ref.Digest {
			return Content{}, ErrGateFailed
		}
		total += int64(len(content.Bytes))
		if total > MaxCollectedBytes {
			return Content{}, ErrGateFailed
		}
		return content, nil
	}
	for _, ref := range envelope.Patches {
		if _, err := verify(ref); err != nil {
			return nil, nil, err
		}
	}
	artifacts := make([]ArtifactSummary, 0, len(envelope.Artifacts))
	for _, ref := range envelope.Artifacts {
		content, err := verify(ref)
		if err != nil || content.Name == "" || content.MediaType == "" {
			return nil, nil, ErrGateFailed
		}
		mediaType, _, mediaErr := mime.ParseMediaType(content.MediaType)
		if mediaErr != nil || !strings.Contains(mediaType, "/") {
			return nil, nil, ErrGateFailed
		}
		artifacts = append(artifacts, ArtifactSummary{ArtifactID: ref.ID, Name: safeText(content.Name), MediaType: mediaType, Size: ref.Size, Digest: ref.Digest})
	}
	var handoff *handoffCandidate
	if envelope.Handoff != nil {
		content, err := verify(*envelope.Handoff)
		if err != nil || len(content.Bytes) > MaxHandoffBytes {
			return nil, nil, ErrGateFailed
		}
		decoder := json.NewDecoder(bytes.NewReader(content.Bytes))
		decoder.DisallowUnknownFields()
		var candidate handoffCandidate
		if decoder.Decode(&candidate) != nil || decoder.Decode(&struct{}{}) != io.EOF || candidate.HandoffID == "" || len(candidate.CompletedWork) == 0 || (candidate.Confidence != project.ConfidenceLow && candidate.Confidence != project.ConfidenceMedium && candidate.Confidence != project.ConfidenceHigh) {
			return nil, nil, ErrGateFailed
		}
		handoff = &candidate
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ArtifactID < artifacts[j].ArtifactID })
	return handoff, artifacts, nil
}

func (service Service) runGates(ctx context.Context, request EvaluateRequest, base workhistory.Metadata, contract executioncontract.Contract) ([]GateEvidence, error) {
	result := make([]GateEvidence, 0, len(contract.RequiredGates))
	for _, gate := range contract.RequiredGates {
		if humanGate(gate.GateID) {
			continue
		}
		plan, ok := service.TestPlans[gate.GateID]
		if !ok || plan.GateID != gate.GateID || plan.GateVersion != gate.Version || plan.GateDigest != gate.Digest || plan.CommandID == "" || !validDigest(plan.CommandDigest) {
			return nil, ErrGateFailed
		}
		pending := orchestration.GateStatusRequest{AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, GateID: gate.GateID, GateVersion: gate.Version, GateDigest: gate.Digest, Status: storage.GatePending, EvidenceID: "evidence:pending", ReasonCode: "authorized_test_pending", AuditID: derivedID("audit:", gate.Digest, "pending"), ActorID: request.ActorID}
		if _, err := service.Control.RecordGateStatus(ctx, pending); err != nil {
			return nil, err
		}
		test, runErr := service.Tests.RunAuthorized(ctx, contract, plan)
		if runErr != nil {
			test.Outcome = project.TestFailed
		}
		if test.EvidenceID == "" || !validDigest(test.EvidenceDigest) || test.DurationMilliseconds < 0 || (test.Outcome != project.TestPassed && test.Outcome != project.TestFailed && test.Outcome != project.TestSkipped) {
			return nil, ErrGateFailed
		}
		exit := test.ExitCode
		evidence := GateEvidence{GateID: gate.GateID, GateVersion: gate.Version, GateDigest: gate.Digest, CommandID: plan.CommandID, CommandDigest: plan.CommandDigest, Outcome: test.Outcome, ExitCode: &exit, DurationMilliseconds: test.DurationMilliseconds, EvidenceID: test.EvidenceID, EvidenceDigest: test.EvidenceDigest}
		result = append(result, evidence)
		_, historyErr := service.History.RecordTest(ctx, workhistory.RecordTestRequest{ExecutionRequest: workhistory.ExecutionRequest{Metadata: withKey(base, "test-"+gate.GateID), WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID}, Name: plan.CommandID, Outcome: test.Outcome, DurationMilliseconds: test.DurationMilliseconds, CommandID: plan.CommandID, CommandDigest: plan.CommandDigest, ExitCode: &exit, EvidenceID: test.EvidenceID, EvidenceDigest: test.EvidenceDigest})
		if historyErr != nil {
			return nil, historyErr
		}
		state, code := storage.GateSatisfied, "authorized_test_passed"
		if runErr != nil || test.Outcome != project.TestPassed || test.ExitCode != 0 {
			state, code = storage.GateFailed, "authorized_test_failed"
		}
		pending.Status, pending.EvidenceID, pending.ReasonCode = state, test.EvidenceID, code
		if _, err := service.Control.RecordGateStatus(ctx, pending); err != nil {
			return nil, err
		}
		if state == storage.GateFailed {
			return result, ErrGateFailed
		}
	}
	return result, nil
}

func (service Service) startHistory(ctx context.Context, base workhistory.Metadata, contract executioncontract.Contract) error {
	steps := []workhistory.TransitionWorkPackageRequest{
		{Metadata: withKey(base, "ready"), WorkPackageID: contract.WorkPackageID, From: project.WorkPackagePlanned, To: project.WorkPackageReady, ReasonCode: "result_collected"},
		{Metadata: withKey(base, "in-progress"), WorkPackageID: contract.WorkPackageID, From: project.WorkPackageReady, To: project.WorkPackageInProgress, ReasonCode: "gate_evaluation"},
	}
	for _, step := range steps {
		if _, err := service.History.TransitionWorkPackage(ctx, step); err != nil {
			return err
		}
	}
	_, err := service.History.StartExecution(ctx, workhistory.StartExecutionRequest{Metadata: withKey(base, "execution-start"), WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID, TradeReference: &contract.Trade, WorkerReference: &contract.Worker, ContractReference: &project.RegistryReference{ID: contract.ContractID, Version: contract.Version, Digest: contract.Digest}})
	return err
}

func (service Service) completeHistory(ctx context.Context, base workhistory.Metadata, contract executioncontract.Contract, manifest workspace.ChangeManifest, handoff *handoffCandidate, artifacts []ArtifactSummary) error {
	if _, err := service.History.CompleteExecution(ctx, workhistory.CompleteExecutionRequest{ExecutionRequest: workhistory.ExecutionRequest{Metadata: withKey(base, "execution-complete"), WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID}, Summary: "authority gates passed"}); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if _, err := service.History.RegisterArtifactReference(ctx, workhistory.RegisterArtifactReferenceRequest{Metadata: withKey(base, "artifact-"+artifact.ArtifactID), ArtifactID: artifact.ArtifactID, WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID, Name: artifact.Name, MediaType: artifact.MediaType, Size: artifact.Size, ContentHash: artifact.Digest}); err != nil {
			return err
		}
	}
	if handoff != nil {
		changed := make([]string, len(manifest.Files))
		for i := range manifest.Files {
			changed[i] = manifest.Files[i].RelativePath
		}
		_, err := service.History.CreateHandoff(ctx, workhistory.CreateHandoffRequest{ExecutionRequest: workhistory.ExecutionRequest{Metadata: withKey(base, "handoff"), WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID}, HandoffID: handoff.HandoffID, CompletedWork: handoff.CompletedWork, ChangedFiles: changed, Decisions: handoff.Decisions, Tests: handoff.Tests, Limitations: handoff.Limitations, UnresolvedIssues: handoff.UnresolvedIssues, Assumptions: handoff.Assumptions, FollowUpWork: handoff.FollowUpWork, ReviewRequirements: handoff.ReviewRequirements, IntegrationConsiderations: handoff.IntegrationConsiderations, Confidence: handoff.Confidence, FailureConditions: handoff.FailureConditions})
		if err != nil {
			return err
		}
	}
	_, err := service.History.TransitionWorkPackage(ctx, workhistory.TransitionWorkPackageRequest{Metadata: withKey(base, "review"), WorkPackageID: contract.WorkPackageID, From: project.WorkPackageInProgress, To: project.WorkPackageReview, ReasonCode: "gates_passed"})
	return err
}

func (service Service) fail(ctx context.Context, request EvaluateRequest, base workhistory.Metadata, contract executioncontract.Contract, code string, cause error) error {
	_, _ = service.History.FailExecution(ctx, workhistory.FailExecutionRequest{ExecutionRequest: workhistory.ExecutionRequest{Metadata: withKey(base, "execution-failed-"+code), WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID}, FailureCode: code, Summary: "authority gate rejected result"})
	_, _ = service.History.TransitionWorkPackage(ctx, workhistory.TransitionWorkPackageRequest{Metadata: withKey(base, "work-failed-"+code), WorkPackageID: contract.WorkPackageID, From: project.WorkPackageInProgress, To: project.WorkPackageFailed, ReasonCode: code})
	snapshot, _ := service.Control.GetAssignment(ctx, request.AssignmentID)
	_, _ = service.Control.Transition(ctx, service.transition(request, snapshot, storage.AssignmentFailed, code, "failed"))
	return fmt.Errorf("%w: %s: %v", ErrGateFailed, code, cause)
}

func (service Service) recordReview(ctx context.Context, base workhistory.Metadata, contract executioncontract.Contract, review ReviewResult, key string) error {
	_, err := service.History.RecordReview(ctx, workhistory.RecordReviewRequest{Metadata: withKey(base, key), WorkPackageID: contract.WorkPackageID, ExecutionID: contract.ExecutionID, Outcome: review.Outcome, ReviewerID: review.ReviewerID, Summary: review.Summary})
	return err
}

func (service Service) recordReviewGateStatuses(ctx context.Context, assignmentID, attemptID, actorID string, contract executioncontract.Contract, status storage.GateState, evidenceID, reasonCode string) error {
	for _, gate := range contract.RequiredGates {
		if !humanGate(gate.GateID) {
			continue
		}
		if _, err := service.Control.RecordGateStatus(ctx, orchestration.GateStatusRequest{AssignmentID: assignmentID, AttemptID: attemptID, GateID: gate.GateID, GateVersion: gate.Version, GateDigest: gate.Digest, Status: status, EvidenceID: evidenceID, ReasonCode: reasonCode, AuditID: derivedID("audit:", gate.Digest, string(status)), ActorID: actorID}); err != nil {
			return err
		}
	}
	return nil
}

func (service Service) transition(request EvaluateRequest, snapshot storage.OrchestrationSnapshot, target storage.AssignmentState, failure, suffix string) orchestration.TransitionRequest {
	d := hash([]byte(request.AssignmentID + "\x00" + request.AttemptID + "\x00" + suffix))
	return orchestration.TransitionRequest{AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, TargetState: target, LeaseGeneration: snapshot.Attempt.LeaseGeneration, FencingDigest: request.FencingDigest, FailureCode: failure, OperationID: derivedID("operation:", d, suffix), OperationDigest: d, AuditID: derivedID("audit:", d, suffix), ActorID: request.ActorID}
}

func (service Service) transitionDecision(request DecisionRequest, snapshot storage.OrchestrationSnapshot, target storage.AssignmentState, failure, suffix string) orchestration.TransitionRequest {
	d := hash([]byte(request.AssignmentID + "\x00" + request.AttemptID + "\x00" + suffix + "\x00" + request.Summary.Digest))
	return orchestration.TransitionRequest{AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, TargetState: target, LeaseGeneration: snapshot.Attempt.LeaseGeneration, FencingDigest: request.FencingDigest, FailureCode: failure, OperationID: derivedID("operation:", d, suffix), OperationDigest: d, AuditID: derivedID("audit:", d, suffix), ActorID: request.ActorID}
}

func (service Service) metadata(root string, contract executioncontract.Contract, when time.Time, actor string) workhistory.Metadata {
	return workhistory.Metadata{RootPath: root, OccurredAt: when, Producer: project.Producer{WorkerID: contract.Worker.ID, DeviceID: actor, Trade: contract.Trade.ID, Provider: contract.Provider.ID, Model: contract.Model.ID}, ProjectVersion: contract.ProjectRevision, ContextVersion: contract.ContextDigest, InstructionVersion: contract.Instruction.Digest, Correlation: &project.Correlation{AuditID: derivedID("audit:", contract.Digest, "result"), RevisionID: contract.ProjectRevision}}
}

func withKey(value workhistory.Metadata, key string) workhistory.Metadata {
	value.IdempotencyKey = "integration-" + key
	value.OccurredAt = value.OccurredAt.Add(operationOffset(key))
	return value
}

func operationOffset(key string) time.Duration {
	switch {
	case key == "ready":
		return time.Second
	case key == "in-progress":
		return 2 * time.Second
	case key == "execution-start":
		return 3 * time.Second
	case strings.HasPrefix(key, "test-"):
		return 10 * time.Second
	case strings.HasPrefix(key, "execution-failed-") || key == "execution-complete":
		return 20 * time.Second
	case strings.HasPrefix(key, "work-failed-"):
		return 21 * time.Second
	case strings.HasPrefix(key, "artifact-"):
		return 22 * time.Second
	case key == "handoff":
		return 23 * time.Second
	case key == "review":
		return 30 * time.Second
	case key == "automated-review":
		return 31 * time.Second
	case key == "human-review":
		return 32 * time.Second
	case key == "accept":
		return 40 * time.Second
	default:
		return 50 * time.Second
	}
}
func gateByID(gates []storage.OrchestrationGateStatus, id string) (storage.OrchestrationGateStatus, bool) {
	for _, g := range gates {
		if g.GateID == id {
			return g, true
		}
	}
	return storage.OrchestrationGateStatus{}, false
}
func hasDecision(decisions []storage.OrchestrationOperatorDecision, decision, digest string) bool {
	for _, value := range decisions {
		if value.Decision == decision && value.IdempotencyDigest == digest {
			return true
		}
	}
	return false
}
func humanGate(id string) bool { return strings.Contains(id, "review") }
func requiresReviewer(contract executioncontract.Contract) bool {
	if contract.ReviewRequired {
		return true
	}
	for _, gate := range contract.RequiredGates {
		if humanGate(gate.GateID) {
			return true
		}
	}
	return false
}
func reason(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	var normalized strings.Builder
	for _, character := range strings.ToLower(value) {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == ':' || character == '-'
		if valid {
			normalized.WriteRune(character)
		} else {
			normalized.WriteByte('_')
		}
		if normalized.Len() >= 128 {
			break
		}
	}
	if normalized.Len() == 0 {
		return fallback
	}
	return normalized.String()
}
func safeText(value string) string {
	value = secretPattern.ReplaceAllString(value, "$1=[redacted]")
	value = windowsPathPattern.ReplaceAllString(value, "[redacted-path]")
	return unixPathPattern.ReplaceAllString(value, "$1[redacted-path]")
}
func safeList(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = safeText(value)
	}
	return result
}
func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
func hash(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func derivedID(prefix, digest, suffix string) string {
	return prefix + hash([]byte(digest + "\x00" + suffix))[:24]
}
func summaryDigest(summary IntegrationSummary) string {
	summary.Digest = ""
	raw, _ := json.Marshal(summary)
	return hash(raw)
}
