package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"syncgate/internal/storage"
)

func (store registryStore) SaveTradeDefinition(ctx context.Context, value storage.TradeDefinition) (storage.RegistryWriteResult, error) {
	if err := validateTrade(value); err != nil {
		return storage.RegistryWriteResult{}, err
	}
	raw, err := registryJSON(value.CapabilityTags, value.RequiredCapabilities, value.OptionalCapabilities, value.Evidence)
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	tags, required, optional, evidence := raw[0], raw[1], raw[2], raw[3]
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO registry_trade_versions(trade_id,version,name,lifecycle,capability_tags_json,required_capabilities_json,optional_capabilities_json,description,evidence_json,content_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, value.TradeID, value.Version, value.Name, value.Lifecycle, tags, required, optional, value.Description, evidence, value.ContentHash, formatTime(value.CreatedAt))
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("save trade definition: %w", err)
	}
	return registryWriteResult(ctx, store.db, result, `SELECT content_hash FROM registry_trade_versions WHERE trade_id=? AND version=?`, value.ContentHash, value.TradeID, value.Version)
}

func (store registryStore) GetTradeDefinition(ctx context.Context, id string, version int64) (storage.TradeDefinition, error) {
	if err := validateRegistryKey(ctx, id, version); err != nil {
		return storage.TradeDefinition{}, err
	}
	value, err := scanTrade(store.db.QueryRowContext(ctx, tradeSelect+` WHERE trade_id=? AND version=?`, id, version))
	if err != nil {
		return storage.TradeDefinition{}, mapNotFound(err, "trade definition", id)
	}
	return value, nil
}
func (store registryStore) ListTradeDefinitions(ctx context.Context, query storage.TradeQuery) (storage.Page[storage.TradeDefinition], error) {
	page, err := storage.NormalizePageRequest(query.Page)
	if err != nil {
		return storage.Page[storage.TradeDefinition]{}, err
	}
	if err := validateRegistryFilter(ctx, query.TradeID, string(query.Lifecycle)); err != nil {
		return storage.Page[storage.TradeDefinition]{}, err
	}
	statement := tradeSelect + ` WHERE 1=1`
	args := []any{}
	if query.TradeID != "" {
		statement += ` AND trade_id=?`
		args = append(args, query.TradeID)
	}
	if query.Lifecycle != "" {
		statement += ` AND lifecycle=?`
		args = append(args, query.Lifecycle)
	}
	if page.Cursor.ID != "" {
		statement += ` AND (trade_id>? OR (trade_id=? AND version>?))`
		args = append(args, page.Cursor.ID, page.Cursor.ID, page.Cursor.Version)
	}
	statement += ` ORDER BY trade_id,version LIMIT ?`
	args = append(args, page.Limit+1)
	rows, err := store.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return storage.Page[storage.TradeDefinition]{}, fmt.Errorf("list trade definitions: %w", err)
	}
	defer rows.Close()
	items := []storage.TradeDefinition{}
	for rows.Next() {
		item, e := scanTrade(rows)
		if e != nil {
			return storage.Page[storage.TradeDefinition]{}, e
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.TradeDefinition]{}, err
	}
	return pageItems(items, page.Limit, func(v storage.TradeDefinition) storage.PageCursor {
		return storage.PageCursor{ID: v.TradeID, Version: int(v.Version)}
	}), nil
}

func (store registryStore) SaveWorkerProfile(ctx context.Context, value storage.WorkerProfile) (storage.RegistryWriteResult, error) {
	if err := validateWorker(value); err != nil {
		return storage.RegistryWriteResult{}, err
	}
	raw, err := registryJSON(value.CapabilityTags, value.Evidence)
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	tags, evidence := raw[0], raw[1]
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO registry_worker_versions(worker_id,version,name,lifecycle,trade_id,trade_version,instruction_id,instruction_version,runtime_id,runtime_version,provider,model,model_version,tool_policy_id,tool_policy_version,capability_tags_json,evidence_json,content_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.WorkerID, value.Version, value.Name, value.Lifecycle, value.TradeID, value.TradeVersion, value.InstructionID, value.InstructionVer, value.RuntimeID, value.RuntimeVersion, value.Provider, value.Model, value.ModelVersion, value.ToolPolicyID, value.ToolPolicyVer, tags, evidence, value.ContentHash, formatTime(value.CreatedAt))
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("save worker profile: %w", err)
	}
	return registryWriteResult(ctx, store.db, result, `SELECT content_hash FROM registry_worker_versions WHERE worker_id=? AND version=?`, value.ContentHash, value.WorkerID, value.Version)
}
func (store registryStore) GetWorkerProfile(ctx context.Context, id string, version int64) (storage.WorkerProfile, error) {
	if err := validateRegistryKey(ctx, id, version); err != nil {
		return storage.WorkerProfile{}, err
	}
	v, e := scanWorker(store.db.QueryRowContext(ctx, workerSelect+` WHERE worker_id=? AND version=?`, id, version))
	if e != nil {
		return storage.WorkerProfile{}, mapNotFound(e, "worker profile", id)
	}
	return v, nil
}
func (store registryStore) ListWorkerProfiles(ctx context.Context, q storage.WorkerQuery) (storage.Page[storage.WorkerProfile], error) {
	page, e := storage.NormalizePageRequest(q.Page)
	if e != nil {
		return storage.Page[storage.WorkerProfile]{}, e
	}
	if e = validateRegistryFilter(ctx, q.WorkerID, q.TradeID, string(q.Lifecycle)); e != nil {
		return storage.Page[storage.WorkerProfile]{}, e
	}
	s := workerSelect + ` WHERE 1=1`
	a := []any{}
	if q.WorkerID != "" {
		s += ` AND worker_id=?`
		a = append(a, q.WorkerID)
	}
	if q.TradeID != "" {
		s += ` AND trade_id=?`
		a = append(a, q.TradeID)
	}
	if q.Lifecycle != "" {
		s += ` AND lifecycle=?`
		a = append(a, q.Lifecycle)
	}
	if page.Cursor.ID != "" {
		s += ` AND (worker_id>? OR (worker_id=? AND version>?))`
		a = append(a, page.Cursor.ID, page.Cursor.ID, page.Cursor.Version)
	}
	s += ` ORDER BY worker_id,version LIMIT ?`
	a = append(a, page.Limit+1)
	rows, e := store.db.QueryContext(ctx, s, a...)
	if e != nil {
		return storage.Page[storage.WorkerProfile]{}, e
	}
	defer rows.Close()
	items := []storage.WorkerProfile{}
	for rows.Next() {
		v, x := scanWorker(rows)
		if x != nil {
			return storage.Page[storage.WorkerProfile]{}, x
		}
		items = append(items, v)
	}
	if e = rows.Err(); e != nil {
		return storage.Page[storage.WorkerProfile]{}, e
	}
	return pageItems(items, page.Limit, func(v storage.WorkerProfile) storage.PageCursor {
		return storage.PageCursor{ID: v.WorkerID, Version: int(v.Version)}
	}), nil
}

func (store registryStore) SaveProjectTradeAdaptation(ctx context.Context, v storage.ProjectTradeAdaptation) (storage.RegistryWriteResult, error) {
	if err := validateAdaptation(ctx, v); err != nil {
		return storage.RegistryWriteResult{}, err
	}
	raw, err := registryJSON(v.RequiredCapabilities, v.OptionalCapabilities)
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	required, optional := raw[0], raw[1]
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO registry_project_trade_adaptations(project_id,adaptation_id,version,trade_id,trade_version,lifecycle,required_capabilities_json,optional_capabilities_json,notes,content_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, v.ProjectID, v.AdaptationID, v.Version, v.TradeID, v.TradeVersion, v.Lifecycle, required, optional, v.Notes, v.ContentHash, formatTime(v.CreatedAt))
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("save project trade adaptation: %w", err)
	}
	return registryWriteResult(ctx, store.db, result, `SELECT content_hash FROM registry_project_trade_adaptations WHERE project_id=? AND adaptation_id=? AND version=?`, v.ContentHash, v.ProjectID, v.AdaptationID, v.Version)
}
func (store registryStore) GetProjectTradeAdaptation(ctx context.Context, projectID, id string, version int64) (storage.ProjectTradeAdaptation, error) {
	if err := validateRegistryKey(ctx, projectID, 1); err != nil {
		return storage.ProjectTradeAdaptation{}, err
	}
	if err := validateRegistryKey(ctx, id, version); err != nil {
		return storage.ProjectTradeAdaptation{}, err
	}
	v, e := scanAdaptation(store.db.QueryRowContext(ctx, adaptationSelect+` WHERE project_id=? AND adaptation_id=? AND version=?`, projectID, id, version))
	if e != nil {
		return storage.ProjectTradeAdaptation{}, mapNotFound(e, "project trade adaptation", id)
	}
	return v, nil
}
func (store registryStore) ListProjectTradeAdaptations(ctx context.Context, q storage.ProjectAdaptationQuery) (storage.Page[storage.ProjectTradeAdaptation], error) {
	page, e := storage.NormalizePageRequest(q.Page)
	if e != nil {
		return storage.Page[storage.ProjectTradeAdaptation]{}, e
	}
	if e = validateRegistryFilter(ctx, q.ProjectID, q.TradeID); e != nil {
		return storage.Page[storage.ProjectTradeAdaptation]{}, e
	}
	if q.ProjectID == "" {
		return storage.Page[storage.ProjectTradeAdaptation]{}, errors.New("project id is required")
	}
	s := adaptationSelect + ` WHERE project_id=?`
	a := []any{q.ProjectID}
	if q.TradeID != "" {
		s += ` AND trade_id=?`
		a = append(a, q.TradeID)
	}
	if page.Cursor.ID != "" {
		s += ` AND (adaptation_id>? OR (adaptation_id=? AND version>?))`
		a = append(a, page.Cursor.ID, page.Cursor.ID, page.Cursor.Version)
	}
	s += ` ORDER BY adaptation_id,version LIMIT ?`
	a = append(a, page.Limit+1)
	rows, e := store.db.QueryContext(ctx, s, a...)
	if e != nil {
		return storage.Page[storage.ProjectTradeAdaptation]{}, e
	}
	defer rows.Close()
	items := []storage.ProjectTradeAdaptation{}
	for rows.Next() {
		v, x := scanAdaptation(rows)
		if x != nil {
			return storage.Page[storage.ProjectTradeAdaptation]{}, x
		}
		items = append(items, v)
	}
	if e = rows.Err(); e != nil {
		return storage.Page[storage.ProjectTradeAdaptation]{}, e
	}
	return pageItems(items, page.Limit, func(v storage.ProjectTradeAdaptation) storage.PageCursor {
		return storage.PageCursor{ID: v.AdaptationID, Version: int(v.Version)}
	}), nil
}

func (store registryStore) RecordRegistryAudit(ctx context.Context, v storage.RegistryAuditEvent) error {
	if err := validateAudit(v); err != nil {
		return err
	}
	r, e := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO registry_audit_events(audit_id,action,subject_kind,subject_id,subject_version,project_id,actor_id,content_hash,occurred_at) VALUES(?,?,?,?,?,?,?,?,?)`, v.AuditID, v.Action, v.SubjectKind, v.SubjectID, v.SubjectVer, nullableString(v.ProjectID), v.ActorID, v.ContentHash, formatTime(v.OccurredAt))
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}
func (store registryStore) ListRegistryAudit(ctx context.Context, q storage.RegistryAuditQuery) (storage.Page[storage.RegistryAuditEvent], error) {
	page, e := storage.NormalizePageRequest(q.Page)
	if e != nil {
		return storage.Page[storage.RegistryAuditEvent]{}, e
	}
	if e = validateRegistryFilter(ctx, q.ProjectID, q.SubjectID); e != nil {
		return storage.Page[storage.RegistryAuditEvent]{}, e
	}
	s := `SELECT audit_id,action,subject_kind,subject_id,subject_version,project_id,actor_id,content_hash,occurred_at FROM registry_audit_events WHERE 1=1`
	a := []any{}
	if q.ProjectID != "" {
		s += ` AND project_id=?`
		a = append(a, q.ProjectID)
	}
	if q.SubjectID != "" {
		s += ` AND subject_id=?`
		a = append(a, q.SubjectID)
	}
	if page.Cursor.ID != "" {
		s += ` AND audit_id>?`
		a = append(a, page.Cursor.ID)
	}
	s += ` ORDER BY audit_id LIMIT ?`
	a = append(a, page.Limit+1)
	rows, e := store.db.QueryContext(ctx, s, a...)
	if e != nil {
		return storage.Page[storage.RegistryAuditEvent]{}, e
	}
	defer rows.Close()
	items := []storage.RegistryAuditEvent{}
	for rows.Next() {
		var v storage.RegistryAuditEvent
		var project sql.NullString
		var occurred string
		e = rows.Scan(&v.AuditID, &v.Action, &v.SubjectKind, &v.SubjectID, &v.SubjectVer, &project, &v.ActorID, &v.ContentHash, &occurred)
		if e != nil {
			return storage.Page[storage.RegistryAuditEvent]{}, e
		}
		v.ProjectID = project.String
		v.OccurredAt = parseStoredTime(occurred)
		items = append(items, v)
	}
	if e = rows.Err(); e != nil {
		return storage.Page[storage.RegistryAuditEvent]{}, e
	}
	return pageItems(items, page.Limit, func(v storage.RegistryAuditEvent) storage.PageCursor { return storage.PageCursor{ID: v.AuditID} }), nil
}

const tradeSelect = `SELECT trade_id,version,name,lifecycle,capability_tags_json,required_capabilities_json,optional_capabilities_json,description,evidence_json,content_hash,created_at FROM registry_trade_versions`
const workerSelect = `SELECT worker_id,version,name,lifecycle,trade_id,trade_version,instruction_id,instruction_version,runtime_id,runtime_version,provider,model,model_version,tool_policy_id,tool_policy_version,capability_tags_json,evidence_json,content_hash,created_at FROM registry_worker_versions`
const adaptationSelect = `SELECT project_id,adaptation_id,version,trade_id,trade_version,lifecycle,required_capabilities_json,optional_capabilities_json,notes,content_hash,created_at FROM registry_project_trade_adaptations`

type registryScanner interface{ Scan(...any) error }

func scanTrade(r registryScanner) (storage.TradeDefinition, error) {
	var v storage.TradeDefinition
	var tags, req, opt, evidence, created string
	e := r.Scan(&v.TradeID, &v.Version, &v.Name, &v.Lifecycle, &tags, &req, &opt, &v.Description, &evidence, &v.ContentHash, &created)
	if e != nil {
		return v, e
	}
	if e = registryDecode(&v.CapabilityTags, tags, &v.RequiredCapabilities, req, &v.OptionalCapabilities, opt, &v.Evidence, evidence); e != nil {
		return v, e
	}
	v.CreatedAt = parseStoredTime(created)
	return v, nil
}
func scanWorker(r registryScanner) (storage.WorkerProfile, error) {
	var v storage.WorkerProfile
	var tags, evidence, created string
	e := r.Scan(&v.WorkerID, &v.Version, &v.Name, &v.Lifecycle, &v.TradeID, &v.TradeVersion, &v.InstructionID, &v.InstructionVer, &v.RuntimeID, &v.RuntimeVersion, &v.Provider, &v.Model, &v.ModelVersion, &v.ToolPolicyID, &v.ToolPolicyVer, &tags, &evidence, &v.ContentHash, &created)
	if e != nil {
		return v, e
	}
	if e = registryDecode(&v.CapabilityTags, tags, &v.Evidence, evidence); e != nil {
		return v, e
	}
	v.CreatedAt = parseStoredTime(created)
	return v, nil
}
func scanAdaptation(r registryScanner) (storage.ProjectTradeAdaptation, error) {
	var v storage.ProjectTradeAdaptation
	var req, opt, created string
	e := r.Scan(&v.ProjectID, &v.AdaptationID, &v.Version, &v.TradeID, &v.TradeVersion, &v.Lifecycle, &req, &opt, &v.Notes, &v.ContentHash, &created)
	if e != nil {
		return v, e
	}
	if e = registryDecode(&v.RequiredCapabilities, req, &v.OptionalCapabilities, opt); e != nil {
		return v, e
	}
	v.CreatedAt = parseStoredTime(created)
	return v, nil
}
func registryJSON(values ...any) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, v := range values {
		raw, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		result = append(result, string(raw))
	}
	return result, nil
}
func registryDecode(values ...any) error {
	for i := 0; i < len(values); i += 2 {
		if e := json.Unmarshal([]byte(values[i+1].(string)), values[i]); e != nil {
			return e
		}
	}
	return nil
}
func registryWriteResult(ctx context.Context, db *sql.DB, result sql.Result, query, hash string, args ...any) (storage.RegistryWriteResult, error) {
	n, e := result.RowsAffected()
	if e != nil {
		return storage.RegistryWriteResult{}, e
	}
	if n == 1 {
		return storage.RegistryWriteResult{}, nil
	}
	var existing string
	e = db.QueryRowContext(ctx, query, args...).Scan(&existing)
	if e != nil {
		return storage.RegistryWriteResult{}, e
	}
	if existing != hash {
		return storage.RegistryWriteResult{}, fmt.Errorf("%w: immutable registry version", storage.ErrConflict)
	}
	return storage.RegistryWriteResult{AlreadyPresent: true}, nil
}
func validateRegistryKey(ctx context.Context, id string, version int64) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := storage.ValidateProjectProjectionID(id); e != nil {
		return e
	}
	if version < 1 {
		return errors.New("positive version is required")
	}
	return nil
}
func validateRegistryFilter(ctx context.Context, values ...string) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	for _, v := range values {
		if e := storage.ValidateAdminFilter(v); e != nil {
			return e
		}
	}
	return nil
}
func validLifecycle(v storage.RegistryLifecycle) bool {
	return v == storage.RegistryActive || v == storage.RegistryDeprecated || v == storage.RegistryDisabled
}
func validHash(v string) bool { return len(v) == 64 && strings.Trim(v, "0123456789abcdef") == "" }
func validateTrade(v storage.TradeDefinition) error {
	if e := validateRegistryKey(context.Background(), v.TradeID, v.Version); e != nil {
		return e
	}
	if v.Name == "" || !validLifecycle(v.Lifecycle) || !validHash(v.ContentHash) || v.CreatedAt.IsZero() {
		return errors.New("trade definition is incomplete")
	}
	return nil
}
func validateWorker(v storage.WorkerProfile) error {
	if e := validateRegistryKey(context.Background(), v.WorkerID, v.Version); e != nil {
		return e
	}
	for _, p := range []struct {
		id      string
		version int64
	}{{v.TradeID, v.TradeVersion}, {v.InstructionID, v.InstructionVer}, {v.RuntimeID, v.RuntimeVersion}, {v.ToolPolicyID, v.ToolPolicyVer}} {
		if e := validateRegistryKey(context.Background(), p.id, p.version); e != nil {
			return e
		}
	}
	if v.Name == "" || v.Provider == "" || v.Model == "" || v.ModelVersion == "" || !validLifecycle(v.Lifecycle) || !validHash(v.ContentHash) || v.CreatedAt.IsZero() {
		return errors.New("worker profile is incomplete")
	}
	return nil
}
func validateAdaptation(ctx context.Context, v storage.ProjectTradeAdaptation) error {
	if e := validateRegistryKey(ctx, v.ProjectID, 1); e != nil {
		return e
	}
	if e := validateRegistryKey(ctx, v.AdaptationID, v.Version); e != nil {
		return e
	}
	if e := validateRegistryKey(ctx, v.TradeID, v.TradeVersion); e != nil {
		return e
	}
	if !validLifecycle(v.Lifecycle) || !validHash(v.ContentHash) || v.CreatedAt.IsZero() {
		return errors.New("project adaptation is incomplete")
	}
	return nil
}
func validateAudit(v storage.RegistryAuditEvent) error {
	if e := validateRegistryKey(context.Background(), v.AuditID, 1); e != nil {
		return e
	}
	if e := validateRegistryKey(context.Background(), v.SubjectID, v.SubjectVer); e != nil {
		return e
	}
	if v.Action == "" || v.SubjectKind == "" || v.ActorID == "" || !validHash(v.ContentHash) || v.OccurredAt.IsZero() {
		return errors.New("registry audit event is incomplete")
	}
	return nil
}
