package project

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"syncgate/internal/filesystem"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func (record ProjectManifest) Validate() error         { return validateRecord(record, true) }
func (record TaskRevision) Validate() error            { return validateRecord(record, true) }
func (record DependencyGraphRevision) Validate() error { return validateRecord(record, true) }
func (record WorkPackageDefinition) Validate() error   { return validateRecord(record, true) }
func (record ExecutionManifest) Validate() error       { return validateRecord(record, true) }
func (record WorkEvent) Validate() error               { return validateRecord(record, true) }
func (record Handoff) Validate() error                 { return validateRecord(record, true) }
func (record ArtifactManifest) Validate() error        { return validateRecord(record, true) }

func validateRecord(record any, requireIntegrity bool) error {
	switch value := record.(type) {
	case ProjectManifest:
		return validateProjectManifest(value, requireIntegrity)
	case *ProjectManifest:
		if value == nil {
			return errors.New("project manifest is nil")
		}
		return validateProjectManifest(*value, requireIntegrity)
	case TaskRevision:
		return validateTaskRevision(value, requireIntegrity)
	case *TaskRevision:
		if value == nil {
			return errors.New("task revision is nil")
		}
		return validateTaskRevision(*value, requireIntegrity)
	case DependencyGraphRevision:
		return validateDependencyGraphRevision(value, requireIntegrity)
	case *DependencyGraphRevision:
		if value == nil {
			return errors.New("dependency graph revision is nil")
		}
		return validateDependencyGraphRevision(*value, requireIntegrity)
	case WorkPackageDefinition:
		return validateWorkPackage(value, requireIntegrity)
	case *WorkPackageDefinition:
		if value == nil {
			return errors.New("work package definition is nil")
		}
		return validateWorkPackage(*value, requireIntegrity)
	case ExecutionManifest:
		return validateExecution(value, requireIntegrity)
	case *ExecutionManifest:
		if value == nil {
			return errors.New("execution manifest is nil")
		}
		return validateExecution(*value, requireIntegrity)
	case WorkEvent:
		return validateWorkEvent(value, requireIntegrity)
	case *WorkEvent:
		if value == nil {
			return errors.New("work event is nil")
		}
		return validateWorkEvent(*value, requireIntegrity)
	case Handoff:
		return validateHandoff(value, requireIntegrity)
	case *Handoff:
		if value == nil {
			return errors.New("handoff is nil")
		}
		return validateHandoff(*value, requireIntegrity)
	case ArtifactManifest:
		return validateArtifact(value, requireIntegrity)
	case *ArtifactManifest:
		if value == nil {
			return errors.New("artifact manifest is nil")
		}
		return validateArtifact(*value, requireIntegrity)
	default:
		return fmt.Errorf("unsupported project record type %T", record)
	}
}

func validateProjectManifest(record ProjectManifest, requireIntegrity bool) error {
	if err := validateHeader(record.RecordHeader, RecordProjectManifest, requireIntegrity); err != nil {
		return err
	}
	if err := validateText("name", record.Name, MaxNameBytes, true); err != nil {
		return err
	}
	if err := validateAuthority(record.Authority); err != nil {
		return err
	}
	return validateUTCTime("created_at", record.CreatedAt)
}

func validateTaskRevision(record TaskRevision, requireIntegrity bool) error {
	if err := validateHeader(record.RecordHeader, RecordTaskRevision, requireIntegrity); err != nil {
		return err
	}
	if record.Schema.Minor < 1 {
		return errors.New("task revision requires schema minor version 1 or newer")
	}
	if err := validateNamespacedIdentifier("task_id", record.TaskID, "task:"); err != nil {
		return err
	}
	if err := validateRevision("task_revision", record.Revision, record.Predecessor); err != nil {
		return err
	}
	if err := validateText("objective", record.Objective, MaxTextBytes, true); err != nil {
		return err
	}
	if !validTaskPriority(record.Priority) {
		return fmt.Errorf("unsupported task priority %q", record.Priority)
	}
	if err := validateRiskDimensions("risk", record.Risk); err != nil {
		return err
	}
	if record.Resources != nil {
		if err := validateResourceConstraints("resources", *record.Resources); err != nil {
			return err
		}
	}
	if record.GraphRevision < 1 || record.GraphRevision > MaxAggregateRevision {
		return fmt.Errorf("graph_revision must be between 1 and %d", MaxAggregateRevision)
	}
	if err := validateQualityGates("quality_gates", record.QualityGates); err != nil {
		return err
	}
	if err := validateUTCTime("created_at", record.CreatedAt); err != nil {
		return err
	}
	if err := validateProvenance(record.Provenance); err != nil {
		return err
	}
	if record.Provenance.WorkPackageID != "" || record.Provenance.ExecutionID != "" {
		return errors.New("task provenance cannot have work package or execution scope")
	}
	return nil
}

func validateDependencyGraphRevision(record DependencyGraphRevision, requireIntegrity bool) error {
	if err := validateHeader(record.RecordHeader, RecordDependencyGraph, requireIntegrity); err != nil {
		return err
	}
	if record.Schema.Minor < 1 {
		return errors.New("dependency graph revision requires schema minor version 1 or newer")
	}
	if err := validateNamespacedIdentifier("task_id", record.TaskID, "task:"); err != nil {
		return err
	}
	if record.TaskRevision < 1 || record.TaskRevision > MaxAggregateRevision {
		return fmt.Errorf("task_revision must be between 1 and %d", MaxAggregateRevision)
	}
	if err := validateRevision("graph_revision", record.Revision, record.Predecessor); err != nil {
		return err
	}
	if len(record.Members) == 0 || len(record.Members) > MaxListItems {
		return fmt.Errorf("members requires between 1 and %d items", MaxListItems)
	}
	previous := ""
	seen := make(map[string]struct{}, len(record.Members))
	for index, member := range record.Members {
		if err := validateIdentifier(fmt.Sprintf("members[%d].work_package_id", index), member.WorkPackageID, true); err != nil {
			return err
		}
		if err := validateIdentifier(fmt.Sprintf("members[%d].definition_record_id", index), member.DefinitionRecordID, true); err != nil {
			return err
		}
		if !validSHA256(member.DefinitionDigest) {
			return fmt.Errorf("members[%d].definition_digest must be a lowercase SHA-256 digest", index)
		}
		if _, exists := seen[member.WorkPackageID]; exists {
			return fmt.Errorf("members contains duplicate work package %q", member.WorkPackageID)
		}
		if previous != "" && member.WorkPackageID <= previous {
			return errors.New("members must be sorted by work_package_id")
		}
		seen[member.WorkPackageID] = struct{}{}
		previous = member.WorkPackageID
	}
	if !validSHA256(record.DependencySetDigest) {
		return errors.New("dependency_set_digest must be a lowercase SHA-256 digest")
	}
	if err := validateIdentifierList("barriers", record.Barriers); err != nil {
		return err
	}
	previous = ""
	for _, barrier := range record.Barriers {
		if _, exists := seen[barrier]; !exists {
			return fmt.Errorf("barrier %q is not a graph member", barrier)
		}
		if previous != "" && barrier <= previous {
			return errors.New("barriers must be sorted")
		}
		previous = barrier
	}
	if err := validateUTCTime("created_at", record.CreatedAt); err != nil {
		return err
	}
	if err := validateProvenance(record.Provenance); err != nil {
		return err
	}
	if record.Provenance.WorkPackageID != "" || record.Provenance.ExecutionID != "" {
		return errors.New("dependency graph provenance cannot have work package or execution scope")
	}
	return nil
}

func validateWorkPackage(record WorkPackageDefinition, requireIntegrity bool) error {
	if err := validateHeader(record.RecordHeader, RecordWorkPackage, requireIntegrity); err != nil {
		return err
	}
	if err := validateIdentifier("work_package_id", record.WorkPackageID, true); err != nil {
		return err
	}
	if err := validateText("objective", record.Objective, MaxTextBytes, true); err != nil {
		return err
	}
	if err := validateText("trade", record.Trade, MaxNameBytes, true); err != nil {
		return err
	}
	if err := validateText("specialization", record.Specialization, MaxNameBytes, false); err != nil {
		return err
	}
	if err := validateScopePathList("scope.allowed", record.Scope.Allowed); err != nil {
		return err
	}
	if err := validateScopePathList("scope.inspect", record.Scope.Inspect); err != nil {
		return err
	}
	if err := validateScopePathList("scope.forbidden", record.Scope.Forbidden); err != nil {
		return err
	}
	if err := validateIdentifierList("dependencies", record.Dependencies); err != nil {
		return err
	}
	for _, dependency := range record.Dependencies {
		if dependency == record.WorkPackageID {
			return errors.New("work package cannot depend on itself")
		}
	}
	if err := validateTextList("deliverables", record.Deliverables, true); err != nil {
		return err
	}
	if err := validateTextList("acceptance_criteria", record.AcceptanceCriteria, true); err != nil {
		return err
	}
	if err := validateWorkPackageTaskFields(record); err != nil {
		return err
	}
	if record.TradeReference != nil {
		if record.Schema.Minor < 2 {
			return errors.New("trade reference requires schema minor version 2 or newer")
		}
		if err := validateRegistryReference("trade_reference", *record.TradeReference, "trade:"); err != nil {
			return err
		}
	}
	if err := validateUTCTime("created_at", record.CreatedAt); err != nil {
		return err
	}
	if err := validateProvenance(record.Provenance); err != nil {
		return err
	}
	if record.Provenance.WorkPackageID != record.WorkPackageID || record.Provenance.ExecutionID != "" {
		return errors.New("provenance work_package_id does not match record")
	}
	return nil
}

func validateExecution(record ExecutionManifest, requireIntegrity bool) error {
	if err := validateHeader(record.RecordHeader, RecordExecution, requireIntegrity); err != nil {
		return err
	}
	if err := validateIdentifier("execution_id", record.ExecutionID, true); err != nil {
		return err
	}
	if err := validateIdentifier("work_package_id", record.WorkPackageID, true); err != nil {
		return err
	}
	if !validExecutionState(record.State) {
		return fmt.Errorf("unsupported execution state %q", record.State)
	}
	if err := validateProducer(record.Producer); err != nil {
		return err
	}
	if err := validateUTCTime("created_at", record.CreatedAt); err != nil {
		return err
	}
	if err := validateProvenance(record.Provenance); err != nil {
		return err
	}
	if record.Producer != record.Provenance.Producer {
		return errors.New("execution producer does not match provenance producer")
	}
	for _, item := range []struct {
		name       string
		reference  *RegistryReference
		prefix     string
		minorSince int
	}{
		{name: "trade_reference", reference: record.TradeReference, prefix: "trade:", minorSince: 2},
		{name: "worker_reference", reference: record.WorkerReference, prefix: "worker:", minorSince: 2},
		{name: "contract_reference", reference: record.ContractReference, prefix: "contract:", minorSince: 3},
	} {
		if item.reference == nil {
			continue
		}
		if record.Schema.Minor < item.minorSince {
			return fmt.Errorf("%s requires schema minor version %d or newer", item.name, item.minorSince)
		}
		if err := validateRegistryReference(item.name, *item.reference, item.prefix); err != nil {
			return err
		}
	}
	if record.Provenance.WorkPackageID != record.WorkPackageID || record.Provenance.ExecutionID != record.ExecutionID {
		return errors.New("execution provenance scope does not match record")
	}
	return nil
}

func validateWorkEvent(record WorkEvent, requireIntegrity bool) error {
	if err := validateHeader(record.RecordHeader, RecordWorkEvent, requireIntegrity); err != nil {
		return err
	}
	if err := validateUTCTime("occurred_at", record.OccurredAt); err != nil {
		return err
	}
	if err := validateIdentifier("work_package_id", record.WorkPackageID, false); err != nil {
		return err
	}
	if err := validateIdentifier("execution_id", record.ExecutionID, false); err != nil {
		return err
	}
	if err := validateProducer(record.Producer); err != nil {
		return err
	}
	if err := validateIdentifier("causation_id", record.CausationID, false); err != nil {
		return err
	}
	if record.CausationID == record.RecordID {
		return errors.New("work event cannot cause itself")
	}
	if record.Correlation != nil {
		if err := validateIdentifier("correlation.audit_id", record.Correlation.AuditID, false); err != nil {
			return err
		}
		if err := validateIdentifier("correlation.revision_id", record.Correlation.RevisionID, false); err != nil {
			return err
		}
		if record.Correlation.AuditID == "" && record.Correlation.RevisionID == "" {
			return errors.New("correlation must contain audit_id or revision_id")
		}
	}
	return validateEventPayload(record)
}

func validateHandoff(record Handoff, requireIntegrity bool) error {
	if err := validateHeader(record.RecordHeader, RecordHandoff, requireIntegrity); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"handoff_id": record.HandoffID, "work_package_id": record.WorkPackageID, "execution_id": record.ExecutionID,
	} {
		if err := validateIdentifier(name, value, true); err != nil {
			return err
		}
	}
	if err := validateTextList("completed_work", record.CompletedWork, true); err != nil {
		return err
	}
	if err := validatePathList("changed_files", record.ChangedFiles); err != nil {
		return err
	}
	for name, values := range map[string][]string{
		"decisions": record.Decisions, "limitations": record.Limitations,
		"unresolved_issues": record.UnresolvedIssues, "assumptions": record.Assumptions,
		"follow_up_work": record.FollowUpWork, "review_requirements": record.ReviewRequirements,
		"integration_considerations": record.IntegrationConsiderations, "failure_conditions": record.FailureConditions,
	} {
		if err := validateTextList(name, values, false); err != nil {
			return err
		}
	}
	if len(record.Tests) > MaxListItems {
		return fmt.Errorf("tests has more than %d items", MaxListItems)
	}
	for index, result := range record.Tests {
		if err := validateText(fmt.Sprintf("tests[%d].name", index), result.Name, MaxNameBytes, true); err != nil {
			return err
		}
		if !validTestOutcome(result.Outcome) {
			return fmt.Errorf("tests[%d] has unsupported outcome %q", index, result.Outcome)
		}
	}
	if !validConfidence(record.Confidence) {
		return fmt.Errorf("unsupported confidence %q", record.Confidence)
	}
	if err := validateUTCTime("created_at", record.CreatedAt); err != nil {
		return err
	}
	if err := validateProvenance(record.Provenance); err != nil {
		return err
	}
	if record.Provenance.WorkPackageID != record.WorkPackageID || record.Provenance.ExecutionID != record.ExecutionID {
		return errors.New("handoff provenance scope does not match record")
	}
	return nil
}

func validateArtifact(record ArtifactManifest, requireIntegrity bool) error {
	if err := validateHeader(record.RecordHeader, RecordArtifact, requireIntegrity); err != nil {
		return err
	}
	if err := validateIdentifier("artifact_id", record.ArtifactID, true); err != nil {
		return err
	}
	if err := validateText("name", record.Name, MaxNameBytes, true); err != nil {
		return err
	}
	if err := validateMediaType(record.MediaType); err != nil {
		return err
	}
	if record.Size < 0 {
		return errors.New("artifact size cannot be negative")
	}
	if record.HashAlgorithm != HashAlgorithmSHA256 || !validSHA256(record.ContentHash) {
		return errors.New("artifact content hash must be a lowercase SHA-256 digest")
	}
	if record.BlobRelativePath != "" {
		if err := validateRelativePath("blob_relative_path", record.BlobRelativePath); err != nil {
			return err
		}
		want := "artifacts/blobs/sha256/" + record.ContentHash
		if record.BlobRelativePath != want {
			return fmt.Errorf("blob_relative_path must be %q", want)
		}
	}
	if err := validateUTCTime("created_at", record.CreatedAt); err != nil {
		return err
	}
	return validateProvenance(record.Provenance)
}

func validateHeader(header RecordHeader, kind RecordKind, requireIntegrity bool) error {
	if header.Schema.Family != SchemaFamily {
		return fmt.Errorf("unsupported schema family %q", header.Schema.Family)
	}
	if header.Schema.Major != SchemaMajor {
		return fmt.Errorf("unsupported schema major version %d", header.Schema.Major)
	}
	if header.Schema.Minor < 0 {
		return errors.New("schema minor version cannot be negative")
	}
	if !requireIntegrity && header.Schema.Minor > SchemaMinor {
		return fmt.Errorf("writer supports schema minor versions through %d, got %d", SchemaMinor, header.Schema.Minor)
	}
	if header.RecordKind != kind {
		return fmt.Errorf("record_kind %q does not match %q", header.RecordKind, kind)
	}
	if err := validateIdentifier("record_id", header.RecordID, true); err != nil {
		return err
	}
	if err := validateIdentifier("project_id", header.ProjectID, true); err != nil {
		return err
	}
	if requireIntegrity {
		if header.Integrity.Algorithm != HashAlgorithmSHA256 || !validSHA256(header.Integrity.Digest) {
			return errors.New("record integrity must be a lowercase SHA-256 digest")
		}
	} else if header.Integrity.Algorithm != "" && header.Integrity.Algorithm != HashAlgorithmSHA256 {
		return fmt.Errorf("unsupported record integrity algorithm %q", header.Integrity.Algorithm)
	}
	return nil
}

func validateWorkPackageTaskFields(record WorkPackageDefinition) error {
	hasTaskFields := record.TaskID != "" || record.TaskRevision != 0 || record.GraphRevision != 0 || record.Priority != "" ||
		len(record.Risk) > 0 || record.Resources != nil || len(record.QualityGates) > 0
	if !hasTaskFields {
		return nil
	}
	if record.Schema.Minor < 1 {
		return errors.New("task-bound work package requires schema minor version 1 or newer")
	}
	if err := validateNamespacedIdentifier("task_id", record.TaskID, "task:"); err != nil {
		return err
	}
	if record.TaskRevision < 1 || record.TaskRevision > MaxAggregateRevision || record.GraphRevision < 1 || record.GraphRevision > MaxAggregateRevision {
		return fmt.Errorf("task_revision and graph_revision must be between 1 and %d", MaxAggregateRevision)
	}
	if !validTaskPriority(record.Priority) {
		return fmt.Errorf("unsupported task priority %q", record.Priority)
	}
	if err := validateRiskDimensions("risk", record.Risk); err != nil {
		return err
	}
	if record.Resources != nil {
		if err := validateResourceConstraints("resources", *record.Resources); err != nil {
			return err
		}
	}
	return validateQualityGates("quality_gates", record.QualityGates)
}

func validateRevision(name string, revision int64, predecessor *RevisionReference) error {
	if revision < 1 || revision > MaxAggregateRevision {
		return fmt.Errorf("%s must be between 1 and %d", name, MaxAggregateRevision)
	}
	if revision == 1 {
		if predecessor != nil {
			return fmt.Errorf("%s 1 cannot have a predecessor", name)
		}
		return nil
	}
	if predecessor == nil || predecessor.Revision != revision-1 || !validSHA256(predecessor.Digest) {
		return fmt.Errorf("%s requires the immediately preceding revision and digest", name)
	}
	return nil
}

func validateRegistryReference(name string, reference RegistryReference, prefix string) error {
	if err := validateNamespacedIdentifier(name+".id", reference.ID, prefix); err != nil {
		return err
	}
	if reference.Version < 1 || reference.Version > MaxAggregateRevision {
		return fmt.Errorf("%s.version must be between 1 and %d", name, MaxAggregateRevision)
	}
	if !validSHA256(reference.Digest) {
		return fmt.Errorf("%s.digest must be a lowercase SHA-256 digest", name)
	}
	return nil
}

func validateNamespacedIdentifier(name, value, prefix string) error {
	if err := validateIdentifier(name, value, true); err != nil {
		return err
	}
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return fmt.Errorf("%s must use namespace %q", name, prefix)
	}
	return nil
}

func validTaskPriority(value TaskPriority) bool {
	return value == TaskPriorityLow || value == TaskPriorityNormal || value == TaskPriorityHigh || value == TaskPriorityCritical
}

func validRiskLevel(value RiskLevel) bool {
	return value == RiskLow || value == RiskMedium || value == RiskHigh || value == RiskCritical
}

func validateRiskDimensions(name string, values []RiskDimension) error {
	if len(values) > MaxListItems {
		return fmt.Errorf("%s has more than %d items", name, MaxListItems)
	}
	previous := ""
	for index, value := range values {
		if err := validateIdentifier(fmt.Sprintf("%s[%d].name", name, index), value.Name, true); err != nil {
			return err
		}
		if !validRiskLevel(value.Level) {
			return fmt.Errorf("%s[%d] has unsupported level %q", name, index, value.Level)
		}
		if previous != "" && value.Name <= previous {
			return fmt.Errorf("%s must be sorted with unique names", name)
		}
		previous = value.Name
	}
	return nil
}

func validateResourceConstraints(name string, value ResourceConstraints) error {
	for suffix, values := range map[string][]string{
		"required_capabilities": value.RequiredCapabilities,
		"required_tools":        value.RequiredTools,
		"allowed_os":            value.AllowedOS,
		"allowed_architectures": value.AllowedArchitectures,
	} {
		if err := validateSortedIdentifierList(name+"."+suffix, values); err != nil {
			return err
		}
	}
	if value.MinimumMemoryMB < 0 || value.MinimumDiskMB < 0 {
		return fmt.Errorf("%s memory and disk minimums cannot be negative", name)
	}
	return nil
}

func validateQualityGates(name string, values []QualityGateReference) error {
	if len(values) > MaxListItems {
		return fmt.Errorf("%s has more than %d items", name, MaxListItems)
	}
	previous := ""
	for index, value := range values {
		if err := validateNamespacedIdentifier(fmt.Sprintf("%s[%d].gate_id", name, index), value.GateID, "gate:"); err != nil {
			return err
		}
		if value.Version < 1 || !validSHA256(value.Digest) {
			return fmt.Errorf("%s[%d] requires a positive version and SHA-256 digest", name, index)
		}
		key := fmt.Sprintf("%s:%020d", value.GateID, value.Version)
		if previous != "" && key <= previous {
			return fmt.Errorf("%s must be sorted with unique identity versions", name)
		}
		previous = key
	}
	return nil
}

func validateSortedIdentifierList(name string, values []string) error {
	if err := validateIdentifierList(name, values); err != nil {
		return err
	}
	for index := 1; index < len(values); index++ {
		if values[index] <= values[index-1] {
			return fmt.Errorf("%s must be sorted", name)
		}
	}
	return nil
}

func validateAuthority(authority Authority) error {
	if err := validateIdentifier("authority.device_id", authority.DeviceID, true); err != nil {
		return err
	}
	return validateIdentifier("authority.share_id", authority.ShareID, true)
}

func validateProducer(producer Producer) error {
	if err := validateIdentifier("producer.worker_id", producer.WorkerID, false); err != nil {
		return err
	}
	if err := validateIdentifier("producer.device_id", producer.DeviceID, true); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"producer.trade": producer.Trade, "producer.specialization": producer.Specialization,
		"producer.provider": producer.Provider, "producer.model": producer.Model,
		"producer.model_version": producer.ModelVersion,
	} {
		if err := validateText(name, value, MaxNameBytes, false); err != nil {
			return err
		}
	}
	return nil
}

func validateProvenance(provenance Provenance) error {
	if err := validateProducer(provenance.Producer); err != nil {
		return err
	}
	if err := validateIdentifier("provenance.work_package_id", provenance.WorkPackageID, false); err != nil {
		return err
	}
	if err := validateIdentifier("provenance.execution_id", provenance.ExecutionID, false); err != nil {
		return err
	}
	if provenance.ExecutionID != "" && provenance.WorkPackageID == "" {
		return errors.New("provenance execution_id requires work_package_id")
	}
	if err := validateIdentifierList("provenance.source_artifact_ids", provenance.SourceArtifactIDs); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"provenance.project_version":     provenance.ProjectVersion,
		"provenance.context_version":     provenance.ContextVersion,
		"provenance.instruction_version": provenance.InstructionVersion,
	} {
		if err := validateText(name, value, MaxShortTextBytes, false); err != nil {
			return err
		}
	}
	return validateUTCTime("provenance.created_at", provenance.CreatedAt)
}

func validateEventPayload(event WorkEvent) error {
	requireWorkPackage := event.EventType != EventProjectRegistered
	requireExecution := event.EventType == EventExecutionStarted || event.EventType == EventExecutionPaused ||
		event.EventType == EventExecutionFailed || event.EventType == EventExecutionCompleted ||
		event.EventType == EventTestRecorded || event.EventType == EventHandoffCreated || event.EventType == EventTelemetryRecorded
	if requireWorkPackage && event.WorkPackageID == "" {
		return fmt.Errorf("event type %s requires work_package_id", event.EventType)
	}
	if !requireWorkPackage && (event.WorkPackageID != "" || event.ExecutionID != "") {
		return fmt.Errorf("event type %s cannot have work package or execution scope", event.EventType)
	}
	if requireExecution && event.ExecutionID == "" {
		return fmt.Errorf("event type %s requires execution_id", event.EventType)
	}
	if event.ExecutionID != "" && event.WorkPackageID == "" {
		return errors.New("execution-scoped event requires work_package_id")
	}

	switch event.EventType {
	case EventProjectRegistered:
		var payload ProjectRegisteredPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		if err := validateIdentifier("payload.manifest_record_id", payload.ManifestRecordID, true); err != nil {
			return err
		}
		return validateAuthority(payload.Authority)
	case EventWorkPackageCreated:
		var payload WorkPackageCreatedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		return validateIdentifier("payload.definition_record_id", payload.DefinitionRecordID, true)
	case EventWorkPackageStateChanged:
		var payload WorkPackageStateChangedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		if !validWorkPackageState(payload.From) || !validWorkPackageState(payload.To) || payload.From == payload.To {
			return errors.New("payload requires distinct supported work package states")
		}
		return validateIdentifier("payload.reason_code", payload.ReasonCode, false)
	case EventExecutionStarted:
		var payload ExecutionStartedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		return validateIdentifier("payload.manifest_record_id", payload.ManifestRecordID, true)
	case EventExecutionPaused:
		var payload ExecutionPausedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		return validateIdentifier("payload.reason_code", payload.ReasonCode, true)
	case EventExecutionFailed:
		var payload ExecutionFailedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		if err := validateIdentifier("payload.failure_code", payload.FailureCode, true); err != nil {
			return err
		}
		return validateText("payload.summary", payload.Summary, MaxTextBytes, false)
	case EventExecutionCompleted:
		var payload ExecutionCompletedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		return validateText("payload.summary", payload.Summary, MaxTextBytes, false)
	case EventTestRecorded:
		var payload TestRecordedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		if err := validateText("payload.name", payload.Name, MaxNameBytes, true); err != nil {
			return err
		}
		if !validTestOutcome(payload.Outcome) {
			return fmt.Errorf("unsupported test outcome %q", payload.Outcome)
		}
		if payload.DurationMilliseconds < 0 {
			return errors.New("test duration cannot be negative")
		}
		evidenceFields := payload.CommandID != "" || payload.CommandDigest != "" || payload.ExitCode != nil || payload.EvidenceID != "" || payload.EvidenceDigest != ""
		if evidenceFields {
			if err := validateIdentifier("payload.command_id", payload.CommandID, true); err != nil {
				return err
			}
			if !validSHA256(payload.CommandDigest) {
				return errors.New("test evidence requires a valid command digest")
			}
			if payload.ExitCode == nil || *payload.ExitCode < 0 {
				return errors.New("test evidence requires a non-negative exit code")
			}
			if err := validateIdentifier("payload.evidence_id", payload.EvidenceID, true); err != nil {
				return err
			}
			if !validSHA256(payload.EvidenceDigest) {
				return errors.New("test evidence requires a valid evidence digest")
			}
		}
		return nil
	case EventHandoffCreated:
		var payload HandoffCreatedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		return validateIdentifier("payload.handoff_record_id", payload.HandoffRecordID, true)
	case EventReviewRecorded:
		var payload ReviewRecordedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		if !validReviewOutcome(payload.Outcome) {
			return fmt.Errorf("unsupported review outcome %q", payload.Outcome)
		}
		if err := validateIdentifier("payload.reviewer_id", payload.ReviewerID, true); err != nil {
			return err
		}
		return validateText("payload.summary", payload.Summary, MaxTextBytes, false)
	case EventArtifactRecorded:
		var payload ArtifactRecordedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		if err := validateIdentifier("payload.artifact_record_id", payload.ArtifactRecordID, true); err != nil {
			return err
		}
		return validateIdentifier("payload.artifact_id", payload.ArtifactID, true)
	case EventWorkAccepted:
		var payload WorkAcceptedPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		if err := validateIdentifier("payload.accepted_by", payload.AcceptedBy, true); err != nil {
			return err
		}
		return validateText("payload.summary", payload.Summary, MaxTextBytes, false)
	case EventTelemetryRecorded:
		var payload TelemetrySummaryPayload
		if err := decodeClosedPayload(event.Payload, &payload); err != nil {
			return err
		}
		return validateTelemetrySummaryPayload(payload, event.WorkPackageID, event.ExecutionID)
	default:
		return fmt.Errorf("unsupported event type %q", event.EventType)
	}
}

func validateTelemetrySummaryPayload(payload TelemetrySummaryPayload, workPackageID, executionID string) error {
	if payload.Schema != "syncgate.telemetry-summary.v1" || !validTelemetryIdentifier(payload.TelemetryID, "telemetry:") ||
		!validSHA256Digest(payload.TelemetryDigest) || !validTelemetryIdentifier(payload.Contract.ID, "contract:") || payload.Contract.Version < 1 || !validSHA256Digest(payload.Contract.Digest) ||
		!validSHA256Digest(payload.ProjectRevision) || !validTelemetryIdentifier(payload.TaskID, "task:") || payload.TaskRevision < 1 || !validTelemetryIdentifier(payload.TaskRecordID, "") ||
		!validSHA256Digest(payload.TaskDigest) || payload.WorkPackageID != workPackageID || !validTelemetryIdentifier(payload.WorkPackageRecordID, "") || !validSHA256Digest(payload.WorkPackageDigest) ||
		executionID == "" || !validTelemetryIdentifier(payload.GraphRecordID, "") || payload.GraphRevision < 1 || !validSHA256Digest(payload.GraphDigest) || !validSHA256Digest(payload.ContextDigest) ||
		!validTelemetryRegistry(payload.Trade, "trade:") || !validTelemetryRegistry(payload.Worker, "worker:") ||
		!validTelemetryBinding(payload.Instruction, "instruction:") || !validTelemetryBinding(payload.Runtime, "runtime:") || !validTelemetryBinding(payload.Provider, "provider:") ||
		!validTelemetryBinding(payload.Model, "model:") || !validTelemetryBinding(payload.Node, "node:") ||
		(payload.FinalOutcome != "succeeded" && payload.FinalOutcome != "failed" && payload.FinalOutcome != "canceled" && payload.FinalOutcome != "partial") ||
		len(payload.Observations) > 32 || len(payload.Evidence) > 64 {
		return errors.New("invalid telemetry summary payload")
	}
	for _, value := range payload.Observations {
		if !validTelemetryObservation(value) {
			return errors.New("invalid telemetry observation")
		}
	}
	for _, value := range payload.Evidence {
		if !validTelemetryIdentifier(value.ID, "evidence:") || !validSHA256Digest(value.Digest) || !validTelemetryEvidenceKind(value.Kind) || !validTelemetrySource(value.Source) {
			return errors.New("invalid telemetry evidence reference")
		}
	}
	return nil
}

func validTelemetryRegistry(value RegistryReference, prefix string) bool {
	return validTelemetryIdentifier(value.ID, prefix) && value.Version > 0 && validSHA256Digest(value.Digest)
}
func validTelemetryBinding(value TelemetryBindingReference, prefix string) bool {
	return validTelemetryIdentifier(value.ID, prefix) && value.Version > 0 && validSHA256Digest(value.Digest)
}
func validSHA256Digest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
func validTelemetryIdentifier(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validateIdentifier("telemetry identifier", value, true) == nil
}
func validTelemetrySource(value string) bool {
	return value == "provider_reported" || value == "locally_measured" || value == "worker_claimed" || value == "reviewer_verified"
}
func validTelemetryEvidenceKind(value string) bool {
	return value == "artifact" || value == "test" || value == "review" || value == "runtime" || value == "result"
}
func validTelemetryObservation(value TelemetryObservation) bool {
	if !validTelemetrySource(value.Source) || (value.Value != nil && *value.Value < 0) {
		return false
	}
	switch value.Name {
	case "duration_milliseconds", "queue_milliseconds", "active_runtime_milliseconds", "gate_milliseconds", "review_milliseconds", "human_wait_milliseconds", "input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "provider_cost_micros", "tool_calls", "files_inspected", "files_changed", "tests_run", "tests_failed", "retries", "runtime_errors", "tool_errors", "review_findings", "rework_cycles", "conflicts", "interventions", "rollbacks", "context_bytes":
		return true
	default:
		return false
	}
}

func decodeClosedPayload(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("event payload is required")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return fmt.Errorf("invalid event payload: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid event payload: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("event payload must contain one JSON value")
		}
		return fmt.Errorf("invalid event payload trailing data: %w", err)
	}
	return nil
}

func validateIdentifier(name, value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > MaxIdentifierBytes || !utf8.ValidString(value) || !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s must be a valid identifier of at most %d bytes", name, MaxIdentifierBytes)
	}
	return nil
}

func validateText(name, value string, limit int, required bool) error {
	if strings.TrimSpace(value) == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		if value != "" {
			return fmt.Errorf("%s cannot contain only whitespace", name)
		}
		return nil
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > limit {
		return fmt.Errorf("%s must be valid text of at most %d bytes", name, limit)
	}
	return nil
}

func validateIdentifierList(name string, values []string) error {
	if len(values) > MaxListItems {
		return fmt.Errorf("%s has more than %d items", name, MaxListItems)
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := validateIdentifier(fmt.Sprintf("%s[%d]", name, index), value, true); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateTextList(name string, values []string, required bool) error {
	if required && len(values) == 0 {
		return fmt.Errorf("%s requires at least one item", name)
	}
	if len(values) > MaxListItems {
		return fmt.Errorf("%s has more than %d items", name, MaxListItems)
	}
	for index, value := range values {
		if err := validateText(fmt.Sprintf("%s[%d]", name, index), value, MaxTextBytes, true); err != nil {
			return err
		}
	}
	return nil
}

func validatePathList(name string, values []string) error {
	if len(values) > MaxListItems {
		return fmt.Errorf("%s has more than %d items", name, MaxListItems)
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := validateRelativePath(fmt.Sprintf("%s[%d]", name, index), value); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateScopePathList(name string, values []string) error {
	if len(values) > MaxListItems {
		return fmt.Errorf("%s has more than %d items", name, MaxListItems)
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if value != "." {
			if err := validateRelativePath(fmt.Sprintf("%s[%d]", name, index), value); err != nil {
				return err
			}
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateRelativePath(name, value string) error {
	if len(value) > MaxRelativePathBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, MaxRelativePathBytes)
	}
	normalized, err := filesystem.NormalizeRelativePath(value)
	if err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	if normalized != value {
		return fmt.Errorf("%s must use canonical forward-slash form %q", name, normalized)
	}
	return nil
}

func validateUTCTime(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", name)
	}
	_, offset := value.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must use UTC", name)
	}
	return nil
}

func validateMediaType(value string) error {
	if err := validateText("media_type", value, MaxMediaTypeBytes, true); err != nil {
		return err
	}
	parsed, parameters, err := mime.ParseMediaType(value)
	if err != nil || parsed != value || len(parameters) != 0 || strings.Count(value, "/") != 1 {
		return errors.New("media_type must be a type/subtype without parameters")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validExecutionState(value ExecutionState) bool {
	switch value {
	case ExecutionPending, ExecutionRunning, ExecutionPaused, ExecutionFailed, ExecutionCompleted:
		return true
	default:
		return false
	}
}

func validWorkPackageState(value WorkPackageState) bool {
	switch value {
	case WorkPackagePlanned, WorkPackageReady, WorkPackageInProgress, WorkPackageBlocked,
		WorkPackageReview, WorkPackageAccepted, WorkPackageFailed, WorkPackageCanceled:
		return true
	default:
		return false
	}
}

func validTestOutcome(value TestOutcome) bool {
	return value == TestPassed || value == TestFailed || value == TestSkipped
}

func validReviewOutcome(value ReviewOutcome) bool {
	return value == ReviewApproved || value == ReviewRejected || value == ReviewChangesRequested
}

func validConfidence(value Confidence) bool {
	return value == ConfidenceLow || value == ConfidenceMedium || value == ConfidenceHigh
}
