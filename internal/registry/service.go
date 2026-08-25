// Package registry owns local, provider-neutral trade and worker definitions.
// It has no portable-record, runtime, transport, or credential dependency.
package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"syncgate/internal/storage"
)

const bundleSchema = "syncgate.registry.v1"

var (
	ErrInvalidDefinition = errors.New("invalid registry definition")
	ErrConflict          = errors.New("registry immutable version conflict")
	ErrUnauthorized      = errors.New("registry project adaptation is unauthorized")
)

type Service struct {
	Store    storage.RegistryStore
	Projects storage.ProjectRegistrationStore
	Now      func() time.Time
}

type Reference struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
}
type CapabilityMatch struct {
	Exact           bool
	MissingRequired []string
	MatchedOptional []string
	Score           *float64
}
type Bundle struct {
	Schema      string                           `json:"schema"`
	Trades      []storage.TradeDefinition        `json:"trades"`
	Workers     []storage.WorkerProfile          `json:"workers"`
	Adaptations []storage.ProjectTradeAdaptation `json:"adaptations"`
}

func (service Service) SaveTrade(ctx context.Context, actorID string, value storage.TradeDefinition) (Reference, error) {
	if err := service.ready(ctx, actorID); err != nil {
		return Reference{}, err
	}
	normalizeTrade(&value)
	if err := validateTrade(value); err != nil {
		return Reference{}, err
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = service.now()
	}
	value.ContentHash = hash(value)
	result, err := service.Store.SaveTradeDefinition(ctx, value)
	if err != nil {
		return Reference{}, mapConflict(err)
	}
	if !result.AlreadyPresent {
		if err := service.audit(ctx, actorID, "create", "trade", value.TradeID, value.Version, "", value.ContentHash); err != nil {
			return Reference{}, err
		}
	}
	return Reference{ID: value.TradeID, Version: value.Version, Digest: value.ContentHash}, nil
}

func (service Service) SaveWorker(ctx context.Context, actorID string, value storage.WorkerProfile) (Reference, error) {
	if err := service.ready(ctx, actorID); err != nil {
		return Reference{}, err
	}
	normalizeWorker(&value)
	if err := validateWorker(value); err != nil {
		return Reference{}, err
	}
	trade, err := service.Store.GetTradeDefinition(ctx, value.TradeID, value.TradeVersion)
	if err != nil {
		return Reference{}, fmt.Errorf("%w: worker trade reference: %v", ErrInvalidDefinition, err)
	}
	if trade.Lifecycle == storage.RegistryDisabled {
		return Reference{}, fmt.Errorf("%w: worker cannot bind disabled trade", ErrInvalidDefinition)
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = service.now()
	}
	value.ContentHash = hash(value)
	result, err := service.Store.SaveWorkerProfile(ctx, value)
	if err != nil {
		return Reference{}, mapConflict(err)
	}
	if !result.AlreadyPresent {
		if err := service.audit(ctx, actorID, "create", "worker", value.WorkerID, value.Version, "", value.ContentHash); err != nil {
			return Reference{}, err
		}
	}
	return Reference{ID: value.WorkerID, Version: value.Version, Digest: value.ContentHash}, nil
}

func (service Service) SaveProjectAdaptation(ctx context.Context, actorID, authorizedProjectID string, value storage.ProjectTradeAdaptation) (Reference, error) {
	if err := service.ready(ctx, actorID); err != nil {
		return Reference{}, err
	}
	if authorizedProjectID == "" || value.ProjectID != authorizedProjectID {
		return Reference{}, ErrUnauthorized
	}
	if _, err := service.Projects.GetProject(ctx, authorizedProjectID); err != nil {
		return Reference{}, ErrUnauthorized
	}
	normalizeAdaptation(&value)
	if err := validateAdaptation(value); err != nil {
		return Reference{}, err
	}
	if _, err := service.Store.GetTradeDefinition(ctx, value.TradeID, value.TradeVersion); err != nil {
		return Reference{}, fmt.Errorf("%w: adaptation trade reference: %v", ErrInvalidDefinition, err)
	}
	if value.CreatedAt.IsZero() {
		value.CreatedAt = service.now()
	}
	value.ContentHash = hash(value)
	result, err := service.Store.SaveProjectTradeAdaptation(ctx, value)
	if err != nil {
		return Reference{}, mapConflict(err)
	}
	if !result.AlreadyPresent {
		if err := service.audit(ctx, actorID, "create", "project_trade_adaptation", value.AdaptationID, value.Version, value.ProjectID, value.ContentHash); err != nil {
			return Reference{}, err
		}
	}
	return Reference{ID: value.AdaptationID, Version: value.Version, Digest: value.ContentHash}, nil
}

func (service Service) Export(ctx context.Context, projectID string) ([]byte, error) {
	if err := service.ready(ctx, "exporter"); err != nil {
		return nil, err
	}
	trades, err := listAllTrades(ctx, service.Store)
	if err != nil {
		return nil, err
	}
	workers, err := listAllWorkers(ctx, service.Store)
	if err != nil {
		return nil, err
	}
	bundle := Bundle{Schema: bundleSchema, Trades: trades, Workers: workers}
	if projectID != "" {
		if _, err := service.Projects.GetProject(ctx, projectID); err != nil {
			return nil, ErrUnauthorized
		}
		bundle.Adaptations, err = listAllAdaptations(ctx, service.Store, projectID)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(bundle)
}

func (service Service) Import(ctx context.Context, actorID, authorizedProjectID string, raw []byte) error {
	if err := service.ready(ctx, actorID); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle Bundle
	if err := decoder.Decode(&bundle); err != nil {
		return fmt.Errorf("%w: invalid registry bundle", ErrInvalidDefinition)
	}
	if decoder.More() {
		return fmt.Errorf("%w: trailing registry bundle data", ErrInvalidDefinition)
	}
	if bundle.Schema != bundleSchema {
		return fmt.Errorf("%w: unsupported bundle schema", ErrInvalidDefinition)
	}
	sort.Slice(bundle.Trades, func(i, j int) bool { return tradeLess(bundle.Trades[i], bundle.Trades[j]) })
	sort.Slice(bundle.Workers, func(i, j int) bool { return workerLess(bundle.Workers[i], bundle.Workers[j]) })
	sort.Slice(bundle.Adaptations, func(i, j int) bool { return adaptationLess(bundle.Adaptations[i], bundle.Adaptations[j]) })
	for _, v := range bundle.Trades {
		if _, err := service.SaveTrade(ctx, actorID, v); err != nil {
			return err
		}
	}
	for _, v := range bundle.Workers {
		if _, err := service.SaveWorker(ctx, actorID, v); err != nil {
			return err
		}
	}
	for _, v := range bundle.Adaptations {
		if _, err := service.SaveProjectAdaptation(ctx, actorID, authorizedProjectID, v); err != nil {
			return err
		}
	}
	return nil
}

func MatchCapabilities(offered, required, optional []string) CapabilityMatch {
	offered = sorted(offered)
	required = sorted(required)
	optional = sorted(optional)
	available := map[string]bool{}
	for _, tag := range offered {
		available[tag] = true
	}
	result := CapabilityMatch{}
	for _, tag := range required {
		if !available[tag] {
			result.MissingRequired = append(result.MissingRequired, tag)
		}
	}
	for _, tag := range optional {
		if available[tag] {
			result.MatchedOptional = append(result.MatchedOptional, tag)
		}
	}
	result.Exact = len(result.MissingRequired) == 0 && len(result.MatchedOptional) == len(optional)
	return result
}

func (service Service) ready(ctx context.Context, actor string) error {
	if service.Store == nil || service.Projects == nil {
		return errors.New("registry service stores are required")
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(actor) {
		return fmt.Errorf("%w: actor id", ErrInvalidDefinition)
	}
	return nil
}
func (service Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}
func (service Service) audit(ctx context.Context, actor, action, kind, id string, version int64, projectID, digest string) error {
	return service.Store.RecordRegistryAudit(ctx, storage.RegistryAuditEvent{AuditID: "registry-audit:" + hash([]string{action, kind, id, fmt.Sprint(version), projectID, digest})[:32], Action: action, SubjectKind: kind, SubjectID: id, SubjectVer: version, ProjectID: projectID, ActorID: actor, ContentHash: digest, OccurredAt: service.now()})
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func validID(v string) bool            { return idPattern.MatchString(v) }
func namespaced(v, prefix string) bool { return validID(v) && strings.HasPrefix(v, prefix) }
func validLifecycle(v storage.RegistryLifecycle) bool {
	return v == storage.RegistryActive || v == storage.RegistryDeprecated || v == storage.RegistryDisabled
}
func validateTrade(v storage.TradeDefinition) error {
	if !namespaced(v.TradeID, "trade:") || v.Version < 1 || strings.TrimSpace(v.Name) == "" || !validLifecycle(v.Lifecycle) || v.Evidence.SampleCount < 0 {
		return fmt.Errorf("%w: trade", ErrInvalidDefinition)
	}
	return validateTags(v.CapabilityTags, v.RequiredCapabilities, v.OptionalCapabilities)
}
func validateWorker(v storage.WorkerProfile) error {
	if !namespaced(v.WorkerID, "worker:") || v.Version < 1 || !namespaced(v.TradeID, "trade:") || v.TradeVersion < 1 || !namespaced(v.InstructionID, "instruction:") || v.InstructionVer < 1 || !namespaced(v.RuntimeID, "runtime:") || v.RuntimeVersion < 1 || !namespaced(v.ToolPolicyID, "tool-policy:") || v.ToolPolicyVer < 1 || strings.TrimSpace(v.Name) == "" || strings.TrimSpace(v.Provider) == "" || strings.TrimSpace(v.Model) == "" || strings.TrimSpace(v.ModelVersion) == "" || !validLifecycle(v.Lifecycle) || v.Evidence.SampleCount < 0 {
		return fmt.Errorf("%w: worker", ErrInvalidDefinition)
	}
	return validateTags(v.CapabilityTags)
}
func validateAdaptation(v storage.ProjectTradeAdaptation) error {
	if !validID(v.ProjectID) || !namespaced(v.AdaptationID, "adaptation:") || v.Version < 1 || !namespaced(v.TradeID, "trade:") || v.TradeVersion < 1 || !validLifecycle(v.Lifecycle) {
		return fmt.Errorf("%w: project adaptation", ErrInvalidDefinition)
	}
	return validateTags(v.RequiredCapabilities, v.OptionalCapabilities)
}
func validateTags(lists ...[]string) error {
	for _, values := range lists {
		seen := map[string]bool{}
		for _, v := range values {
			if !validID(v) || seen[v] {
				return fmt.Errorf("%w: capability tags", ErrInvalidDefinition)
			}
			seen[v] = true
		}
	}
	return nil
}
func normalizeTrade(v *storage.TradeDefinition) {
	v.CapabilityTags = sorted(v.CapabilityTags)
	v.RequiredCapabilities = sorted(v.RequiredCapabilities)
	v.OptionalCapabilities = sorted(v.OptionalCapabilities)
}
func normalizeWorker(v *storage.WorkerProfile) { v.CapabilityTags = sorted(v.CapabilityTags) }
func normalizeAdaptation(v *storage.ProjectTradeAdaptation) {
	v.RequiredCapabilities = sorted(v.RequiredCapabilities)
	v.OptionalCapabilities = sorted(v.OptionalCapabilities)
}
func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
func hash(value any) string {
	switch item := value.(type) {
	case storage.TradeDefinition:
		item.ContentHash = ""
		value = item
	case storage.WorkerProfile:
		item.ContentHash = ""
		value = item
	case storage.ProjectTradeAdaptation:
		item.ContentHash = ""
		value = item
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func mapConflict(err error) error {
	if errors.Is(err, storage.ErrConflict) {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}
func tradeLess(a, b storage.TradeDefinition) bool {
	return a.TradeID < b.TradeID || a.TradeID == b.TradeID && a.Version < b.Version
}
func workerLess(a, b storage.WorkerProfile) bool {
	return a.WorkerID < b.WorkerID || a.WorkerID == b.WorkerID && a.Version < b.Version
}
func adaptationLess(a, b storage.ProjectTradeAdaptation) bool {
	return a.ProjectID < b.ProjectID || a.ProjectID == b.ProjectID && (a.AdaptationID < b.AdaptationID || a.AdaptationID == b.AdaptationID && a.Version < b.Version)
}
func listAllTrades(ctx context.Context, s storage.RegistryStore) ([]storage.TradeDefinition, error) {
	return listTrades(ctx, s, storage.PageRequest{Limit: storage.MaxAdminPageLimit})
}
func listAllWorkers(ctx context.Context, s storage.RegistryStore) ([]storage.WorkerProfile, error) {
	return listWorkers(ctx, s, storage.PageRequest{Limit: storage.MaxAdminPageLimit})
}
func listAllAdaptations(ctx context.Context, s storage.RegistryStore, p string) ([]storage.ProjectTradeAdaptation, error) {
	return listAdaptations(ctx, s, p, storage.PageRequest{Limit: storage.MaxAdminPageLimit})
}
func listTrades(ctx context.Context, s storage.RegistryStore, page storage.PageRequest) ([]storage.TradeDefinition, error) {
	out := []storage.TradeDefinition{}
	for {
		r, e := s.ListTradeDefinitions(ctx, storage.TradeQuery{Page: page})
		if e != nil {
			return nil, e
		}
		out = append(out, r.Items...)
		if r.NextCursor == nil {
			return out, nil
		}
		page.Cursor = *r.NextCursor
	}
}
func listWorkers(ctx context.Context, s storage.RegistryStore, page storage.PageRequest) ([]storage.WorkerProfile, error) {
	out := []storage.WorkerProfile{}
	for {
		r, e := s.ListWorkerProfiles(ctx, storage.WorkerQuery{Page: page})
		if e != nil {
			return nil, e
		}
		out = append(out, r.Items...)
		if r.NextCursor == nil {
			return out, nil
		}
		page.Cursor = *r.NextCursor
	}
}
func listAdaptations(ctx context.Context, s storage.RegistryStore, p string, page storage.PageRequest) ([]storage.ProjectTradeAdaptation, error) {
	out := []storage.ProjectTradeAdaptation{}
	for {
		r, e := s.ListProjectTradeAdaptations(ctx, storage.ProjectAdaptationQuery{ProjectID: p, Page: page})
		if e != nil {
			return nil, e
		}
		out = append(out, r.Items...)
		if r.NextCursor == nil {
			return out, nil
		}
		page.Cursor = *r.NextCursor
	}
}
