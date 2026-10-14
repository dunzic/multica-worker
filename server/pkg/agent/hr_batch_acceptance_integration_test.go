//go:build agentintegration

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This explicit, local-only acceptance launches the checkout's managed daemon
// and drives the real browser/API flow. It may use an authenticated provider
// account and quota; the default test suite cannot run it.
func TestHRBatchResumeAcceptance(t *testing.T) {
	requireRealAgentSmoke(t)
	if os.Getenv("MULTICA_HR_ACCEPTANCE") != "1" {
		t.Skip("set MULTICA_HR_ACCEPTANCE=1 for the managed local HR acceptance")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	devHome := os.Getenv("MULTICA_DEV_HOME")
	rel, err := filepath.Rel(filepath.Join(root, ".context"), devHome)
	if err != nil || !filepath.IsLocal(rel) {
		t.Fatal("acceptance requires a managed registry inside this checkout's .context")
	}
	if _, err := os.Stat(filepath.Join(root, ".env.worktree")); err != nil {
		t.Fatal("acceptance requires an isolated .env.worktree")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	defer cancel()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		t.Logf("%s", out)
		if err != nil {
			t.Fatalf("%s failed: %v", args[0], err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "scripts/dev-env.sh", "down", "--components", "daemon")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		t.Logf("stop acceptance daemon: %s", out)
		if err != nil {
			t.Errorf("stop acceptance daemon: %v", err)
		}
	})
	run("make", "up", "C=api,web,daemon")
	run("pnpm", "exec", "playwright", "test", "e2e/hr-batch-resumes-live.spec.ts", "--reporter=list", "--output=test-results/hr-playwright")
}
