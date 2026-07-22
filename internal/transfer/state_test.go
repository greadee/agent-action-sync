package transfer

import (
	"testing"

	"syncgate/internal/core"
)

func TestValidateTransitionAllowsHappyPath(t *testing.T) {
	path := []core.TransferState{
		core.TransferQueued,
		core.TransferAuthorizing,
		core.TransferOffered,
		core.TransferNegotiatingChunks,
		core.TransferTransferring,
		core.TransferVerifying,
		core.TransferCommitting,
		core.TransferCompleted,
	}

	for i := 0; i < len(path)-1; i++ {
		if err := ValidateTransition(path[i], path[i+1]); err != nil {
			t.Fatalf("transition %q -> %q: %v", path[i], path[i+1], err)
		}
	}
}

func TestValidateTransitionRejectsInvalidMove(t *testing.T) {
	if err := ValidateTransition(core.TransferQueued, core.TransferCompleted); err == nil {
		t.Fatal("expected queued -> completed to be rejected")
	}
}
