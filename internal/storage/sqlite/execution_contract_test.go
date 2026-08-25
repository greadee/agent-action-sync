package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"syncgate/internal/storage"
)

func TestExecutionContractsAreImmutableIdempotentAndPaginated(t *testing.T) {
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-contract")
	ctx := context.Background()
	first := executionContractRecord(1, "a")
	result, err := store.ExecutionContracts().SaveExecutionContract(ctx, first)
	if err != nil || result.AlreadyPresent {
		t.Fatalf("save first=%+v err=%v", result, err)
	}
	result, err = store.ExecutionContracts().SaveExecutionContract(ctx, first)
	if err != nil || !result.AlreadyPresent {
		t.Fatalf("replay first=%+v err=%v", result, err)
	}
	changed := first
	changed.Digest = strings.Repeat("b", 64)
	if _, err := store.ExecutionContracts().SaveExecutionContract(ctx, changed); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("same-version mutation error=%v", err)
	}
	otherID := first
	otherID.ContractID = "contract:other"
	if _, err := store.ExecutionContracts().SaveExecutionContract(ctx, otherID); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate execution version error=%v", err)
	}

	second := executionContractRecord(2, "c")
	second.PredecessorDigest = first.Digest
	if _, err := store.ExecutionContracts().SaveExecutionContract(ctx, second); err != nil {
		t.Fatal(err)
	}
	page, err := store.ExecutionContracts().ListExecutionContracts(ctx, storage.ExecutionContractQuery{ProjectID: first.ProjectID, Page: storage.PageRequest{Limit: 1}})
	if err != nil || len(page.Items) != 1 || page.Items[0].Version != 1 || page.NextCursor == nil {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = store.ExecutionContracts().ListExecutionContracts(ctx, storage.ExecutionContractQuery{ProjectID: first.ProjectID, Page: storage.PageRequest{Limit: 1, Cursor: *page.NextCursor}})
	if err != nil || len(page.Items) != 1 || page.Items[0].Version != 2 || page.NextCursor != nil {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
}

func executionContractRecord(version int64, seed string) storage.ExecutionContractRecord {
	return storage.ExecutionContractRecord{
		ContractID: "contract:one", Version: version, ProjectID: "project-contract", TaskID: "task:one",
		TaskRevision: 1, GraphRevision: 1, WorkPackageID: "work:one", ExecutionID: "execution:one",
		Digest: strings.Repeat(seed, 64), ContractJSON: []byte(`{"schema":"syncgate.execution-contract.v1"}`),
		CreatedAt: time.Date(2026, time.August, 17, 18, int(version), 0, 0, time.UTC),
	}
}
