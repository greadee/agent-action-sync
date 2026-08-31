package nodestatus

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/storage"
)

const (
	Protocol         = "syncgate-node-status-v1"
	LegacyProtocol   = "syncgate-node-status-v0"
	MaxProjects      = 200
	MaxStatusCounts  = 16
	MaxSnapshotTTL   = 5 * time.Minute
	MaxClockSkew     = 5 * time.Minute
	MaxEnvelopeBytes = 96 << 10
)

// Compatibility identifies how much of a replica can be safely projected by
// the current control plane. Legacy replicas remain read-only observations.
func Compatibility(protocol string) string {
	switch protocol {
	case Protocol:
		return "current"
	case LegacyProtocol:
		return "downgraded"
	default:
		return "incompatible"
	}
}

func SupportedProtocol(protocol string) bool {
	return Compatibility(protocol) != "incompatible"
}

type StatusCount struct {
	State string `json:"state"`
	Count int64  `json:"count"`
}

type ProjectSummary struct {
	ProjectID                string        `json:"project_id"`
	DisplayName              string        `json:"display_name"`
	SchedulerState           string        `json:"scheduler_state"`
	AssignmentCounts         []StatusCount `json:"assignment_counts,omitempty"`
	GateCounts               []StatusCount `json:"gate_counts,omitempty"`
	AcceptedHistoryWatermark string        `json:"accepted_history_watermark,omitempty"`
}

type Snapshot struct {
	Protocol       string           `json:"protocol"`
	SourceDeviceID core.DeviceID    `json:"source_device_id"`
	Revision       int64            `json:"revision"`
	Watermark      string           `json:"watermark"`
	Health         string           `json:"health"`
	Lifecycle      string           `json:"lifecycle"`
	Projects       []ProjectSummary `json:"projects,omitempty"`
	ObservedAt     time.Time        `json:"observed_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
}

type Envelope struct {
	Snapshot  Snapshot `json:"snapshot"`
	Digest    string   `json:"digest"`
	Signature string   `json:"signature"`
}

type Receiver struct {
	Devices storage.DeviceStore
	Store   storage.NodeStatusStore
	Now     func() time.Time
}

func Build(deviceIdentity identity.DeviceIdentity, snapshot Snapshot) (Envelope, error) {
	if len(deviceIdentity.PublicKey) != ed25519.PublicKeySize || len(deviceIdentity.PrivateKey) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("valid Ed25519 signing identity is required")
	}
	if snapshot.SourceDeviceID != deviceIdentity.DeviceID {
		return Envelope{}, errors.New("snapshot source does not match signing identity")
	}
	if err := ValidateSnapshot(snapshot, snapshot.ObservedAt.UTC()); err != nil {
		return Envelope{}, err
	}
	payload, digest, err := encodeSnapshot(snapshot)
	if err != nil {
		return Envelope{}, err
	}
	signature := ed25519.Sign(deviceIdentity.PrivateKey, payload)
	return Envelope{Snapshot: snapshot, Digest: digest, Signature: base64.RawURLEncoding.EncodeToString(signature)}, nil
}

func EncodeEnvelope(envelope Envelope) ([]byte, error) {
	payload, err := json.Marshal(envelope)
	if err != nil || len(payload) == 0 || len(payload) > MaxEnvelopeBytes {
		return nil, errors.New("node status envelope is not serializable within bounds")
	}
	return payload, nil
}

func DecodeEnvelope(payload []byte) (Envelope, error) {
	if len(payload) == 0 || len(payload) > MaxEnvelopeBytes {
		return Envelope{}, errors.New("node status envelope is not bounded")
	}
	var envelope Envelope
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode node status envelope: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Envelope{}, errors.New("node status envelope has trailing data")
	}
	return envelope, nil
}

func DecodeSnapshot(payload []byte) (Snapshot, error) {
	if len(payload) == 0 || len(payload) > storage.MaxNodeStatusPayloadBytes {
		return Snapshot{}, errors.New("node status snapshot is not bounded")
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode node status snapshot: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("node status snapshot has trailing data")
	}
	return snapshot, nil
}

func (receiver Receiver) Apply(ctx context.Context, envelope Envelope) (storage.RegistryWriteResult, error) {
	if ctx == nil || receiver.Devices == nil || receiver.Store == nil {
		return storage.RegistryWriteResult{}, errors.New("node status receiver is not configured")
	}
	now := time.Now().UTC()
	if receiver.Now != nil {
		now = receiver.Now().UTC()
	}
	if err := ValidateSnapshot(envelope.Snapshot, now); err != nil {
		return storage.RegistryWriteResult{}, err
	}
	device, err := receiver.Devices.GetDevice(ctx, envelope.Snapshot.SourceDeviceID)
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("load paired node identity: %w", err)
	}
	if device.TrustState != storage.TrustTrusted {
		return storage.RegistryWriteResult{}, errors.New("node status source is not trusted")
	}
	payload, digest, err := encodeSnapshot(envelope.Snapshot)
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if digest != envelope.Digest {
		return storage.RegistryWriteResult{}, errors.New("node status digest does not match")
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || len(device.PublicKey) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(device.PublicKey), payload, signature) {
		return storage.RegistryWriteResult{}, errors.New("node status signature is invalid")
	}
	grant, err := receiver.Store.GetControlPlaneGrant(ctx, device.ID)
	if err != nil || !grant.ReadStatus || !grant.RevokedAt.IsZero() || !grant.ExpiresAt.After(now) {
		return storage.RegistryWriteResult{}, errors.New("paired node lacks an active read-only status grant")
	}
	return receiver.Store.SaveNodeStatusReplica(ctx, storage.NodeStatusReplica{
		DeviceID: device.ID, Revision: envelope.Snapshot.Revision, Watermark: envelope.Snapshot.Watermark,
		Protocol: envelope.Snapshot.Protocol, SnapshotJSON: payload, Digest: digest,
		ObservedAt: envelope.Snapshot.ObservedAt, ExpiresAt: envelope.Snapshot.ExpiresAt, ReceivedAt: now,
	})
}

func ValidateSnapshot(snapshot Snapshot, now time.Time) error {
	if !SupportedProtocol(snapshot.Protocol) {
		return errors.New("unsupported node status protocol")
	}
	if err := boundedID(string(snapshot.SourceDeviceID)); err != nil || snapshot.Revision < 1 || !digestValue(snapshot.Watermark) {
		return errors.New("node status identity, revision, or watermark is invalid")
	}
	if !allowed(snapshot.Health, "healthy", "degraded", "unhealthy", "unknown") || !allowed(snapshot.Lifecycle, "starting", "running", "draining", "stopped", "error") {
		return errors.New("node status health or lifecycle is invalid")
	}
	observedAt, expiresAt := snapshot.ObservedAt.UTC(), snapshot.ExpiresAt.UTC()
	if observedAt.IsZero() || expiresAt.IsZero() || !expiresAt.After(observedAt) || expiresAt.Sub(observedAt) > MaxSnapshotTTL || observedAt.After(now.UTC().Add(MaxClockSkew)) || !expiresAt.After(now.UTC()) {
		return errors.New("node status observation lifetime is invalid")
	}
	if len(snapshot.Projects) > MaxProjects {
		return errors.New("node status contains too many project summaries")
	}
	seen := make(map[string]bool, len(snapshot.Projects))
	for _, project := range snapshot.Projects {
		if err := validateProject(project); err != nil {
			return err
		}
		if seen[project.ProjectID] {
			return errors.New("node status contains duplicate project summaries")
		}
		seen[project.ProjectID] = true
	}
	return nil
}

func validateProject(project ProjectSummary) error {
	if boundedID(project.ProjectID) != nil || len(project.DisplayName) < 1 || len(project.DisplayName) > 128 || strings.TrimSpace(project.DisplayName) != project.DisplayName || strings.ContainsAny(project.DisplayName, `/\`) ||
		!allowed(project.SchedulerState, "running", "paused", "disabled", "draining", "not_selected", "unknown") {
		return errors.New("node status project summary is invalid")
	}
	if project.AcceptedHistoryWatermark != "" && !digestValue(project.AcceptedHistoryWatermark) {
		return errors.New("project accepted-history watermark is invalid")
	}
	if err := validateCounts(project.AssignmentCounts); err != nil {
		return err
	}
	return validateCounts(project.GateCounts)
}

func validateCounts(counts []StatusCount) error {
	if len(counts) > MaxStatusCounts {
		return errors.New("node status contains too many status counts")
	}
	seen := map[string]bool{}
	for _, count := range counts {
		if count.Count < 0 || len(count.State) < 1 || len(count.State) > 64 || strings.TrimSpace(count.State) != count.State || seen[count.State] {
			return errors.New("node status count is invalid")
		}
		seen[count.State] = true
	}
	return nil
}

func encodeSnapshot(snapshot Snapshot) ([]byte, string, error) {
	clone := snapshot
	clone.ObservedAt, clone.ExpiresAt = clone.ObservedAt.UTC(), clone.ExpiresAt.UTC()
	clone.Projects = append([]ProjectSummary(nil), clone.Projects...)
	sort.Slice(clone.Projects, func(i, j int) bool { return clone.Projects[i].ProjectID < clone.Projects[j].ProjectID })
	for index := range clone.Projects {
		clone.Projects[index].AssignmentCounts = sortedCounts(clone.Projects[index].AssignmentCounts)
		clone.Projects[index].GateCounts = sortedCounts(clone.Projects[index].GateCounts)
	}
	payload, err := json.Marshal(clone)
	if err != nil || len(payload) > storage.MaxNodeStatusPayloadBytes {
		return nil, "", errors.New("node status snapshot is not serializable within bounds")
	}
	hash := sha256.Sum256(payload)
	return payload, hex.EncodeToString(hash[:]), nil
}

func sortedCounts(source []StatusCount) []StatusCount {
	result := append([]StatusCount(nil), source...)
	sort.Slice(result, func(i, j int) bool { return result[i].State < result[j].State })
	return result
}

func boundedID(value string) error {
	if len(value) < 1 || len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, `/\`) {
		return errors.New("identifier is invalid")
	}
	return nil
}

func digestValue(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func allowed(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
