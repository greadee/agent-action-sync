package contextcompiler

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/workhistory"
)

func TestCompilerGoldenDeterminismBudgetsPrivacyAndTradeFiltering(t *testing.T) {
	fixture := newCompilerFixture(t)
	writeSource(t, fixture.root, "workspace/src/main.go", "package main\n// password=hunter2\n// C:\\Users\\owner\\secret.txt\n")
	writeSource(t, fixture.root, "workspace/docs/adr/decision.md", "# Decision\nUse deterministic context.\n")
	writeSource(t, fixture.root, "workspace/tests/expected.txt", "go test ./...\n")
	writeSource(t, fixture.root, "workspace/docs/other.md", "irrelevant trade material\n")
	writeSource(t, fixture.root, "workspace/src/binary.bin", string([]byte{'a', 0, 'b'}))
	writeSource(t, fixture.root, "workspace/src/large.txt", strings.Repeat("x", 2049))
	writeSource(t, fixture.root, "workspace/src/private.pem", "-----BEGIN PRIVATE KEY-----\nnot-real\n")
	writeSource(t, fixture.root, "workspace/src/sensitive.txt", "sensitive project note\n")
	writeSource(t, fixture.root, "workspace/.env", "TOKEN=do-not-include\n")

	sources := []SourceSpec{
		{RelativePath: fixture.graphPath, Kind: SourceTaskGraph, Privacy: PrivacyProject},
		{RelativePath: "workspace/tests/expected.txt", Kind: SourceTestExpectation, Privacy: PrivacyProject},
		{RelativePath: "workspace/docs/other.md", Kind: SourceRepositoryDoc, Privacy: PrivacyProject, TradeIDs: []string{"trade:other"}},
		{RelativePath: "workspace/src/missing.go", Kind: SourceRepositoryFile, Privacy: PrivacyProject},
		{RelativePath: "workspace/src/main.go", Kind: SourceRepositoryFile, Privacy: PrivacyProject},
		{RelativePath: "workspace/src/binary.bin", Kind: SourceRepositoryFile, Privacy: PrivacyProject},
		{RelativePath: "workspace/src/large.txt", Kind: SourceRepositoryFile, Privacy: PrivacyProject},
		{RelativePath: "workspace/src/private.pem", Kind: SourceRepositoryFile, Privacy: PrivacyProject},
		{RelativePath: "workspace/src/sensitive.txt", Kind: SourceRepositoryFile, Privacy: PrivacySensitive},
		{RelativePath: "workspace/.env", Kind: SourceRepositoryFile, Privacy: PrivacySecret},
		{RelativePath: "workspace/docs/adr/decision.md", Kind: SourceADR, Privacy: PrivacyPublic},
	}
	request := fixture.request(sources)
	request.MaxSourceBytes = 2048
	first, err := Compile(context.Background(), fixture.layout, request)
	if err != nil {
		t.Fatal(err)
	}
	reversed := append([]SourceSpec(nil), sources...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	secondRequest := fixture.request(reversed)
	secondRequest.MaxSourceBytes = 2048
	second, err := Compile(context.Background(), fixture.layout, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Bytes, second.Bytes) {
		t.Fatal("bundle bytes changed with source enumeration order")
	}
	if first.Bundle.Manifest.ContextDigest != "93a94fa5e04a7717b68f71d5bafc92cf850d29d881aa1637d9df007b32fa3ee6" {
		t.Fatalf("golden digest=%s", first.Bundle.Manifest.ContextDigest)
	}
	portable := string(first.Bytes)
	for _, forbidden := range []string{"hunter2", `C:\\Users\\owner`, "do-not-include", "BEGIN PRIVATE KEY", "irrelevant trade material", "sensitive project note"} {
		if strings.Contains(portable, forbidden) {
			t.Fatalf("bundle leaked %q", forbidden)
		}
	}
	if !strings.Contains(first.Bundle.Briefing, "password=<redacted>") || !strings.Contains(first.Bundle.Briefing, "<absolute-path>") {
		t.Fatalf("redaction missing: %s", first.Bundle.Briefing)
	}
	codes := map[string]bool{}
	for _, notice := range first.Bundle.Manifest.Omissions {
		codes[notice.Code] = true
	}
	for _, code := range []string{"trade_irrelevant", "source_missing", "source_binary", "source_oversized", "secret_content", "sensitive_source", "forbidden_source"} {
		if !codes[code] {
			t.Fatalf("missing omission %q: %+v", code, first.Bundle.Manifest.Omissions)
		}
	}
	cachePath, err := Cache(fixture.layout, first)
	if err != nil || !strings.HasPrefix(cachePath, project.LocalDirectory+"/context-cache/") {
		t.Fatalf("cache=%q err=%v", cachePath, err)
	}
	if replayPath, err := Cache(fixture.layout, second); err != nil || replayPath != cachePath {
		t.Fatalf("cache replay=%q err=%v", replayPath, err)
	}
}

func TestCompilerFailsClosedOnScopeAndOmitsSymlink(t *testing.T) {
	fixture := newCompilerFixture(t)
	writeSource(t, fixture.root, "workspace/outside/file.txt", "outside scope")
	_, err := Compile(context.Background(), fixture.layout, fixture.request([]SourceSpec{{RelativePath: "workspace/outside/file.txt", Kind: SourceRepositoryFile, Privacy: PrivacyProject}}))
	if err == nil || !strings.Contains(err.Error(), ErrScopeViolation.Error()) {
		t.Fatalf("scope error=%v", err)
	}

	writeSource(t, fixture.root, "workspace/src/target.txt", "target")
	symlink := filepath.Join(fixture.root, filepath.FromSlash("workspace/src/link.txt"))
	if err := os.Symlink(filepath.Join(fixture.root, filepath.FromSlash("workspace/src/target.txt")), symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := Compile(context.Background(), fixture.layout, fixture.request([]SourceSpec{{RelativePath: "workspace/src/link.txt", Kind: SourceRepositoryFile, Privacy: PrivacyProject}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Bundle.Manifest.Omissions) != 1 || result.Bundle.Manifest.Omissions[0].Code != "source_unsafe" {
		t.Fatalf("symlink omissions=%+v", result.Bundle.Manifest.Omissions)
	}
}

func TestCompilerAppliesDeterministicTokenBudget(t *testing.T) {
	fixture := newCompilerFixture(t)
	writeSource(t, fixture.root, "workspace/src/a.txt", strings.Repeat("a", 400))
	writeSource(t, fixture.root, "workspace/src/b.txt", strings.Repeat("b", 400))
	request := fixture.request([]SourceSpec{
		{RelativePath: "workspace/src/b.txt", Kind: SourceRepositoryFile, Privacy: PrivacyProject},
		{RelativePath: "workspace/src/a.txt", Kind: SourceRepositoryFile, Privacy: PrivacyProject},
	})
	request.MaxTokens = 40
	result, err := Compile(context.Background(), fixture.layout, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, notice := range result.Bundle.Manifest.Omissions {
		if notice.Code == "bundle_budget" {
			return
		}
	}
	t.Fatalf("budget omissions=%+v", result.Bundle.Manifest.Omissions)
}

func TestCompilerRecoversAfterCanceledCompileAndRestart(t *testing.T) {
	fixture := newCompilerFixture(t)
	writeSource(t, fixture.root, "workspace/src/main.go", "package main\n")
	request := fixture.request([]SourceSpec{{RelativePath: "workspace/src/main.go", Kind: SourceRepositoryFile, Privacy: PrivacyProject}})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Compile(canceled, fixture.layout, request); err != context.Canceled {
		t.Fatalf("canceled compile error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, filepath.FromSlash(CacheDirectory))); !os.IsNotExist(err) {
		t.Fatalf("canceled compile created cache state: %v", err)
	}
	first, err := Compile(context.Background(), fixture.layout, request)
	if err != nil {
		t.Fatal(err)
	}
	path, err := Cache(fixture.layout, first)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := project.NewLayout(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Compile(context.Background(), reopened, request)
	if err != nil {
		t.Fatal(err)
	}
	if pathAfterRestart, err := Cache(reopened, second); err != nil || pathAfterRestart != path || !reflect.DeepEqual(first, second) {
		t.Fatalf("restart recovery cache=%q err=%v first=%+v second=%+v", pathAfterRestart, err, first.Bundle.Manifest, second.Bundle.Manifest)
	}
}

func TestApprovedPublicationUsesAuthorityArtifactBoundaryAndContextDigest(t *testing.T) {
	fixture := newCompilerFixture(t)
	writeSource(t, fixture.root, "workspace/src/main.go", "package main\n")
	result, err := Compile(context.Background(), fixture.layout, fixture.request([]SourceSpec{{RelativePath: "workspace/src/main.go", Kind: SourceRepositoryFile, Privacy: PrivacyProject}}))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindExecutionContext(result, workhistory.StartExecutionRequest{})
	if err != nil || bound.ContextVersion != result.Bundle.Manifest.ContextDigest {
		t.Fatalf("bound execution=%+v err=%v", bound, err)
	}
	publisher := &capturingPublisher{}
	_, err = PublishApproved(context.Background(), fixture.layout, publisher, result, PublishRequest{Metadata: workhistory.Metadata{RootPath: fixture.root, IdempotencyKey: "publish-context", OccurredAt: fixture.when, Producer: project.Producer{DeviceID: "device-one"}}, ArtifactID: "artifact-context"})
	if err != nil {
		t.Fatal(err)
	}
	if publisher.request.ContextVersion != result.Bundle.Manifest.ContextDigest || publisher.request.WorkPackageID != fixture.workPackageID || !publisher.request.EmbedBlob || !strings.HasPrefix(publisher.request.SourceRelativePath, project.LocalDirectory+"/context-cache/") {
		t.Fatalf("publish request=%+v", publisher.request)
	}
}

type compilerFixture struct {
	root          string
	layout        project.Layout
	workPackageID string
	trade         project.RegistryReference
	graphPath     string
	when          time.Time
}

func newCompilerFixture(t *testing.T) compilerFixture {
	t.Helper()
	root := t.TempDir()
	when := time.Date(2026, time.August, 17, 13, 0, 0, 0, time.UTC)
	if _, err := project.BootstrapProject(project.ProjectBootstrapRequest{RootPath: root, ProjectID: "project-context", Name: "Context", Authority: project.Authority{DeviceID: "device-one", ShareID: "share-one"}}); err != nil {
		t.Fatal(err)
	}
	layout, err := project.NewLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	trade := project.RegistryReference{ID: "trade:go", Version: 1, Digest: strings.Repeat("a", 64)}
	producer := project.Producer{DeviceID: "device-one"}
	provenance := project.Provenance{Producer: producer, WorkPackageID: "wp-context", CreatedAt: when}
	definition := project.WorkPackageDefinition{RecordHeader: project.NewRecordHeader(project.RecordWorkPackage, "record-wp-context", "project-context"), WorkPackageID: "wp-context", Objective: "compile context", Trade: "go", Scope: project.WorkScope{Allowed: []string{"src"}, Inspect: []string{"docs", "tests"}, Forbidden: []string{"src/forbidden"}}, Deliverables: []string{"bundle"}, AcceptanceCriteria: []string{"deterministic"}, TaskID: "task:context", TaskRevision: 1, GraphRevision: 1, Priority: project.TaskPriorityNormal, TradeReference: &trade, CreatedAt: when, Provenance: provenance}
	if _, err := project.PublishRecord(layout, definition); err != nil {
		t.Fatal(err)
	}
	wpPath, _ := project.WorkPackageDefinitionRelativePath("wp-context")
	decoded, err := project.ReadPortableRecord(layout, wpPath)
	if err != nil {
		t.Fatal(err)
	}
	task := project.TaskRevision{RecordHeader: project.NewRecordHeader(project.RecordTaskRevision, "record-task-context", "project-context"), TaskID: "task:context", Revision: 1, Objective: "compile", Priority: project.TaskPriorityNormal, GraphRevision: 1, CreatedAt: when, Provenance: project.Provenance{Producer: producer, CreatedAt: when}}
	member := project.DependencyGraphMember{WorkPackageID: "wp-context", DefinitionRecordID: "record-wp-context", DefinitionDigest: decoded.Digest}
	graphDefinitions := map[string]orchestration.WorkPackageDefinition{"wp-context": {Record: definition, RecordDigest: decoded.Digest}}
	dependencyDigest, err := orchestration.DependencySetDigest(context.Background(), graphDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	graph := project.DependencyGraphRevision{RecordHeader: project.NewRecordHeader(project.RecordDependencyGraph, "record-graph-context", "project-context"), TaskID: "task:context", TaskRevision: 1, Revision: 1, Members: []project.DependencyGraphMember{member}, DependencySetDigest: dependencyDigest, CreatedAt: when, Provenance: project.Provenance{Producer: producer, CreatedAt: when}}
	if _, err := project.PublishRecord(layout, task); err != nil {
		t.Fatal(err)
	}
	if _, err := project.PublishRecord(layout, graph); err != nil {
		t.Fatal(err)
	}
	graphPath, _ := project.DependencyGraphRevisionRelativePath("task:context", 1, 1)
	return compilerFixture{root: root, layout: layout, workPackageID: "wp-context", trade: trade, graphPath: graphPath, when: when}
}
func (fixture compilerFixture) request(sources []SourceSpec) Request {
	return Request{ProjectID: "project-context", WorkPackageID: fixture.workPackageID, TradeReference: fixture.trade, Sources: sources}
}
func writeSource(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type capturingPublisher struct {
	request workhistory.RegisterArtifactRequest
}

func (publisher *capturingPublisher) RegisterArtifact(_ context.Context, request workhistory.RegisterArtifactRequest) (workhistory.OperationResult, error) {
	publisher.request = request
	return workhistory.OperationResult{}, nil
}
