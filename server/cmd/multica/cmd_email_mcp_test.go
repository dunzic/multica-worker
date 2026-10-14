package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestNewEmailMcpServerRequiresTaskIdentity(t *testing.T) {
	t.Setenv("MULTICA_TOKEN", "mat_task")
	t.Setenv("MULTICA_SERVER_URL", "http://example.test")
	for _, name := range []string{"MULTICA_AGENT_ID", "MULTICA_TASK_ID", "MULTICA_WORKSPACE_ID"} {
		t.Setenv(name, "")
		if _, err := newEmailMcpServer(); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("missing %s: error = %v", name, err)
		}
		t.Setenv(name, "value")
	}

	t.Setenv("MULTICA_TOKEN", "mul_user_token")
	if _, err := newEmailMcpServer(); err == nil || !strings.Contains(err.Error(), "mat_") {
		t.Fatalf("non-task token: error = %v", err)
	}

	t.Setenv("MULTICA_TOKEN", "mat_task")
	t.Setenv("MULTICA_SERVER_URL", "")
	if _, err := newEmailMcpServer(); err == nil || !strings.Contains(err.Error(), "MULTICA_SERVER_URL") {
		t.Fatalf("missing server URL: error = %v", err)
	}
}

func TestServeEmailMcpProtocolAndTools(t *testing.T) {
	server := &emailMcpServer{client: cli.NewAPIClient("http://unused", "ws", "mat_task")}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"does/not-exist"}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := serveEmailMcp(context.Background(), strings.NewReader(input), &output, server); err != nil {
		t.Fatalf("serveEmailMcp: %v", err)
	}
	dec := json.NewDecoder(&output)
	var initialize, tools, ping, unknown map[string]any
	for _, target := range []*map[string]any{&initialize, &tools, &ping, &unknown} {
		if err := dec.Decode(target); err != nil {
			t.Fatalf("decode response: %v (output %q)", err, output.String())
		}
	}
	if _, ok := initialize["result"].(map[string]any); !ok {
		t.Fatalf("initialize response = %#v", initialize)
	}
	initResult := initialize["result"].(map[string]any)
	if initResult["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v", initResult["protocolVersion"])
	}
	toolResult := tools["result"].(map[string]any)
	toolList := toolResult["tools"].([]any)
	if len(toolList) != 3 {
		t.Fatalf("tool count = %d", len(toolList))
	}
	for i, want := range []string{"preview", "send", "status"} {
		if got := toolList[i].(map[string]any)["name"]; got != want {
			t.Fatalf("tool %d name = %v, want %q", i, got, want)
		}
	}
	if ping["result"].(map[string]any) == nil {
		t.Fatal("ping result missing")
	}
	if unknown["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("unknown method response = %#v", unknown)
	}
	var extra map[string]any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("notification unexpectedly produced another response: %#v, err=%v", extra, err)
	}
}

func TestEmailMcpProtocolVersionNegotiation(t *testing.T) {
	for _, version := range []string{"2024-11-05", "2025-03-26", "2025-06-18"} {
		params := json.RawMessage(`{"protocolVersion":"` + version + `"}`)
		if got := negotiateEmailMcpProtocolVersion(params); got != version {
			t.Fatalf("requested %q, got %q", version, got)
		}
	}
	if got := negotiateEmailMcpProtocolVersion(json.RawMessage(`{"protocolVersion":"2099-01-01"}`)); got != emailMcpProtocolVersion {
		t.Fatalf("unknown version fallback = %q", got)
	}
}

func TestServeEmailMcpDistinguishesParseAndInvalidRequests(t *testing.T) {
	server := &emailMcpServer{client: cli.NewAPIClient("http://unused", "ws", "mat_task")}
	input := "not-json\n[]\nnull\n{}\n"
	var output bytes.Buffer
	if err := serveEmailMcp(context.Background(), strings.NewReader(input), &output, server); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	for index, wantCode := range []float64{-32700, -32600, -32600, -32600} {
		var response map[string]any
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("response %d: %v", index, err)
		}
		errorBody := response["error"].(map[string]any)
		if errorBody["code"] != wantCode || response["id"] != nil {
			t.Fatalf("response %d = %#v", index, response)
		}
	}
}

func TestEmailMcpToolsUseTaskIdentityAndEndpoints(t *testing.T) {
	var requests []struct {
		method string
		path   string
		header http.Header
		body   map[string]any
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		requests = append(requests, struct {
			method string
			path   string
			header http.Header
			body   map[string]any
		}{r.Method, r.URL.Path, r.Header.Clone(), body})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg-1","status":"queued"}`)
	}))
	defer ts.Close()
	client := cli.NewAPIClient(ts.URL, "workspace-from-env", "mat_task")
	client.AgentID = "agent-from-env"
	client.TaskID = "task-from-env"
	server := &emailMcpServer{client: client}

	preview := server.callTool(context.Background(), "preview", json.RawMessage(`{"to":["a@example.com"],"subject":"Hi","text_body":"Body"}`))
	if preview.IsError {
		t.Fatalf("preview error: %#v", preview)
	}
	send := server.callTool(context.Background(), "send", json.RawMessage(`{"to":["a@example.com"],"subject":"Hi","text_body":"Body","idempotency_key":"idem-1"}`))
	if send.IsError {
		t.Fatalf("send error: %#v", send)
	}
	status := server.callTool(context.Background(), "status", json.RawMessage(`{"message_id":"msg-1"}`))
	if status.IsError {
		t.Fatalf("status error: %#v", status)
	}
	if len(requests) != 3 {
		t.Fatalf("request count = %d", len(requests))
	}
	if requests[0].path != "/v1/emails/preview" || requests[1].path != "/v1/emails/send" || requests[2].path != "/v1/emails/msg-1" {
		t.Fatalf("paths = %#v", requests)
	}
	for _, request := range requests {
		if request.header.Get("Authorization") != "Bearer mat_task" || request.header.Get("X-Agent-ID") != "agent-from-env" || request.header.Get("X-Task-ID") != "task-from-env" || request.header.Get("X-Workspace-ID") != "workspace-from-env" {
			t.Fatalf("identity headers = %#v", request.header)
		}
	}
	if got := requests[1].header.Get("Idempotency-Key"); got != "idem-1" {
		t.Fatalf("idempotency header = %q", got)
	}
}

func TestEmailMcpRejectsIdentityArguments(t *testing.T) {
	server := &emailMcpServer{client: cli.NewAPIClient("http://unused", "ws", "mat_task")}
	result := server.callTool(context.Background(), "preview", json.RawMessage(`{"to":["a@example.com"],"subject":"Hi","text_body":"Body","agent_id":"attacker"}`))
	if !result.IsError || !strings.Contains(result.Content[0].Text, "unsupported tool argument") {
		t.Fatalf("result = %#v", result)
	}
}

func TestEmailMcpHTTPErrorExposesOnlyStableErrorCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error_code":"EMAIL_APPROVAL_REQUIRED","provider_response":"secret-provider-body"}`)
	}))
	defer ts.Close()
	server := &emailMcpServer{client: cli.NewAPIClient(ts.URL, "ws", "mat_task")}

	result := server.callTool(context.Background(), "preview", json.RawMessage(`{"to":["a@example.com"],"subject":"Hi","text_body":"Body"}`))
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("result = %#v", result)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "HTTP 403: EMAIL_APPROVAL_REQUIRED") {
		t.Fatalf("error = %q", text)
	}
	if strings.Contains(text, "provider") || strings.Contains(text, "secret") {
		t.Fatalf("unsafe response body leaked: %q", text)
	}
}
