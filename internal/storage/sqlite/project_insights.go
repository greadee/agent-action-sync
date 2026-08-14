package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"syncgate/internal/storage"
)

func (store projectInsightStore) GetProjectInsight(ctx context.Context, projectID, scope, metricName string, definitionVersion int) (storage.ProjectInsightProjection, error) {
	if err := validateProjectInsightIdentity(ctx, projectID, scope, metricName, definitionVersion); err != nil {
		return storage.ProjectInsightProjection{}, err
	}
	row := store.sql.QueryRowContext(ctx, projectInsightSelect+` WHERE project_id = ? AND scope = ? AND metric_name = ? AND definition_version = ?`, projectID, scope, metricName, definitionVersion)
	insight, err := scanProjectInsight(row)
	if err != nil {
		return storage.ProjectInsightProjection{}, mapNotFound(err, "project insight", metricName)
	}
	return insight, nil
}

func (store projectInsightStore) ListProjectInsights(ctx context.Context, query storage.ProjectInsightQuery) ([]storage.ProjectInsightProjection, error) {
	if err := requireProjectID(ctx, query.ProjectID); err != nil {
		return nil, err
	}
	if err := storage.ValidateProjectQueryFilter(query.Scope); err != nil {
		return nil, err
	}
	if err := storage.ValidateProjectQueryFilter(query.MetricName); err != nil {
		return nil, err
	}
	statement := projectInsightSelect + ` WHERE project_id = ?`
	arguments := []any{query.ProjectID}
	if query.Scope != "" {
		statement += ` AND scope = ?`
		arguments = append(arguments, query.Scope)
	}
	if query.MetricName != "" {
		statement += ` AND metric_name = ?`
		arguments = append(arguments, query.MetricName)
	}
	statement += ` ORDER BY scope, metric_name, definition_version`
	rows, err := store.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list project insights: %w", err)
	}
	defer rows.Close()
	insights := make([]storage.ProjectInsightProjection, 0)
	for rows.Next() {
		insight, scanErr := scanProjectInsight(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan project insight: %w", scanErr)
		}
		insights = append(insights, insight)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list project insights: %w", err)
	}
	return insights, nil
}

func (store projectInsightProjectionStore) ReplaceProjectInsights(ctx context.Context, projectID string, replace func(storage.ProjectInsightWriter) error) error {
	if err := requireProjectID(ctx, projectID); err != nil {
		return err
	}
	if replace == nil {
		return errors.New("project insight replacement is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project insight replacement: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_insights WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("clear project insights: %w", err)
	}
	if err := replace(projectInsightWriter{sql: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project insight replacement: %w", err)
	}
	return nil
}

type projectInsightWriter struct{ sql projectSQL }

func (writer projectInsightWriter) SaveProjectInsight(ctx context.Context, insight storage.ProjectInsightProjection) error {
	if err := validateProjectInsight(ctx, insight); err != nil {
		return err
	}
	_, err := writer.sql.ExecContext(ctx, `
INSERT INTO project_insights(
    project_id, scope, metric_name, definition_version, source_event_watermark,
    window_start, window_end, value_json, sample_count, completeness, evidence, calculated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		insight.ProjectID, insight.Scope, insight.MetricName, insight.DefinitionVersion, insight.SourceEventWatermark,
		nullableInsightTime(insight.WindowStart), nullableInsightTime(insight.WindowEnd), string(insight.ValueJSON), insight.SampleCount,
		insight.Completeness, insight.Evidence, formatTime(insight.CalculatedAt),
	)
	if err != nil {
		return fmt.Errorf("save project insight: %w", err)
	}
	return nil
}

const projectInsightSelect = `SELECT project_id, scope, metric_name, definition_version, source_event_watermark,
       window_start, window_end, value_json, sample_count, completeness, evidence, calculated_at
FROM project_insights`

func scanProjectInsight(row rowScanner) (storage.ProjectInsightProjection, error) {
	var insight storage.ProjectInsightProjection
	var windowStart, windowEnd sql.NullString
	var value, calculatedAt string
	err := row.Scan(&insight.ProjectID, &insight.Scope, &insight.MetricName, &insight.DefinitionVersion, &insight.SourceEventWatermark,
		&windowStart, &windowEnd, &value, &insight.SampleCount, &insight.Completeness, &insight.Evidence, &calculatedAt)
	if err != nil {
		return storage.ProjectInsightProjection{}, err
	}
	if windowStart.Valid {
		value := parseStoredTime(windowStart.String)
		insight.WindowStart = &value
	}
	if windowEnd.Valid {
		value := parseStoredTime(windowEnd.String)
		insight.WindowEnd = &value
	}
	insight.ValueJSON = []byte(value)
	insight.CalculatedAt = parseStoredTime(calculatedAt)
	return insight, nil
}

func validateProjectInsightIdentity(ctx context.Context, projectID, scope, metricName string, version int) error {
	if err := requireProjectID(ctx, projectID); err != nil {
		return err
	}
	if err := storage.ValidateProjectProjectionID(scope); err != nil {
		return err
	}
	if err := storage.ValidateProjectProjectionID(metricName); err != nil {
		return err
	}
	if version < 1 {
		return errors.New("project insight definition version is required")
	}
	return nil
}

func validateProjectInsight(ctx context.Context, insight storage.ProjectInsightProjection) error {
	if err := validateProjectInsightIdentity(ctx, insight.ProjectID, insight.Scope, insight.MetricName, insight.DefinitionVersion); err != nil {
		return err
	}
	if !validProjectionHash(insight.SourceEventWatermark) || insight.SampleCount < 0 || insight.CalculatedAt.IsZero() {
		return errors.New("project insight watermark, sample count, and calculation time are required")
	}
	if len(insight.ValueJSON) == 0 || len(insight.ValueJSON) > storage.MaxProjectInsightValueBytes || !json.Valid(insight.ValueJSON) {
		return errors.New("project insight value must be bounded JSON")
	}
	if insight.Completeness != "complete" && insight.Completeness != "partial" && insight.Completeness != "insufficient" {
		return errors.New("project insight completeness is invalid")
	}
	if insight.Evidence != "strong" && insight.Evidence != "weak" && insight.Evidence != "none" {
		return errors.New("project insight evidence is invalid")
	}
	return nil
}

func nullableInsightTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

var _ storage.ProjectInsightStore = projectInsightStore{}
var _ storage.ProjectInsightProjectionStore = projectInsightProjectionStore{}
