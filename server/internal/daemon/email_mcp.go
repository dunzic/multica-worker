package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	builtinEmailMCPServerName = "multica-email"
	emailMCPEnabledEnv        = "MULTICA_AGENT_EMAIL_MCP_ENABLED"
)

func builtinEmailMCPEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(emailMCPEnabledEnv)))
	return err == nil && enabled
}

func providerSupportsBuiltinEmailMCP(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "codebuddy", "codex", "opencode", "openclaw", "hermes", "kimi", "reasonix", "dsh", "kiro", "qoder", "qoderclicn", "traecli", "grok", "qwen", "qwenpaw":
		return true
	default:
		return false
	}
}

func buildBuiltinEmailMCPConfig(provider string, raw json.RawMessage, command string) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		runtimeServers, supported, err := loadRuntimeMcpServerConfigs(provider)
		if err != nil {
			return nil, err
		}
		if supported {
			encoded, err := json.Marshal(map[string]any{"mcpServers": runtimeServers})
			if err != nil {
				return nil, fmt.Errorf("marshal runtime MCP config: %w", err)
			}
			raw = encoded
		}
	}
	return injectBuiltinEmailMCP(raw, command)
}

// injectBuiltinEmailMCP appends the server-owned email adapter after all
// runtime and agent MCP overlays. The reserved name therefore cannot be
// replaced by an agent-supplied command. Credentials are intentionally absent:
// the child process receives only the task-scoped environment at launch.
func injectBuiltinEmailMCP(raw json.RawMessage, command string) (json.RawMessage, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("email MCP command is required")
	}

	document := map[string]any{}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &document); err != nil {
			return nil, fmt.Errorf("parse effective MCP config: %w", err)
		}
	}

	servers := map[string]any{}
	// Preserve legacy OpenCode-shaped input if the runtime merge did not
	// normalize it, then let the canonical envelope win on collisions.
	if legacy, ok := nestedRuntimeMcpMap(document, "mcp"); ok {
		for name, entry := range legacy {
			servers[name] = entry
		}
	}
	if canonical, ok := nestedRuntimeMcpMap(document, "mcpServers"); ok {
		for name, entry := range canonical {
			servers[name] = entry
		}
	}
	servers[builtinEmailMCPServerName] = map[string]any{
		"command": command,
		"args":    []string{"email-mcp"},
	}

	encoded, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return nil, fmt.Errorf("marshal email MCP config: %w", err)
	}
	return encoded, nil
}
