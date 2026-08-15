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
