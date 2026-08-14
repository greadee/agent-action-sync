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
