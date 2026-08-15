package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAmbiguityMigrationSeparatesMetadataLockFromValidationScan(t *testing.T) {
	root := filepath.Join("..", "..", "..", "migrations")
	shape, err := os.ReadFile(filepath.Join(root, "388_channel_delivery_ambiguity.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	validation, err := os.ReadFile(filepath.Join(root, "389_channel_delivery_ambiguity_validate.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	shapeSQL := string(shape)
	if strings.Count(shapeSQL, "NOT VALID") != 2 || strings.Contains(shapeSQL, "VALIDATE CONSTRAINT") {
		t.Fatalf("migration 388 must add both checks NOT VALID without scanning existing rows: %s", shapeSQL)
	}
	validationSQL := string(validation)
	for _, constraint := range []string{"channel_delivery_status_check", "channel_delivery_ambiguous_evidence_check"} {
		if !strings.Contains(validationSQL, "VALIDATE CONSTRAINT "+constraint) {
			t.Fatalf("migration 389 does not validate %s", constraint)
		}
	}
}

func TestAmbiguityRollbackRefusesEvidenceLoss(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "388_channel_delivery_ambiguity.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rollback := string(body)
	if !strings.Contains(rollback, "status = 'ambiguous'") || !strings.Contains(rollback, "RAISE EXCEPTION") {
		t.Fatalf("ambiguity rollback must fail closed while evidence exists: %s", rollback)
	}
}

func TestReconciliationMigrationsPreserveEvidenceAndIndexAvailability(t *testing.T) {
	root := filepath.Join("..", "..", "..", "migrations")
	shape, err := os.ReadFile(filepath.Join(root, "390_channel_delivery_reconciliation.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	shapeSQL := string(shape)
	for _, required := range []string{
		"channel_delivery_reconciliation", "retry_authorized", "reconciled", "ambiguity_evidence",
		"requester_key_id <> approver_key_id", "confirmed_not_delivered", "partial_delivery", "NOT VALID",
	} {
		if !strings.Contains(shapeSQL, required) {
			t.Fatalf("migration 390 is missing %q", required)
		}
	}
	if strings.Contains(shapeSQL, "VALIDATE CONSTRAINT") {
		t.Fatal("migration 390 performs an existing-row validation scan in its metadata-lock window")
	}
	validation, err := os.ReadFile(filepath.Join(root, "396_channel_delivery_reconciliation_validate.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, constraint := range []string{
		"channel_delivery_status_check", "channel_delivery_reconciliation_count_check",
		"channel_delivery_reconciliation_state_check", "channel_delivery_retry_publish_lease_check",
	} {
		if !strings.Contains(string(validation), "VALIDATE CONSTRAINT "+constraint) {
			t.Fatalf("migration 396 does not validate %s", constraint)
		}
	}
	for _, migration := range []string{
		"391_channel_delivery_reconciliation_id_unique.up.sql",
		"392_channel_delivery_reconciliation_generation_unique.up.sql",
		"393_channel_delivery_reconciliation_listing_index.up.sql",
		"394_channel_delivery_reconciliation_authorization_unique.up.sql",
		"397_chat_message_assistant_task_index.up.sql",
	} {
		body, err := os.ReadFile(filepath.Join(root, migration))
		if err != nil || !strings.Contains(string(body), "INDEX CONCURRENTLY") || strings.Count(strings.TrimSpace(string(body)), ";") != 1 {
			t.Fatalf("%s must contain exactly one concurrent index statement: %v", migration, err)
		}
	}
	guard, err := os.ReadFile(filepath.Join(root, "395_channel_delivery_reconciliation_mutation_guard.up.sql"))
	if err != nil || !strings.Contains(string(guard), "receipts are immutable") || !strings.Contains(string(guard), "workspace_teardown") {
		t.Fatalf("migration 395 does not enforce append-only receipts: %v", err)
	}
	rollback, err := os.ReadFile(filepath.Join(root, "390_channel_delivery_reconciliation.down.sql"))
	if err != nil || !strings.Contains(string(rollback), "RAISE EXCEPTION") || !strings.Contains(string(rollback), "reconciliation_count > 0") {
		t.Fatalf("migration 390 rollback can lose reconciliation evidence: %v", err)
	}
}
