package transfer

import (
	"fmt"

	"syncgate/internal/core"
)

var allowedTransitions = map[core.TransferState]map[core.TransferState]bool{
	core.TransferQueued: {
		core.TransferAuthorizing: true,
		core.TransferCanceled:    true,
	},
	core.TransferAuthorizing: {
		core.TransferOffered:  true,
		core.TransferFailed:   true,
		core.TransferCanceled: true,
	},
	core.TransferOffered: {
		core.TransferNegotiatingChunks: true,
		core.TransferFailed:            true,
		core.TransferCanceled:          true,
	},
	core.TransferNegotiatingChunks: {
		core.TransferTransferring: true,
		core.TransferFailed:       true,
		core.TransferCanceled:     true,
	},
	core.TransferTransferring: {
		core.TransferPaused:    true,
		core.TransferVerifying: true,
		core.TransferFailed:    true,
		core.TransferCanceled:  true,
	},
	core.TransferPaused: {
		core.TransferTransferring: true,
		core.TransferCanceled:     true,
	},
	core.TransferVerifying: {
		core.TransferCommitting: true,
		core.TransferFailed:     true,
		core.TransferCanceled:   true,
	},
	core.TransferCommitting: {
		core.TransferCompleted: true,
		core.TransferFailed:    true,
	},
	core.TransferFailed: {
		core.TransferQueued:   true,
		core.TransferCanceled: true,
	},
}

func CanTransition(from, to core.TransferState) bool {
	return allowedTransitions[from][to]
}

func ValidateTransition(from, to core.TransferState) error {
	if CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("invalid transfer state transition from %q to %q", from, to)
}
