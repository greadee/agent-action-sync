package core

import "time"

type TransferState string

const (
	TransferQueued            TransferState = "queued"
	TransferAuthorizing       TransferState = "authorizing"
	TransferOffered           TransferState = "offered"
	TransferNegotiatingChunks TransferState = "negotiating_chunks"
	TransferTransferring      TransferState = "transferring"
	TransferPaused            TransferState = "paused"
	TransferVerifying         TransferState = "verifying"
	TransferCommitting        TransferState = "committing"
	TransferCompleted         TransferState = "completed"
	TransferFailed            TransferState = "failed"
	TransferCanceled          TransferState = "canceled"
)

type TransferDirection string

const (
	TransferSend    TransferDirection = "send"
	TransferReceive TransferDirection = "receive"
)

type Transfer struct {
	ID            TransferID
	Direction     TransferDirection
	PeerDeviceID  DeviceID
	ShareID       ShareID
	RelativePath  string
	State         TransferState
	Size          int64
	ChunkSize     int64
	ContentHash   string
	HashAlgorithm string
	BytesVerified int64
	RetryCount    int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   time.Time
	LastError     string
}

type ChunkState string

const (
	ChunkPending  ChunkState = "pending"
	ChunkReceived ChunkState = "received"
	ChunkVerified ChunkState = "verified"
	ChunkRejected ChunkState = "rejected"
)

type TransferChunk struct {
	TransferID TransferID
	Index      int64
	Offset     int64
	Size       int64
	Hash       string
	State      ChunkState
	VerifiedAt time.Time
}
