package registry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestRegistryBindsOneTradeToDistinctWorkerProfilesAndPreservesUnknownScores(t *testing.T) {
	service := registryFixture(t, "project-registry")
	trade := saveTrade(t, service)
	profiles := []storage.WorkerProfile{
		workerProfile("worker:go-openai-a", trade),
		workerProfile("worker:go-openai-b", trade),
		workerProfile("worker:go-local", trade),
	}
	profiles[1].ToolPolicyVer = 2
	profiles[2].Provider, profiles[2].Model, profiles[2].ModelVersion = "local", "qwen-coder", "2026-08"
	for _, profile := range profiles {
		if _, err := service.SaveWorker(context.Background(), "device:local", profile); err != nil {
			t.Fatal(err)
		}
	}
	workers, err := service.Store.ListWorkerProfiles(context.Background(), storage.WorkerQuery{Page: storage.PageRequest{Limit: 10}, TradeID: trade.ID})
	byID := map[string]storage.WorkerProfile{}
	for _, item := range workers.Items {
		byID[item.WorkerID] = item
	}
	if err != nil || len(workers.Items) != 3 || byID["worker:go-openai-a"].TradeID != trade.ID || byID["worker:go-local"].Provider != "local" || byID["worker:go-openai-a"].Model != byID["worker:go-openai-b"].Model || byID["worker:go-openai-a"].WorkerID == byID["worker:go-openai-b"].WorkerID {
		t.Fatalf("workers=%+v err=%v", workers, err)
	}
	match := MatchCapabilities([]string{"cap:go"}, []string{"cap:go"}, []string{"cap:lint"})
	if len(match.MissingRequired) != 0 || len(match.MatchedOptional) != 0 || match.Score != nil || match.Exact {
		t.Fatalf("capability match invented a score or ranking: %+v", match)
	}
}

func TestRegistryImportReplayConflictAndProjectAdaptationAuthorization(t *testing.T) {
	service := registryFixture(t, "project-registry")
	trade := saveTrade(t, service)
	if _, err := service.SaveProjectAdaptation(context.Background(), "device:local", "project-registry", storage.ProjectTradeAdaptation{
		ProjectID: "project-registry", AdaptationID: "adaptation:go", Version: 1, TradeID: trade.ID, TradeVersion: trade.Version,
		Lifecycle: storage.RegistryActive, RequiredCapabilities: []string{"cap:go"}, CreatedAt: fixedRegistryTime,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveProjectAdaptation(context.Background(), "device:local", "project-other", storage.ProjectTradeAdaptation{
		ProjectID: "project-registry", AdaptationID: "adaptation:cross", Version: 1, TradeID: trade.ID, TradeVersion: trade.Version,
		Lifecycle: storage.RegistryActive, CreatedAt: fixedRegistryTime,
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-project adaptation error=%v", err)
	}

	bundle, err := service.Export(context.Background(), "project-registry")
	if err != nil {
		t.Fatal(err)
	}
	clone := registryFixture(t, "project-registry")
	if err := clone.Import(context.Background(), "device:local", "project-registry", bundle); err != nil {
		t.Fatal(err)
	}
	if err := clone.Import(context.Background(), "device:local", "project-registry", bundle); err != nil {
		t.Fatalf("idempotent import: %v", err)
	}
	var changed Bundle
	if err := json.Unmarshal(bundle, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Trades[0].Description = "different immutable content"
	changedBytes, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := clone.Import(context.Background(), "device:local", "project-registry", changedBytes); !errors.Is(err, ErrConflict) {
		t.Fatalf("same ID different content import error=%v", err)
	}
	if err := clone.Import(context.Background(), "device:local", "project-registry", append(bundle[:len(bundle)-1], []byte(`,"credential":"secret"}`)...)); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("secret-bearing unknown import error=%v", err)
	}
}

var fixedRegistryTime = time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)

func registryFixture(t *testing.T, projectID string) Service {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: core.ShareID("share-registry"), Name: "Registry", RootPath: t.TempDir(), Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, storage.ProjectRegistration{ProjectID: projectID, ShareID: "share-registry", RootPath: "registry-root", Name: "Registry Project", AuthorityDeviceID: "device:local", ManifestRecordID: "manifest-registry", ManifestRecordHash: strings.Repeat("a", 64), ManifestPath: ".agent-project/manifest.json", RegisteredAt: fixedRegistryTime}); err != nil {
		t.Fatal(err)
	}
	return Service{Store: store.Registry(), Projects: store.ProjectRegistrations(), Now: func() time.Time { return fixedRegistryTime }}
}

func saveTrade(t *testing.T, service Service) Reference {
	t.Helper()
	value, err := service.SaveTrade(context.Background(), "device:local", storage.TradeDefinition{TradeID: "trade:go", Version: 1, Name: "Go engineering", Lifecycle: storage.RegistryActive, CapabilityTags: []string{"cap:go", "cap:test"}, RequiredCapabilities: []string{"cap:go"}, OptionalCapabilities: []string{"cap:lint"}, Description: "Go implementation and review", CreatedAt: fixedRegistryTime})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func workerProfile(id string, trade Reference) storage.WorkerProfile {
	return storage.WorkerProfile{WorkerID: id, Version: 1, Name: id, Lifecycle: storage.RegistryActive, TradeID: trade.ID, TradeVersion: trade.Version, InstructionID: "instruction:go", InstructionVer: 1, RuntimeID: "runtime:disabled", RuntimeVersion: 1, Provider: "openai", Model: "gpt-5.6-terra", ModelVersion: "2026-08", ToolPolicyID: "tool-policy:go", ToolPolicyVer: 1, CapabilityTags: []string{"cap:go"}, CreatedAt: fixedRegistryTime}
}
