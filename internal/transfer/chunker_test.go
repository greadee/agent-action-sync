package transfer

import "testing"

func TestPlanFixedChunks(t *testing.T) {
	chunks, err := PlanFixedChunks(10, 4)
	if err != nil {
		t.Fatalf("PlanFixedChunks: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3", len(chunks))
	}
	if chunks[0].Offset != 0 || chunks[0].Size != 4 {
		t.Fatalf("chunk 0 = %+v", chunks[0])
	}
	if chunks[2].Offset != 8 || chunks[2].Size != 2 {
		t.Fatalf("chunk 2 = %+v", chunks[2])
	}
}

func TestPlanFixedChunksZeroByteFile(t *testing.T) {
	chunks, err := PlanFixedChunks(0, DefaultChunkSize)
	if err != nil {
		t.Fatalf("PlanFixedChunks: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Size != 0 {
		t.Fatalf("zero-byte chunks = %+v", chunks)
	}
}

func TestPlanFixedChunksRejectsInvalidInput(t *testing.T) {
	if _, err := PlanFixedChunks(-1, DefaultChunkSize); err == nil {
		t.Fatal("expected negative file size to be rejected")
	}
	if _, err := PlanFixedChunks(1, 0); err == nil {
		t.Fatal("expected zero chunk size to be rejected")
	}
}
