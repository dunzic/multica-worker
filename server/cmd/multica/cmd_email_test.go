package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func setEmailHumanTestEnv(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", serverURL)
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_TOKEN", "human-token")
	t.Setenv("MULTICA_DAEMON_PORT", "")
}

func TestAgentEmailGovernanceCommandsRegistered(t *testing.T) {
	for _, name := range []string{"configure", "request-approval", "approval-status", "approve", "reject", "revoke"} {
		cmd, _, err := agentEmailCmd.Find([]string{name})
		if err != nil || cmd == nil || cmd.Name() != name {
			t.Fatalf("agent email %s command not registered: cmd=%v err=%v", name, cmd, err)
		}
	}
}

func TestRunAgentEmailConfigureRequestShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "pending"})
	}))
	defer server.Close()
	setEmailHumanTestEnv(t, server.URL)

	cmd := &cobra.Command{Use: "configure"}
	addCommonProfileFlags(cmd)
	cmd.Flags().String("from", "", "")
	cmd.Flags().StringArray("allow-to", nil, "")
	cmd.Flags().Int("per-minute", 0, "")
	cmd.Flags().Int("per-day", 0, "")
	cmd.Flags().Int("max-recipients-per-message", 0, "")
	cmd.SetOut(&bytes.Buffer{})
	_ = cmd.Flags().Set("from", "notifications@example.com")
	_ = cmd.Flags().Set("allow-to", "one@example.com")
	_ = cmd.Flags().Set("allow-to", "two@example.com")
	_ = cmd.Flags().Set("per-minute", "10")
	_ = cmd.Flags().Set("per-day", "200")
	if err := runAgentEmailConfigure(cmd, []string{"agent-123"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/workspaces/workspace-123/agents/agent-123/email-policy" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody["from_address"] != "notifications@example.com" {
		t.Fatalf("body = %#v", gotBody)
	}
	limits, ok := gotBody["limits"].(map[string]any)
	if !ok || limits["per_minute"] != float64(10) || limits["per_day"] != float64(200) {
		t.Fatalf("limits = %#v", gotBody["limits"])
	}
	if _, present := limits["max_recipients_per_message"]; present {
		t.Fatalf("unset max_recipients_per_message must use the deployment default: %#v", limits)
	}
}

func TestAgentEmailApprovalActionsUseDocumentedPathsAndHumanGuard(t *testing.T) {
	t.Run("reject path and reason", func(t *testing.T) {
		var gotPath string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"decision": "rejected"})
		}))
		defer server.Close()
		setEmailHumanTestEnv(t, server.URL)
		cmd := &cobra.Command{Use: "reject"}
		addCommonProfileFlags(cmd)
		cmd.Flags().String("reason", "", "")
		cmd.SetOut(&bytes.Buffer{})
		_ = cmd.Flags().Set("reason", "policy_incomplete")
		if err := runAgentEmailReject(cmd, []string{"agent-123", "approval-123"}); err != nil {
			t.Fatal(err)
		}
		if gotPath != "/api/workspaces/workspace-123/agents/agent-123/email-approvals/approval-123/reject" {
			t.Fatalf("path = %q", gotPath)
		}
		if gotBody["reason_code"] != "policy_incomplete" {
			t.Fatalf("body = %#v", gotBody)
		}
	})

	t.Run("daemon task rejected before network", func(t *testing.T) {
		called := false
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		defer server.Close()
		setEmailHumanTestEnv(t, server.URL)
		t.Setenv("MULTICA_AGENT_ID", "agent-task")
		t.Setenv("MULTICA_TASK_ID", "task-123")
		t.Setenv("MULTICA_TOKEN", "mat_test")
		cmd := &cobra.Command{Use: "approve"}
		addCommonProfileFlags(cmd)
		err := runAgentEmailApprove(cmd, []string{"agent-123", "approval-123"})
		if err == nil || !strings.Contains(err.Error(), "daemon-managed task") {
			t.Fatalf("error = %v", err)
		}
		if called {
			t.Fatal("approval endpoint called from daemon task")
		}
	})
}

func TestAgentEmailGovernanceCommandPaths(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		reason     string
		runCommand func(*cobra.Command) error
	}{
		{
			name: "request approval", method: http.MethodPost,
			path: "/api/workspaces/workspace-123/agents/agent-123/email-approval-requests",
			runCommand: func(cmd *cobra.Command) error {
				return runAgentEmailRequestApproval(cmd, []string{"agent-123"})
			},
		},
		{
			name: "approval status", method: http.MethodGet,
			path: "/api/workspaces/workspace-123/agents/agent-123/email-policy/status",
			runCommand: func(cmd *cobra.Command) error {
				return runAgentEmailApprovalStatus(cmd, []string{"agent-123"})
			},
		},
		{
			name: "approve", method: http.MethodPost,
			path: "/api/workspaces/workspace-123/agents/agent-123/email-approvals/request-123/approve",
			runCommand: func(cmd *cobra.Command) error {
				return runAgentEmailApprove(cmd, []string{"agent-123", "request-123"})
			},
		},
		{
			name: "revoke", method: http.MethodPost, reason: "operator_revoked",
			path: "/api/workspaces/workspace-123/agents/agent-123/email-approvals/approval-123/revoke",
			runCommand: func(cmd *cobra.Command) error {
				return runAgentEmailRevoke(cmd, []string{"agent-123", "approval-123"})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.URL.Path != test.path {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				if test.reason != "" {
					var body agentEmailDecisionRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.ReasonCode != test.reason {
						t.Fatalf("reason_code = %q", body.ReasonCode)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"state": "ok"})
			}))
			defer server.Close()
			setEmailHumanTestEnv(t, server.URL)

			cmd := &cobra.Command{Use: test.name}
			addCommonProfileFlags(cmd)
			cmd.SetOut(&bytes.Buffer{})
			if test.reason != "" {
				cmd.Flags().String("reason", "", "")
				_ = cmd.Flags().Set("reason", test.reason)
			}
			if err := test.runCommand(cmd); err != nil {
				t.Fatal(err)
			}
		})
	}
}
