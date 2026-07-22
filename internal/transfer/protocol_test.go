package transfer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSendReceiveFile(t *testing.T) {
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "payload.bin")
	data := []byte("abcdefghijklmnopqrstuvwxyz")
	if err := os.WriteFile(sourcePath, data, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	var stream bytes.Buffer
	sendResult, err := SendFile(&stream, sourcePath, "payload.bin", 7)
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if sendResult.BytesSent != int64(len(data)) {
		t.Fatalf("bytes sent = %d", sendResult.BytesSent)
	}

	receiveResult, err := ReceiveFile(&stream, t.TempDir())
	if err != nil {
		t.Fatalf("ReceiveFile: %v", err)
	}
	got, err := os.ReadFile(receiveResult.DestinationPath)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("destination = %q, want %q", got, data)
	}
}

func TestReceiveFileRejectsCorruptedChunk(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "payload.bin")
	data := []byte("abcdefghijklmnopqrstuvwxyz")
	if err := os.WriteFile(sourcePath, data, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	var stream bytes.Buffer
	if _, err := SendFile(&stream, sourcePath, "payload.bin", 7); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	raw := stream.Bytes()
	raw[len(raw)-1] ^= 0xff

	if _, err := ReceiveFile(bytes.NewReader(raw), t.TempDir()); err == nil {
		t.Fatal("expected corrupted chunk to be rejected")
	}
}
