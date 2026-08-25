package workhistory

import (
	"context"
	"strings"
	"testing"

	"syncgate/internal/project"
)

func TestWorkHistoryRecordsResolvedRegistryReferencesWithoutRegistryConfiguration(t *testing.T) {
	fixture := newHistoryFixture(t)
	trade := &project.RegistryReference{ID: "trade:go", Version: 1, Digest: strings.Repeat("a", 64)}
	worker := &project.RegistryReference{ID: "worker:go-openai", Version: 2, Digest: strings.Repeat("b", 64)}
	contract := &project.RegistryReference{ID: "contract:execution-registry", Version: 3, Digest: strings.Repeat("c", 64)}
	if _, err := fixture.service.CreateWorkPackage(context.Background(), CreateWorkPackageRequest{
		Metadata: fixture.metadata("registry-work", 1), WorkPackageID: "wp-registry", Objective: "registry-bound work", Trade: "go",
		TradeReference: trade, Scope: project.WorkScope{Allowed: []string{"internal"}}, Deliverables: []string{"code"}, AcceptanceCriteria: []string{"tests pass"},
	}); err != nil {
		t.Fatal(err)
	}
	layout, err := project.NewLayout(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := project.WorkPackageDefinitionRelativePath("wp-registry")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := project.ReadPortableRecord(layout, path)
	if err != nil {
		t.Fatal(err)
	}
	definition := decoded.Value.(*project.WorkPackageDefinition)
	if definition.TradeReference == nil || *definition.TradeReference != *trade {
		t.Fatalf("trade reference=%+v", definition.TradeReference)
	}
	if strings.Contains(string(decoded.Canonical), "credential") || strings.Contains(string(decoded.Canonical), "secret") {
		t.Fatalf("portable definition contains registry configuration: %s", decoded.Canonical)
	}

	transitionTaskWork(t, fixture, "wp-registry", "registry-ready", project.WorkPackagePlanned, project.WorkPackageReady, 2)
	transitionTaskWork(t, fixture, "wp-registry", "registry-start", project.WorkPackageReady, project.WorkPackageInProgress, 3)
	if _, err := fixture.service.StartExecution(context.Background(), StartExecutionRequest{Metadata: fixture.metadata("registry-execution", 4), WorkPackageID: "wp-registry", ExecutionID: "execution-registry", TradeReference: trade, WorkerReference: worker, ContractReference: contract}); err != nil {
		t.Fatal(err)
	}
	executionPath, err := project.ExecutionManifestRelativePath("execution-registry")
	if err != nil {
		t.Fatal(err)
	}
	executionRecord, err := project.ReadPortableRecord(layout, executionPath)
	if err != nil {
		t.Fatal(err)
	}
	execution := executionRecord.Value.(*project.ExecutionManifest)
	if execution.TradeReference == nil || execution.WorkerReference == nil || execution.ContractReference == nil || *execution.TradeReference != *trade || *execution.WorkerReference != *worker || *execution.ContractReference != *contract {
		t.Fatalf("execution authority refs=%+v %+v %+v", execution.TradeReference, execution.WorkerReference, execution.ContractReference)
	}
}
