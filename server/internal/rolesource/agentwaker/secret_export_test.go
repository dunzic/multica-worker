package agentwaker

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/rolesource"
)

func TestSecretExportMatchesApprovedSnapshot(t *testing.T) {
	root := writeFixture(t, "super-secret-token")
	registry, err := rolesource.NewRegistry(newTestAdapter(t))
	if err != nil {
		t.Fatal(err)
	}
	request := rolesource.ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root)}
	snapshot, err := registry.Scan(context.Background(), Kind, request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := registry.ExportSecretPayload(context.Background(), Kind, request, snapshot, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if payload.Environment["API_TOKEN"] != "super-secret-token" || payload.Environment["API_BASE"] != "https://api.example.com" {
		t.Fatalf("environment = %+v", payload.Environment)
	}
	if len(payload.MCPServers) != 1 || !strings.Contains(string(payload.MCPServers["platform-api"]), "platform-mcp") {
		t.Fatalf("MCP servers = %+v", payload.MCPServers)
	}
}

func TestSecretExportRejectsEnvironmentChangedAfterScan(t *testing.T) {
	root := writeFixture(t, "first-secret")
	registry, err := rolesource.NewRegistry(newTestAdapter(t))
	if err != nil {
		t.Fatal(err)
	}
	request := rolesource.ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root)}
	snapshot, err := registry.Scan(context.Background(), Kind, request)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, root, "writer/env/.env", "API_TOKEN=second-secret\nAPI_BASE=https://api.example.com\n")
	if _, err := registry.ExportSecretPayload(context.Background(), Kind, request, snapshot, "writer"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed environment error = %v", err)
	}
}

func TestSecretExportRejectsMCPChangedAfterScan(t *testing.T) {
	root := writeFixture(t, "secret")
	registry, err := rolesource.NewRegistry(newTestAdapter(t))
	if err != nil {
		t.Fatal(err)
	}
	request := rolesource.ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root)}
	snapshot, err := registry.Scan(context.Background(), Kind, request)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, root, "writer/mcp/mcp.json", `{"mcpServers":{"platform-api":{"command":"different-mcp"}}}`)
	if _, err := registry.ExportSecretPayload(context.Background(), Kind, request, snapshot, "writer"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed MCP error = %v", err)
	}
}
