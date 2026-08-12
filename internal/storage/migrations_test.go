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
