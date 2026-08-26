// Package contextcompiler builds deterministic, bounded context bundles from
// explicit project sources. It never selects or starts a runtime.
package contextcompiler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"syncgate/internal/filesystem"
	"syncgate/internal/project"
	"syncgate/internal/workhistory"
)

const (
	CompilerVersion       = "context-compiler:v1"
	DefaultMaxSourceBytes = 256 << 10
	DefaultMaxBundleBytes = 1 << 20
	DefaultMaxTokens      = 50_000
	MaxSources            = project.MaxListItems
	CacheDirectory        = project.LocalDirectory + "/context-cache"
)

var (
	ErrInvalidRequest = errors.New("invalid context compiler request")
	ErrScopeViolation = errors.New("context source violates work-package scope")
	ErrBudgetExceeded = errors.New("context bundle budget exceeded")
)

type SourceKind string
type PrivacyClass string

const (
	SourceRepositoryFile  SourceKind = "repository_file"
	SourceRepositoryDoc   SourceKind = "repository_doc"
	SourceADR             SourceKind = "adr"
	SourceProjectHistory  SourceKind = "project_history"
	SourceTaskGraph       SourceKind = "task_graph"
	SourceTestExpectation SourceKind = "test_expectation"

	PrivacyPublic    PrivacyClass = "public"
	PrivacyProject   PrivacyClass = "project"
	PrivacySensitive PrivacyClass = "sensitive"
	PrivacySecret    PrivacyClass = "secret"
)

type SourceSpec struct {
	RelativePath string       `json:"relative_path"`
	Kind         SourceKind   `json:"kind"`
	Privacy      PrivacyClass `json:"privacy"`
	TradeIDs     []string     `json:"trade_ids,omitempty"`
}

type Request struct {
	ProjectID      string
	WorkPackageID  string
	TradeReference project.RegistryReference
	Sources        []SourceSpec
	MaxSourceBytes int64
	MaxBundleBytes int64
	MaxTokens      int64
}

type SourceIndexEntry struct {
	RelativePath string       `json:"relative_path"`
	Kind         SourceKind   `json:"kind"`
	Privacy      PrivacyClass `json:"privacy"`
	Digest       string       `json:"digest"`
	Bytes        int64        `json:"bytes"`
	Redacted     bool         `json:"redacted"`
}

type Notice struct {
	RelativePath string `json:"relative_path"`
	Code         string `json:"code"`
}
type Manifest struct {
	CompilerVersion string                    `json:"compiler_version"`
	ProjectID       string                    `json:"project_id"`
	WorkPackageID   string                    `json:"work_package_id"`
	TradeReference  project.RegistryReference `json:"trade_reference"`
	Sources         []SourceIndexEntry        `json:"sources"`
	Omissions       []Notice                  `json:"omissions"`
	Warnings        []Notice                  `json:"warnings"`
	EstimatedTokens int64                     `json:"estimated_tokens"`
	ContextDigest   string                    `json:"context_digest"`
}
type Bundle struct {
	Manifest Manifest           `json:"manifest"`
	Index    []SourceIndexEntry `json:"index"`
	Briefing string             `json:"briefing"`
}
type Result struct {
	Bundle Bundle
	Bytes  []byte
}

// VerifyBundleBytes checks canonical bundle bytes against the context digest
// that an immutable execution contract authorized.
func VerifyBundleBytes(raw []byte, expectedDigest string) error {
	if len(raw) == 0 || len(raw) > DefaultMaxBundleBytes || expectedDigest == "" {
		return ErrInvalidRequest
	}
	var bundle Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return ErrInvalidRequest
	}
	result := Result{Bundle: bundle, Bytes: append([]byte(nil), raw...)}
	if err := validateResult(result); err != nil || bundle.Manifest.ContextDigest != expectedDigest {
		return ErrInvalidRequest
	}
	return nil
}

type ArtifactPublisher interface {
	RegisterArtifact(context.Context, workhistory.RegisterArtifactRequest) (workhistory.OperationResult, error)
}
type PublishRequest struct {
	Metadata          workhistory.Metadata
	ArtifactID        string
	Name              string
	ExecutionID       string
	SourceArtifactIDs []string
}

func Compile(ctx context.Context, layout project.Layout, request Request) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	request = defaults(request)
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	definitionPath, err := project.WorkPackageDefinitionRelativePath(request.WorkPackageID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: work package identity", ErrInvalidRequest)
	}
	decoded, err := project.ReadPortableRecord(layout, definitionPath)
	if err != nil {
		return Result{}, err
	}
	definition, ok := decoded.Value.(*project.WorkPackageDefinition)
	if !ok || definition.ProjectID != request.ProjectID {
		return Result{}, fmt.Errorf("%w: work package project mismatch", ErrInvalidRequest)
	}
	if definition.TradeReference != nil && *definition.TradeReference != request.TradeReference {
		return Result{}, fmt.Errorf("%w: resolved trade does not match work package", ErrInvalidRequest)
	}

	sources := append([]SourceSpec(nil), request.Sources...)
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].RelativePath == sources[j].RelativePath {
			return sources[i].Kind < sources[j].Kind
		}
		return sources[i].RelativePath < sources[j].RelativePath
	})
	manifest := Manifest{CompilerVersion: CompilerVersion, ProjectID: request.ProjectID, WorkPackageID: request.WorkPackageID, TradeReference: request.TradeReference}
	contents := make([]compiledSource, 0, len(sources)+1)
	contents = append(contents, compiledSource{entry: SourceIndexEntry{RelativePath: definitionPath, Kind: SourceProjectHistory, Privacy: PrivacyProject, Digest: decoded.Digest, Bytes: int64(len(decoded.Canonical))}, content: string(decoded.Canonical)})
	seen := map[string]bool{definitionPath: true}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if seen[source.RelativePath] {
			manifest.Omissions = append(manifest.Omissions, Notice{source.RelativePath, "duplicate_source"})
			continue
		}
		seen[source.RelativePath] = true
		if !tradeApplies(source.TradeIDs, request.TradeReference.ID) {
			manifest.Omissions = append(manifest.Omissions, Notice{source.RelativePath, "trade_irrelevant"})
			continue
		}
		compiled, notices, err := compileSource(layout, request, definition, source)
		if err != nil {
			return Result{}, err
		}
		for _, notice := range notices {
			if notice.Code == "content_redacted" {
				manifest.Warnings = append(manifest.Warnings, notice)
			} else {
				manifest.Omissions = append(manifest.Omissions, notice)
			}
		}
		if compiled != nil {
			contents = append(contents, *compiled)
		}
	}
	sort.Slice(contents, func(i, j int) bool { return contents[i].entry.RelativePath < contents[j].entry.RelativePath })
	sortNotices(manifest.Omissions)
	sortNotices(manifest.Warnings)
	var briefing strings.Builder
	briefing.WriteString("# Project Context\n\n")
	briefing.WriteString("Project: " + request.ProjectID + "\nWork package: " + request.WorkPackageID + "\nTrade: " + request.TradeReference.ID + "@" + fmt.Sprint(request.TradeReference.Version) + "\n")
	var contentBytes int64
	for _, source := range contents {
		section := "\n## " + string(source.entry.Kind) + ": " + source.entry.RelativePath + "\n\n" + source.content + "\n"
		projected := contentBytes + int64(len(section))
		if projected > request.MaxBundleBytes || estimateTokens(projected) > request.MaxTokens {
			manifest.Omissions = append(manifest.Omissions, Notice{source.entry.RelativePath, "bundle_budget"})
			continue
		}
		briefing.WriteString(section)
		contentBytes = projected
		manifest.Sources = append(manifest.Sources, source.entry)
	}
	sortNotices(manifest.Omissions)
	manifest.EstimatedTokens = estimateTokens(int64(briefing.Len()))
	bundle := Bundle{Manifest: manifest, Index: append([]SourceIndexEntry(nil), manifest.Sources...), Briefing: briefing.String()}
	unsigned, err := json.Marshal(bundle)
	if err != nil {
		return Result{}, err
	}
	if int64(len(unsigned)) > request.MaxBundleBytes {
		return Result{}, ErrBudgetExceeded
	}
	bundle.Manifest.ContextDigest = digest(unsigned)
	raw, err := json.Marshal(bundle)
	if err != nil {
		return Result{}, err
	}
	if int64(len(raw)) > request.MaxBundleBytes {
		return Result{}, ErrBudgetExceeded
	}
	return Result{Bundle: bundle, Bytes: raw}, nil
}

func Cache(layout project.Layout, result Result) (string, error) {
	if err := validateResult(result); err != nil {
		return "", fmt.Errorf("%w: invalid bundle", ErrInvalidRequest)
	}
	relative := CacheDirectory + "/" + result.Bundle.Manifest.ContextDigest + ".json"
	destination, err := filesystem.EnsureParentDirectoriesInsideShare(layout.Root(), relative, 0o700)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		resolved, info, exists, inspectErr := layout.InspectManaged(relative)
		if inspectErr != nil || !exists || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%w: unsafe cache destination", ErrInvalidRequest)
		}
		existing, readErr := os.ReadFile(resolved)
		if readErr != nil {
			return "", readErr
		}
		if !bytes.Equal(existing, result.Bytes) {
			return "", project.ErrRecordConflict
		}
		return relative, nil
	}
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err = file.Write(result.Bytes); err != nil {
		_ = file.Close()
		return "", err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	ok = true
	return relative, nil
}

// BindExecutionContext makes the compiled digest the execution's portable
// provenance context_version without exposing cache paths or compiler internals.
func BindExecutionContext(result Result, request workhistory.StartExecutionRequest) (workhistory.StartExecutionRequest, error) {
	if err := validateResult(result); err != nil {
		return workhistory.StartExecutionRequest{}, err
	}
	request.ContextVersion = result.Bundle.Manifest.ContextDigest
	return request, nil
}

func PublishApproved(ctx context.Context, layout project.Layout, publisher ArtifactPublisher, result Result, request PublishRequest) (workhistory.OperationResult, error) {
	if publisher == nil {
		return workhistory.OperationResult{}, fmt.Errorf("%w: publisher is required", ErrInvalidRequest)
	}
	source, err := Cache(layout, result)
	if err != nil {
		return workhistory.OperationResult{}, err
	}
	if request.Name == "" {
		request.Name = "context-bundle.json"
	}
	request.Metadata.ContextVersion = result.Bundle.Manifest.ContextDigest
	return publisher.RegisterArtifact(ctx, workhistory.RegisterArtifactRequest{Metadata: request.Metadata, ArtifactID: request.ArtifactID, WorkPackageID: result.Bundle.Manifest.WorkPackageID, ExecutionID: request.ExecutionID, Name: request.Name, MediaType: "application/vnd.syncgate.context+json", SourceRelativePath: source, EmbedBlob: true, SourceArtifactIDs: request.SourceArtifactIDs})
}

type compiledSource struct {
	entry   SourceIndexEntry
	content string
}

func compileSource(layout project.Layout, request Request, definition *project.WorkPackageDefinition, source SourceSpec) (*compiledSource, []Notice, error) {
	if err := project.ValidateProjectRelativePath(source.RelativePath); err != nil {
		return nil, nil, fmt.Errorf("%w: source path", ErrInvalidRequest)
	}
	if !validKind(source.Kind) || !validPrivacy(source.Privacy) {
		return nil, nil, fmt.Errorf("%w: source classification", ErrInvalidRequest)
	}
	lower := strings.ToLower(source.RelativePath)
	if forbiddenContextPath(lower) {
		return nil, []Notice{{source.RelativePath, "forbidden_source"}}, nil
	}
	if source.Privacy == PrivacySecret {
		return nil, []Notice{{source.RelativePath, "secret_source"}}, nil
	}
	if source.Privacy == PrivacySensitive {
		return nil, []Notice{{source.RelativePath, "sensitive_source"}}, nil
	}
	var raw []byte
	var sourceDigest string
	if strings.HasPrefix(source.RelativePath, project.ControlDirectory+"/") {
		if source.Kind != SourceProjectHistory && source.Kind != SourceTaskGraph {
			return nil, nil, fmt.Errorf("%w: portable source kind", ErrInvalidRequest)
		}
		decoded, err := project.ReadPortableRecord(layout, source.RelativePath)
		if err != nil {
			if errors.Is(err, project.ErrRecordNotFound) {
				return nil, []Notice{{source.RelativePath, "source_missing"}}, nil
			}
			return nil, []Notice{{source.RelativePath, "source_invalid"}}, nil
		}
		if recordProjectID(decoded.Value) != request.ProjectID {
			return nil, nil, fmt.Errorf("%w: cross-project source", ErrInvalidRequest)
		}
		if !portableSourceAllowed(source.Kind, decoded.Value, definition) {
			return nil, []Notice{{source.RelativePath, "source_not_allowlisted"}}, nil
		}
		raw = decoded.Canonical
		sourceDigest = decoded.Digest
	} else {
		if !strings.HasPrefix(source.RelativePath, project.WorkspaceDirectory+"/") {
			return nil, []Notice{{source.RelativePath, "outside_workspace"}}, nil
		}
		if !scopeAllows(definition.Scope, strings.TrimPrefix(source.RelativePath, project.WorkspaceDirectory+"/")) {
			return nil, nil, fmt.Errorf("%w: %s", ErrScopeViolation, source.RelativePath)
		}
		resolved, err := layout.Resolve(source.RelativePath)
		if err != nil {
			return nil, []Notice{{source.RelativePath, "source_unsafe"}}, nil
		}
		info, err := os.Lstat(resolved)
		if errors.Is(err, os.ErrNotExist) {
			return nil, []Notice{{source.RelativePath, "source_missing"}}, nil
		}
		if err != nil || !info.Mode().IsRegular() {
			return nil, []Notice{{source.RelativePath, "source_unsafe"}}, nil
		}
		if info.Size() > request.MaxSourceBytes {
			return nil, []Notice{{source.RelativePath, "source_oversized"}}, nil
		}
		raw, err = os.ReadFile(resolved)
		if err != nil {
			return nil, []Notice{{source.RelativePath, "source_unreadable"}}, nil
		}
		sourceDigest = digest(raw)
	}
	if int64(len(raw)) > request.MaxSourceBytes {
		return nil, []Notice{{source.RelativePath, "source_oversized"}}, nil
	}
	if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
		return nil, []Notice{{source.RelativePath, "source_binary"}}, nil
	}
	if containsPrivateKey(raw) {
		return nil, []Notice{{source.RelativePath, "secret_content"}}, nil
	}
	content, redacted := redact(string(raw))
	entry := SourceIndexEntry{RelativePath: source.RelativePath, Kind: source.Kind, Privacy: source.Privacy, Digest: sourceDigest, Bytes: int64(len(raw)), Redacted: redacted}
	notices := []Notice{}
	if redacted {
		notices = append(notices, Notice{source.RelativePath, "content_redacted"})
	}
	return &compiledSource{entry: entry, content: content}, notices, nil
}

func defaults(r Request) Request {
	if r.MaxSourceBytes == 0 {
		r.MaxSourceBytes = DefaultMaxSourceBytes
	}
	if r.MaxBundleBytes == 0 {
		r.MaxBundleBytes = DefaultMaxBundleBytes
	}
	if r.MaxTokens == 0 {
		r.MaxTokens = DefaultMaxTokens
	}
	return r
}
func validateRequest(r Request) error {
	if r.ProjectID == "" || r.WorkPackageID == "" || !strings.HasPrefix(r.TradeReference.ID, "trade:") || r.TradeReference.Version < 1 || len(r.TradeReference.Digest) != 64 || len(r.Sources) > MaxSources || r.MaxSourceBytes < 1 || r.MaxBundleBytes < 1 || r.MaxTokens < 1 {
		return ErrInvalidRequest
	}
	return nil
}
func validKind(v SourceKind) bool {
	switch v {
	case SourceRepositoryFile, SourceRepositoryDoc, SourceADR, SourceProjectHistory, SourceTaskGraph, SourceTestExpectation:
		return true
	}
	return false
}
func validPrivacy(v PrivacyClass) bool {
	return v == PrivacyPublic || v == PrivacyProject || v == PrivacySensitive || v == PrivacySecret
}
func tradeApplies(ids []string, id string) bool {
	if len(ids) == 0 {
		return true
	}
	copy := append([]string(nil), ids...)
	sort.Strings(copy)
	for _, candidate := range copy {
		if candidate == id {
			return true
		}
	}
	return false
}
func scopeAllows(scope project.WorkScope, path string) bool {
	for _, forbidden := range scope.Forbidden {
		if within(path, forbidden) {
			return false
		}
	}
	for _, allowed := range append(append([]string{}, scope.Allowed...), scope.Inspect...) {
		if within(path, allowed) {
			return true
		}
	}
	return false
}
func within(path, parent string) bool {
	if parent == "." {
		return true
	}
	return path == parent || strings.HasPrefix(path, strings.TrimSuffix(parent, "/")+"/")
}
func forbiddenContextPath(path string) bool {
	for _, part := range []string{".git/", ".env", ".secrets/", project.LocalDirectory + "/", "prompt", "transcript", "terminal-output", "terminal_output"} {
		if strings.Contains(path, part) {
			return true
		}
	}
	return false
}
func containsPrivateKey(raw []byte) bool {
	return bytes.Contains(bytes.ToUpper(raw), []byte("BEGIN PRIVATE KEY")) || bytes.Contains(bytes.ToUpper(raw), []byte("BEGIN OPENSSH PRIVATE KEY"))
}

var secretAssignment = regexp.MustCompile(`(?i)(password|token|api[_-]?key|secret)\s*[:=]\s*[^\s"']+`)
var environmentAssignment = regexp.MustCompile(`(?i)(home|userprofile|shell|aws_[a-z0-9_]+|openai_[a-z0-9_]+)\s*=\s*[^\s"']+`)
var windowsAbsolute = regexp.MustCompile(`[A-Za-z]:\\[^\s"']+`)
var unixAbsolute = regexp.MustCompile(`(?:^|\s)/(?:home|users|var|tmp|etc)/[^\s"']+`)

func redact(value string) (string, bool) {
	next := secretAssignment.ReplaceAllString(value, "$1=<redacted>")
	next = environmentAssignment.ReplaceAllString(next, "$1=<redacted>")
	next = windowsAbsolute.ReplaceAllString(next, "<absolute-path>")
	next = unixAbsolute.ReplaceAllString(next, " <absolute-path>")
	return next, next != value
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func digestWithoutContext(bundle Bundle) string {
	copy := bundle
	copy.Manifest.ContextDigest = ""
	raw, _ := json.Marshal(copy)
	return digest(raw)
}
func validateResult(result Result) error {
	expected, err := json.Marshal(result.Bundle)
	if err != nil || len(result.Bytes) == 0 || !bytes.Equal(expected, result.Bytes) || result.Bundle.Manifest.ContextDigest == "" || digestWithoutContext(result.Bundle) != result.Bundle.Manifest.ContextDigest {
		return ErrInvalidRequest
	}
	return nil
}
func estimateTokens(bytes int64) int64 { return (bytes + 3) / 4 }
func sortNotices(values []Notice) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].RelativePath == values[j].RelativePath {
			return values[i].Code < values[j].Code
		}
		return values[i].RelativePath < values[j].RelativePath
	})
}
func recordProjectID(value any) string {
	switch v := value.(type) {
	case *project.ProjectManifest:
		return v.ProjectID
	case *project.TaskRevision:
		return v.ProjectID
	case *project.DependencyGraphRevision:
		return v.ProjectID
	case *project.WorkPackageDefinition:
		return v.ProjectID
	case *project.ExecutionManifest:
		return v.ProjectID
	case *project.WorkEvent:
		return v.ProjectID
	case *project.Handoff:
		return v.ProjectID
	case *project.ArtifactManifest:
		return v.ProjectID
	}
	return ""
}

func portableSourceAllowed(kind SourceKind, value any, definition *project.WorkPackageDefinition) bool {
	switch kind {
	case SourceTaskGraph:
		switch record := value.(type) {
		case *project.TaskRevision:
			return definition.TaskID != "" && record.TaskID == definition.TaskID && record.Revision == definition.TaskRevision
		case *project.DependencyGraphRevision:
			return definition.TaskID != "" && record.TaskID == definition.TaskID && record.TaskRevision == definition.TaskRevision && record.Revision == definition.GraphRevision
		}
	case SourceProjectHistory:
		event, ok := value.(*project.WorkEvent)
		if !ok || event.WorkPackageID != definition.WorkPackageID {
			return false
		}
		switch event.EventType {
		case project.EventWorkPackageStateChanged, project.EventExecutionFailed, project.EventTestRecorded, project.EventReviewRecorded, project.EventWorkAccepted:
			return true
		}
	}
	return false
}
