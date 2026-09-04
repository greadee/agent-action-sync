package desktop

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/executioncontract"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestBootstrapLocalRegistryIsDeterministicAndReplaySafe(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: core.ShareID("share-registry"), Name: "Registry", RootPath: t.TempDir(), Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, storage.ProjectRegistration{
		ProjectID: "project:registry", ShareID: core.ShareID("share-registry"), RootPath: t.TempDir(), Name: "Registry",
		AuthorityDeviceID: core.DeviceID("device:registry"), ManifestRecordID: "record:manifest", ManifestRecordHash: localHash("manifest"), ManifestPath: ".agent-project/manifest.json", RegisteredAt: localRegistryEpoch,
	}); err != nil {
		t.Fatal(err)
	}
	runtimeRef := executioncontract.BindingReference{ID: "runtime:codex-local", Version: 1, Digest: localHash("runtime")}
	now := func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	first, err := BootstrapLocalRegistry(ctx, store.Registry(), store.ProjectRegistrations(), runtimeRef, "codex", "gpt-5.6-sol", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BootstrapLocalRegistry(ctx, store.Registry(), store.ProjectRegistrations(), runtimeRef, "codex", "gpt-5.6-sol", now)
	if err != nil || second.TradeRef != first.TradeRef || second.WorkerRef != first.WorkerRef {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	if first.TradeRef.ID != localTradeID || !strings.HasPrefix(first.WorkerRef.ID, localWorkerID+"-") || len(first.TradeRef.Digest) != 64 || len(first.WorkerRef.Digest) != 64 {
		t.Fatalf("snapshot=%+v", first)
	}
}
