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

type projectTaskStore struct{ sql projectSQL }
type projectTaskNodeStore struct{ sql projectSQL }

func (store *Store) ProjectTasks() storage.ProjectTaskStore {
	return projectTaskStore{sql: store.db}
}

func (store *Store) ProjectTaskNodes() storage.ProjectTaskNodeStore {
	return projectTaskNodeStore{sql: store.db}
}

func (store projectTaskStore) SaveProjectTask(ctx context.Context, task storage.ProjectTaskProjection) (storage.ProjectTaskProjectionResult, error) {
	if err := validateProjectTask(ctx, task); err != nil {
		return storage.ProjectTaskProjectionResult{}, err
	}
	parallel, err := json.Marshal(task.ParallelReady)
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("encode task parallel readiness: %w", err)
	}
	result, err := store.sql.ExecContext(ctx, `
INSERT OR IGNORE INTO project_tasks(
    project_id, task_id, task_revision, graph_revision,
    task_record_id, task_record_hash, task_record_path,
    graph_record_id, graph_record_hash, graph_record_path,
    objective, priority, state, explanation_code, event_watermark,
    parallel_ready_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ProjectID, task.TaskID, task.TaskRevision, task.GraphRevision,
		task.TaskRecordID, task.TaskRecordHash, task.TaskRecordPath,
		task.GraphRecordID, task.GraphRecordHash, task.GraphRecordPath,
		task.Objective, task.Priority, task.State, task.ExplanationCode, task.EventWatermark,
		string(parallel), formatTime(task.CreatedAt),
	)
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("save project task: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("check project task insert: %w", err)
	}
	if changed == 1 {
		return storage.ProjectTaskProjectionResult{}, nil
	}
	var taskHash, graphHash string
	err = store.sql.QueryRowContext(ctx, `
SELECT task_record_hash, graph_record_hash FROM project_tasks
WHERE project_id = ? AND task_id = ? AND task_revision = ?`, task.ProjectID, task.TaskID, task.TaskRevision).Scan(&taskHash, &graphHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (taskHash != task.TaskRecordHash || graphHash != task.GraphRecordHash)) {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("%w: project task %s revision %d", storage.ErrConflict, task.TaskID, task.TaskRevision)
	}
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("read existing project task: %w", err)
	}
	_, err = store.sql.ExecContext(ctx, `
UPDATE project_tasks SET state = ?, explanation_code = ?, event_watermark = ?, parallel_ready_json = ?
WHERE project_id = ? AND task_id = ? AND task_revision = ? AND task_record_hash = ? AND graph_record_hash = ?`,
		task.State, task.ExplanationCode, task.EventWatermark, string(parallel), task.ProjectID, task.TaskID, task.TaskRevision,
		task.TaskRecordHash, task.GraphRecordHash)
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("update project task readiness: %w", err)
	}
	return storage.ProjectTaskProjectionResult{AlreadyPresent: true}, nil
}

func (store projectTaskStore) GetProjectTask(ctx context.Context, projectID, taskID string, taskRevision int64) (storage.ProjectTaskProjection, error) {
	if err := requireProjectID(ctx, projectID); err != nil {
		return storage.ProjectTaskProjection{}, err
	}
	if err := storage.ValidateProjectProjectionID(taskID); err != nil || taskRevision < 1 {
		return storage.ProjectTaskProjection{}, errors.New("task identity and positive revision are required")
	}
	item, err := scanProjectTask(store.sql.QueryRowContext(ctx, projectTaskSelect+`
WHERE project_id = ? AND task_id = ? AND task_revision = ?`, projectID, taskID, taskRevision))
	if err != nil {
		return storage.ProjectTaskProjection{}, mapNotFound(err, "project task", taskID)
	}
	return item, nil
}

func (store projectTaskStore) ListProjectTasks(ctx context.Context, query storage.ProjectTaskQuery) (storage.Page[storage.ProjectTaskProjection], error) {
	page, err := storage.NormalizePageRequest(query.Page)
	if err != nil {
		return storage.Page[storage.ProjectTaskProjection]{}, err
	}
	if err := requireProjectID(ctx, query.ProjectID); err != nil {
		return storage.Page[storage.ProjectTaskProjection]{}, err
	}
	if err := storage.ValidateProjectQueryFilter(query.TaskID); err != nil {
		return storage.Page[storage.ProjectTaskProjection]{}, err
	}
	statement := projectTaskSelect + ` WHERE project_id = ?`
	arguments := []any{query.ProjectID}
	if query.TaskID != "" {
		statement += ` AND task_id = ?`
		arguments = append(arguments, query.TaskID)
	}
	if page.Cursor.ID != "" {
		statement += ` AND (task_id > ? OR (task_id = ? AND task_revision > ?))`
		arguments = append(arguments, page.Cursor.ID, page.Cursor.ID, page.Cursor.Version)
	}
	statement += ` ORDER BY task_id, task_revision LIMIT ?`
	arguments = append(arguments, page.Limit+1)
	rows, err := store.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return storage.Page[storage.ProjectTaskProjection]{}, fmt.Errorf("list project tasks: %w", err)
	}
	defer rows.Close()
	items := make([]storage.ProjectTaskProjection, 0, page.Limit+1)
	for rows.Next() {
		item, scanErr := scanProjectTask(rows)
		if scanErr != nil {
			return storage.Page[storage.ProjectTaskProjection]{}, fmt.Errorf("scan project task: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.ProjectTaskProjection]{}, fmt.Errorf("iterate project tasks: %w", err)
	}
	return pageItems(items, page.Limit, func(item storage.ProjectTaskProjection) storage.PageCursor {
		return storage.PageCursor{ID: item.TaskID, Version: int(item.TaskRevision)}
	}), nil
}

const projectTaskSelect = `SELECT project_id, task_id, task_revision, graph_revision,
       task_record_id, task_record_hash, task_record_path,
       graph_record_id, graph_record_hash, graph_record_path,
       objective, priority, state, explanation_code, event_watermark,
       parallel_ready_json, created_at
FROM project_tasks`

func scanProjectTask(row rowScanner) (storage.ProjectTaskProjection, error) {
	var task storage.ProjectTaskProjection
	var parallel, createdAt string
	err := row.Scan(
		&task.ProjectID, &task.TaskID, &task.TaskRevision, &task.GraphRevision,
		&task.TaskRecordID, &task.TaskRecordHash, &task.TaskRecordPath,
		&task.GraphRecordID, &task.GraphRecordHash, &task.GraphRecordPath,
		&task.Objective, &task.Priority, &task.State, &task.ExplanationCode, &task.EventWatermark,
		&parallel, &createdAt,
	)
	if err != nil {
		return storage.ProjectTaskProjection{}, err
	}
	if err := json.Unmarshal([]byte(parallel), &task.ParallelReady); err != nil {
		return storage.ProjectTaskProjection{}, err
	}
	task.CreatedAt = parseStoredTime(createdAt)
	return task, nil
}

func (store projectTaskNodeStore) SaveProjectTaskNode(ctx context.Context, node storage.ProjectTaskNodeProjection) (storage.ProjectTaskProjectionResult, error) {
	if err := validateProjectTaskNode(ctx, node); err != nil {
		return storage.ProjectTaskProjectionResult{}, err
	}
	dependencies, err := json.Marshal(node.Dependencies)
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("encode task node dependencies: %w", err)
	}
	result, err := store.sql.ExecContext(ctx, `
INSERT OR IGNORE INTO project_task_nodes(
    project_id, task_id, task_revision, graph_revision, work_package_id,
    definition_record_id, definition_hash, definition_path, canonical_state,
    readiness, explanation_code, dependencies_json, barrier
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		node.ProjectID, node.TaskID, node.TaskRevision, node.GraphRevision, node.WorkPackageID,
		node.DefinitionRecordID, node.DefinitionHash, node.DefinitionPath, nullableString(node.CanonicalState),
		node.Readiness, node.ExplanationCode, string(dependencies), boolInt(node.Barrier),
	)
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("save project task node: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("check task node insert: %w", err)
	}
	if changed == 1 {
		return storage.ProjectTaskProjectionResult{}, nil
	}
	var definitionHash string
	err = store.sql.QueryRowContext(ctx, `
SELECT definition_hash FROM project_task_nodes
WHERE project_id = ? AND task_id = ? AND task_revision = ? AND work_package_id = ?`,
		node.ProjectID, node.TaskID, node.TaskRevision, node.WorkPackageID).Scan(&definitionHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && definitionHash != node.DefinitionHash) {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("%w: project task node %s", storage.ErrConflict, node.WorkPackageID)
	}
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("read existing project task node: %w", err)
	}
	_, err = store.sql.ExecContext(ctx, `
UPDATE project_task_nodes SET canonical_state = ?, readiness = ?, explanation_code = ?, dependencies_json = ?, barrier = ?
WHERE project_id = ? AND task_id = ? AND task_revision = ? AND work_package_id = ? AND definition_hash = ?`,
		nullableString(node.CanonicalState), node.Readiness, node.ExplanationCode, string(dependencies), boolInt(node.Barrier),
		node.ProjectID, node.TaskID, node.TaskRevision, node.WorkPackageID, node.DefinitionHash)
	if err != nil {
		return storage.ProjectTaskProjectionResult{}, fmt.Errorf("update project task node readiness: %w", err)
	}
	return storage.ProjectTaskProjectionResult{AlreadyPresent: true}, nil
}

func (store projectTaskNodeStore) ListProjectTaskNodes(ctx context.Context, query storage.ProjectTaskNodeQuery) (storage.Page[storage.ProjectTaskNodeProjection], error) {
	page, err := storage.NormalizePageRequest(query.Page)
	if err != nil {
		return storage.Page[storage.ProjectTaskNodeProjection]{}, err
	}
	if err := requireProjectID(ctx, query.ProjectID); err != nil {
		return storage.Page[storage.ProjectTaskNodeProjection]{}, err
	}
	if err := storage.ValidateProjectProjectionID(query.TaskID); err != nil || query.TaskRevision < 1 || query.GraphRevision < 1 {
		return storage.Page[storage.ProjectTaskNodeProjection]{}, errors.New("task and graph identity are required")
	}
	if query.Readiness != "" {
		if err := storage.ValidateTaskProjectionState(query.Readiness); err != nil {
			return storage.Page[storage.ProjectTaskNodeProjection]{}, err
		}
	}
	statement := projectTaskNodeSelect + ` WHERE project_id = ? AND task_id = ? AND task_revision = ? AND graph_revision = ?`
	arguments := []any{query.ProjectID, query.TaskID, query.TaskRevision, query.GraphRevision}
	if query.Readiness != "" {
		statement += ` AND readiness = ?`
		arguments = append(arguments, query.Readiness)
	}
	if page.Cursor.ID != "" {
		statement += ` AND work_package_id > ?`
		arguments = append(arguments, page.Cursor.ID)
	}
	statement += ` ORDER BY work_package_id LIMIT ?`
	arguments = append(arguments, page.Limit+1)
	rows, err := store.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return storage.Page[storage.ProjectTaskNodeProjection]{}, fmt.Errorf("list project task nodes: %w", err)
	}
	defer rows.Close()
	items := make([]storage.ProjectTaskNodeProjection, 0, page.Limit+1)
	for rows.Next() {
		item, scanErr := scanProjectTaskNode(rows)
		if scanErr != nil {
			return storage.Page[storage.ProjectTaskNodeProjection]{}, fmt.Errorf("scan project task node: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.ProjectTaskNodeProjection]{}, fmt.Errorf("iterate project task nodes: %w", err)
	}
	return pageItems(items, page.Limit, func(item storage.ProjectTaskNodeProjection) storage.PageCursor {
		return storage.PageCursor{ID: item.WorkPackageID}
	}), nil
}

const projectTaskNodeSelect = `SELECT project_id, task_id, task_revision, graph_revision, work_package_id,
       definition_record_id, definition_hash, definition_path, canonical_state,
       readiness, explanation_code, dependencies_json, barrier
FROM project_task_nodes`

func scanProjectTaskNode(row rowScanner) (storage.ProjectTaskNodeProjection, error) {
	var node storage.ProjectTaskNodeProjection
	var canonical sql.NullString
	var dependencies string
	var barrier int
	err := row.Scan(
		&node.ProjectID, &node.TaskID, &node.TaskRevision, &node.GraphRevision, &node.WorkPackageID,
		&node.DefinitionRecordID, &node.DefinitionHash, &node.DefinitionPath, &canonical,
		&node.Readiness, &node.ExplanationCode, &dependencies, &barrier,
	)
	if err != nil {
		return storage.ProjectTaskNodeProjection{}, err
	}
	if err := json.Unmarshal([]byte(dependencies), &node.Dependencies); err != nil {
		return storage.ProjectTaskNodeProjection{}, err
	}
	node.CanonicalState = canonical.String
	node.Barrier = barrier == 1
	return node, nil
}

func validateProjectTask(ctx context.Context, task storage.ProjectTaskProjection) error {
	if err := requireProjectID(ctx, task.ProjectID); err != nil {
		return err
	}
	for _, value := range []string{task.TaskID, task.TaskRecordID, task.GraphRecordID, task.ExplanationCode} {
		if err := storage.ValidateProjectProjectionID(value); err != nil {
			return err
		}
	}
	if task.TaskRevision < 1 || task.GraphRevision < 1 || task.CreatedAt.IsZero() || strings.TrimSpace(task.Objective) == "" {
		return errors.New("task projection is incomplete")
	}
	if task.Priority != "low" && task.Priority != "normal" && task.Priority != "high" && task.Priority != "critical" {
		return errors.New("task priority is unsupported")
	}
	if err := storage.ValidateTaskProjectionState(task.State); err != nil {
		return err
	}
	for _, hash := range []string{task.TaskRecordHash, task.GraphRecordHash, task.EventWatermark} {
		if !validProjectionHash(hash) {
			return errors.New("task projection hashes are invalid")
		}
	}
	for _, path := range []string{task.TaskRecordPath, task.GraphRecordPath} {
		if err := storage.ValidateProjectProjectionPath(path, true); err != nil {
			return err
		}
	}
	for _, id := range task.ParallelReady {
		if err := storage.ValidateProjectProjectionID(id); err != nil {
			return err
		}
	}
	return nil
}

func validateProjectTaskNode(ctx context.Context, node storage.ProjectTaskNodeProjection) error {
	if err := requireProjectID(ctx, node.ProjectID); err != nil {
		return err
	}
	for _, value := range []string{node.TaskID, node.WorkPackageID, node.DefinitionRecordID, node.ExplanationCode} {
		if err := storage.ValidateProjectProjectionID(value); err != nil {
			return err
		}
	}
	if node.TaskRevision < 1 || node.GraphRevision < 1 || !validProjectionHash(node.DefinitionHash) {
		return errors.New("task node identity is incomplete")
	}
	if err := storage.ValidateProjectProjectionPath(node.DefinitionPath, true); err != nil {
		return err
	}
	if node.CanonicalState != "" {
		switch node.CanonicalState {
		case "planned", "ready", "in_progress", "blocked", "review", "accepted", "failed", "canceled":
		default:
			return errors.New("task node canonical state is unsupported")
		}
	}
	if err := storage.ValidateTaskProjectionState(node.Readiness); err != nil {
		return err
	}
	for _, dependency := range node.Dependencies {
		if err := storage.ValidateProjectProjectionID(dependency); err != nil {
			return err
		}
	}
	return nil
}
