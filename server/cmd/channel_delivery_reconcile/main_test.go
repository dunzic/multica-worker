package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/delivery"
)

func TestOperatorFilesAndCanonicalAuthorization(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "authorization.json")
	if err := os.WriteFile(privatePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(privatePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privatePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(privatePath); err == nil {
		t.Fatal("world-readable private operator file was accepted")
	}
	if runtime.GOOS != "windows" {
		linkPath := filepath.Join(dir, "link")
		if err := os.Symlink(privatePath, linkPath); err != nil {
			t.Fatal(err)
		}
		if _, err := readPrivateFile(linkPath); err == nil {
			t.Fatal("symlink operator file was accepted")
		}
	}
	output := filepath.Join(dir, "new.json")
	if err := writeExclusive(output, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeExclusive(output, []byte("second")); err == nil {
		t.Fatal("exclusive output overwrote an existing authorization")
	}
}

func TestExecuteRejectsNonCanonicalAuthorizationBeforeSignatures(t *testing.T) {
	dir := t.TempDir()
	auth, err := delivery.NewReconciliationAuthorization(delivery.ReconciliationSummary{
		DeliveryID: "00000000-0000-4000-8000-000000000001", Status: "ambiguous", NextGeneration: 1,
		ExpectedAmbiguityEvidenceHash: "sha256:" + strings.Repeat("a", 64),
	}, delivery.ReconciliationConfirmedNotDelivered, delivery.ReconciliationProviderNonDeliveryConfirmed,
		"sha256:"+strings.Repeat("b", 64), time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := delivery.CanonicalReconciliationAuthorization(auth)
	if err != nil {
		t.Fatal(err)
	}
	authorizationPath := filepath.Join(dir, "authorization.json")
	if err := os.WriteFile(authorizationPath, append(canonical, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	err = runExecute(t.Context(), []string{
		"--authorization", authorizationPath, "--requester-key-id", "requester_1",
		"--requester-signature-file", filepath.Join(dir, "missing-requester.sig"),
		"--approver-key-id", "approver_1", "--approver-signature-file", filepath.Join(dir, "missing-approver.sig"),
	})
	if err == nil || !strings.Contains(err.Error(), "not the exact canonical JSON") {
		t.Fatalf("non-canonical authorization error=%v", err)
	}
}

func TestRunAndDatabaseDefaultRefusal(t *testing.T) {
	if err := run(t.Context(), nil); err == nil {
		t.Fatal("empty command was accepted")
	}
	if err := run(t.Context(), []string{"unknown"}); err == nil {
		t.Fatal("unknown command was accepted")
	}
	t.Setenv("DATABASE_URL", "")
	if _, err := openDatabase(t.Context()); err == nil || !strings.Contains(err.Error(), "defaults are forbidden") {
		t.Fatalf("missing database URL error=%v", err)
	}
}
