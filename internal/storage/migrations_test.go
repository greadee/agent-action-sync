package storage

import (
	"strings"
	"testing"
)

func TestMigrationsAreValid(t *testing.T) {
	if err := ValidateMigrations(Migrations); err != nil {
		t.Fatalf("ValidateMigrations: %v", err)
	}
}

func TestInitialMigrationContainsCoreTables(t *testing.T) {
	sql := Migrations[0].SQL
	requiredTables := []string{
		"devices",
		"shares",
		"share_permissions",
		"revisions",
		"file_index",
		"transfers",
		"transfer_chunks",
		"tombstones",
		"conflicts",
		"audit_events",
	}

	for _, table := range requiredTables {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("initial migration is missing table %q", table)
		}
	}
}

func TestValidateMigrationsRejectsVersionGaps(t *testing.T) {
	migrations := []Migration{{Version: 2, Name: "skip", SQL: "SELECT 1;"}}
	if err := ValidateMigrations(migrations); err == nil {
		t.Fatal("expected version gap to be rejected")
	}
}

func TestPairingMigrationContainsAcceptanceLedger(t *testing.T) {
	if !strings.Contains(Migrations[2].SQL, "CREATE TABLE IF NOT EXISTS pairing_acceptances") {
		t.Fatal("pairing migration is missing acceptance ledger")
	}
}

func TestAuthenticatedWorkMigrationBindsJobsToPeers(t *testing.T) {
	sql := Migrations[3].SQL
	for _, column := range []string{"peer_device_id", "required_capability"} {
		if !strings.Contains(sql, column) {
			t.Fatalf("authenticated work migration is missing %q", column)
		}
	}
}

func TestOneWayJobNetworkScopeMigrationPreservesRemoteDefault(t *testing.T) {
	sql := Migrations[4].SQL
	if !strings.Contains(sql, "remote INTEGER NOT NULL DEFAULT 1") {
		t.Fatal("one-way job network scope migration must preserve the prior remote authorization behavior")
	}
}

func TestAdministrationQueryMigrationAddsBoundedIndexes(t *testing.T) {
	sql := Migrations[5].SQL
	for _, index := range []string{"admin_permissions_device_idx", "admin_jobs_page_idx", "admin_audit_page_idx"} {
		if !strings.Contains(sql, index) {
			t.Fatalf("administration query migration is missing %q", index)
		}
	}
}

func TestAgentProjectProjectionMigrationIsAdditiveAndHasNoInsightSnapshots(t *testing.T) {
	sql := Migrations[6].SQL
	for _, table := range []string{
		"agent_projects", "project_events", "project_artifacts",
		"project_projection_checkpoints", "project_projection_rejections",
	} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("project projection migration is missing table %q", table)
		}
	}
	if strings.Contains(strings.ToLower(sql), "insight") {
		t.Fatal("stage 5 migration must not add insight snapshot storage")
	}
}

func TestTaskGraphReadinessMigrationAddsOnlyRebuildableProjectionTables(t *testing.T) {
	sql := Migrations[8].SQL
	for _, table := range []string{"project_tasks", "project_task_nodes"} {
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("task graph migration is missing table %q", table)
		}
	}
	for _, forbidden := range []string{"lease", "assignment", "runtime_session", "agent_event"} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Fatalf("task graph projection migration contains local execution state %q", forbidden)
		}
	}
}

func TestRegistryMigrationKeepsCredentialsAndRuntimeSessionsOutOfDurableDefinitions(t *testing.T) {
	sql := strings.ToLower(Migrations[9].SQL)
	for _, table := range []string{"registry_trade_versions", "registry_worker_versions", "registry_project_trade_adaptations", "registry_audit_events"} {
		if !strings.Contains(sql, "create table if not exists "+table) {
			t.Fatalf("registry migration is missing %q", table)
		}
	}
	for _, forbidden := range []string{"credential", "secret", "access_token", "runtime_session"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("registry migration contains forbidden durable field %q", forbidden)
		}
	}
}

func TestExecutionContractMigrationStoresVersionedAuthorityWithoutCredentialColumns(t *testing.T) {
	sql := strings.ToLower(Migrations[10].SQL)
	if !strings.Contains(sql, "create table if not exists execution_contract_versions") {
		t.Fatal("execution contract migration is missing the version table")
	}
	for _, required := range []string{"primary key (contract_id, version)", "predecessor_digest", "contract_json", "unique (project_id, execution_id, version)"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("execution contract migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"credential", "access_token", "runtime_session", "provider_session"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("execution contract migration contains forbidden durable column %q", forbidden)
		}
	}
}

func TestResultIntakeMigrationStoresOnlyBoundedUntrustedEnvelopeState(t *testing.T) {
	sql := strings.ToLower(Migrations[11].SQL)
	if !strings.Contains(sql, "create table if not exists orchestration_result_intake") {
		t.Fatal("result intake migration is missing the durable decision table")
	}
	for _, required := range []string{"result_id text primary key", "envelope_digest", "idempotency_key_digest", "unique (project_id, execution_id, idempotency_key_digest)", "assignment_digest", "decision", "reason_code", "envelope_json"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("result intake migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"credential", "access_token", "provider_session", "terminal_output", "raw_prompt", "secret_value"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("result intake migration contains forbidden field %q", forbidden)
		}
	}
}

func TestTelemetryMigrationStoresOnlyBoundedReferenceEvidence(t *testing.T) {
	sql := strings.ToLower(Migrations[12].SQL)
	if !strings.Contains(sql, "create table if not exists execution_telemetry") {
		t.Fatal("telemetry migration is missing the durable telemetry table")
	}
	for _, required := range []string{"telemetry_id text primary key", "telemetry_digest", "idempotency_key_digest", "summary_json", "unique (project_id, execution_id, idempotency_key_digest)"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("telemetry migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"credential", "access_token", "provider_session", "terminal_output", "raw_prompt", "secret_value", "absolute_path"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("telemetry migration contains forbidden field %q", forbidden)
		}
	}
}

func TestOrchestrationControlMigrationSeparatesFencedLocalStateFromCanonicalHistory(t *testing.T) {
	sql := strings.ToLower(Migrations[13].SQL)
	for _, table := range []string{
		"orchestration_assignments", "orchestration_attempts", "orchestration_leases", "orchestration_resource_bindings",
		"orchestration_gate_status", "orchestration_operator_decisions", "orchestration_operations", "orchestration_audit_events",
	} {
		if !strings.Contains(sql, "create table if not exists "+table) {
			t.Fatalf("orchestration control migration is missing %q", table)
		}
	}
	for _, required := range []string{
		"orchestration_active_attempt_idx", "orchestration_active_lease_idx", "where state in ('planned','leased','preparing','running','paused','collecting','awaiting_gates')",
		"fencing_digest", "lease_generation", "runtime_resume_key_digest", "recovery_disposition", "unique (project_id, work_package_id, idempotency_digest)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("orchestration control migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"raw_prompt", "terminal_output", "credential", "access_token", "provider_session", "absolute_path"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("orchestration control migration contains forbidden durable field %q", forbidden)
		}
	}
}

func TestOrchestrationAttemptBindingMigrationStoresOnlyBoundedAuthorityMetadata(t *testing.T) {
	sql := strings.ToLower(Migrations[14].SQL)
	if !strings.Contains(sql, "create table if not exists orchestration_attempt_bindings") {
		t.Fatal("attempt binding migration is missing the binding table")
	}
	for _, required := range []string{"attempt_id text primary key", "contract_id", "contract_version", "contract_digest", "context_digest", "context_compiler_version", "binding_digest", "binding_json"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("attempt binding migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"credential", "access_token", "provider_session", "terminal_output", "raw_prompt", "secret_value", "absolute_path"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("attempt binding migration contains forbidden field %q", forbidden)
		}
	}
}

func TestLocalProjectAuthorityMigrationIsExclusiveAndAuthorityLocal(t *testing.T) {
	sql := strings.ToLower(Migrations[15].SQL)
	for _, required := range []string{"local_project_policies", "local_project_selection", "singleton = 1", "max_concurrent between 1 and 2"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("local project authority migration is missing %q", required)
		}
	}
	for _, forbidden := range []string{"root_path", "workspace", "credential", "content", "prompt", "runtime_session"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("local project authority migration contains forbidden field %q", forbidden)
		}
	}
}
