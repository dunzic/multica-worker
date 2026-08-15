package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/rolesourcereplay"
)

func TestOperatorPrivateFilesAreStrictAndOutputIsExclusive(t *testing.T) {
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
	if runtime.GOOS != "windows" {
		unsafeDir := filepath.Join(dir, "unsafe")
		if err := os.Mkdir(unsafeDir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unsafeDir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := writeExclusive(filepath.Join(unsafeDir, "authorization.json"), []byte("unsafe")); err == nil {
			t.Fatal("operator output in a world-writable directory was accepted")
		}
	}
}

func TestStrictJSONAndDatabaseDefaultRefusal(t *testing.T) {
	var target struct {
		Value string `json:"value"`
	}
	if err := decodeStrictJSON([]byte(`{"value":"ok","unknown":true}`), &target); err == nil {
		t.Fatal("unknown authorization field was accepted")
	}
	if err := decodeStrictJSON([]byte(`{"value":"ok"} {}`), &target); err == nil {
		t.Fatal("trailing authorization JSON was accepted")
	}
	t.Setenv("DATABASE_URL", "")
	if _, err := openDatabase(t.Context()); err == nil || !strings.Contains(err.Error(), "defaults are forbidden") {
		t.Fatalf("missing database URL error=%v", err)
	}
}

func TestExecuteRejectsNonCanonicalAuthorizationBeforeReadingSignatures(t *testing.T) {
	dir := t.TempDir()
	authorizationPath := filepath.Join(dir, "authorization.json")
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	auth, err := rolesourcereplay.NewAuthorization(rolesourcereplay.Summary{
		OutboxID: "00000000-0000-4000-8000-000000000001", Status: "dead", Attempt: 20, NextGeneration: 1,
		ExpectedReceiptDigest: "sha256:" + strings.Repeat("a", 64),
	}, "dependency_recovered", "sha256:"+strings.Repeat("b", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := rolesourcereplay.CanonicalAuthorization(auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorizationPath, append(canonical, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	err = runExecute(t.Context(), []string{
		"--authorization", authorizationPath,
		"--requester-key-id", "requester_1",
		"--requester-signature-file", filepath.Join(dir, "missing-requester.sig"),
		"--approver-key-id", "approver_1",
		"--approver-signature-file", filepath.Join(dir, "missing-approver.sig"),
	})
	if err == nil || !strings.Contains(err.Error(), "not the exact canonical JSON") {
		t.Fatalf("non-canonical authorization error=%v", err)
	}
}

func TestRunRejectsUnknownAndIncompleteCommands(t *testing.T) {
	if err := run(t.Context(), []string{"unknown"}); err == nil {
		t.Fatal("unknown command was accepted")
	}
	if err := run(t.Context(), []string{"execute"}); err == nil {
		t.Fatal("incomplete execute was accepted")
	}
	if err := run(t.Context(), nil); err == nil {
		t.Fatalf("empty command error=%v", err)
	}
}
