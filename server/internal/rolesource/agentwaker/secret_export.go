package agentwaker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"

	"github.com/multica-ai/multica/server/internal/rolesource"
)

// ExportSecretPayload reopens only the selected role's sensitive files and
// verifies every value/definition against the immutable snapshot declarations.
// A changed source must be rescanned and reapproved before transfer.
func (a *Adapter) ExportSecretPayload(ctx context.Context, request rolesource.ScanRequest, snapshot rolesource.Snapshot, roleID string) (rolesource.SecretEnvelopePayload, error) {
	if err := ctx.Err(); err != nil {
		return rolesource.SecretEnvelopePayload{}, err
	}
	cfg, err := decodeConfig(request.Config)
	if err != nil {
		return rolesource.SecretEnvelopePayload{}, err
	}
	if err := a.validateRoot(cfg.RootPath); err != nil {
		return rolesource.SecretEnvelopePayload{}, err
	}
	sourceFS, err := rolesource.OpenBoundedFS(cfg.RootPath)
	if err != nil {
		return rolesource.SecretEnvelopePayload{}, err
	}
	defer sourceFS.Close() //nolint:errcheck
	role, ok := snapshotRole(snapshot, roleID)
	if !ok {
		return rolesource.SecretEnvelopePayload{}, errors.New("AgentWaker secret export role is absent from snapshot")
	}
	roleDir, err := findRoleDirectory(ctx, sourceFS, roleID)
	if err != nil {
		return rolesource.SecretEnvelopePayload{}, err
	}

	environment := map[string]string{}
	envBody, err := readOptionalSensitiveFile(sourceFS, path.Join(roleDir, "env/.env"), maxContractBytes)
	if err != nil {
		return rolesource.SecretEnvelopePayload{}, err
	}
	defer clear(envBody)
	actual := parseEnv(envBody)
	defer clearParsedEnvironment(actual)
	if len(actual.values) != configuredEnvironmentCount(role.Environment) {
		return rolesource.SecretEnvelopePayload{}, errors.New("AgentWaker environment keys changed after approved scan")
	}
	for _, declaration := range role.Environment {
		value, exists := actual.values[declaration.Name]
		if exists != declaration.Configured {
			return rolesource.SecretEnvelopePayload{}, fmt.Errorf("AgentWaker environment key %q configuration changed", declaration.Name)
		}
		if !exists {
			continue
		}
		if envValueDigest(declaration.Name, value, a.digestKey) != declaration.ValueDigest {
			return rolesource.SecretEnvelopePayload{}, fmt.Errorf("AgentWaker environment key %q changed after approved scan", declaration.Name)
		}
		environment[declaration.Name] = value
	}

	mcpServers, err := exportMCPServers(sourceFS, path.Join(roleDir, "mcp/mcp.json"), role.MCP)
	if err != nil {
		clearStringMap(environment)
		return rolesource.SecretEnvelopePayload{}, err
	}
	return rolesource.SecretEnvelopePayload{Environment: environment, MCPServers: mcpServers}, nil
}

func snapshotRole(snapshot rolesource.Snapshot, roleID string) (rolesource.Role, bool) {
	for _, role := range snapshot.Manifest.Roles {
		if role.ID == roleID {
			return role, true
		}
	}
	return rolesource.Role{}, false
}

func findRoleDirectory(ctx context.Context, sourceFS *rolesource.BoundedFS, roleID string) (string, error) {
	entries, err := sourceFS.ReadDir(".")
	if err != nil {
		return "", err
	}
	found := ""
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !entry.IsDir() {
			continue
		}
		candidate := path.Join(entry.Name(), "agent-soul/PROFILE.yaml")
		body, err := sourceFS.ReadFile(candidate, maxContractBytes)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		var sourceProfile profile
		if err := decodeYAMLStrict(body, &sourceProfile); err != nil {
			return "", err
		}
		if sourceProfile.ID == roleID {
			if found != "" {
				return "", errors.New("AgentWaker role identity is duplicated")
			}
			found = entry.Name()
		}
	}
	if found == "" {
		return "", errors.New("AgentWaker role directory disappeared after approved scan")
	}
	return found, nil
}

func readOptionalSensitiveFile(sourceFS *rolesource.BoundedFS, name string, limit int64) ([]byte, error) {
	body, err := sourceFS.ReadFile(name, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return body, err
}

func exportMCPServers(sourceFS *rolesource.BoundedFS, name string, declarations []rolesource.MCPServer) (map[string]json.RawMessage, error) {
	body, err := readOptionalSensitiveFile(sourceFS, name, maxContractBytes)
	if err != nil {
		return nil, err
	}
	defer clear(body)
	if body == nil {
		if len(declarations) != 0 {
			return nil, errors.New("AgentWaker MCP file disappeared after approved scan")
		}
		return map[string]json.RawMessage{}, nil
	}
	var config mcpConfig
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	defer func() {
		for id, raw := range config.Servers {
			clear(raw)
			delete(config.Servers, id)
		}
	}()
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("AgentWaker MCP file contains trailing JSON")
	}
	if len(config.Servers) != len(declarations) {
		return nil, errors.New("AgentWaker MCP server set changed after approved scan")
	}
	declarationByID := make(map[string]rolesource.MCPServer, len(declarations))
	for _, declaration := range declarations {
		declarationByID[declaration.ID] = declaration
	}
	result := make(map[string]json.RawMessage, len(config.Servers))
	for id, raw := range config.Servers {
		canonical, err := canonicalJSON(raw)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(canonical)
		declaration, ok := declarationByID[id]
		if !ok || declaration.DefinitionHash != "sha256:"+hex.EncodeToString(sum[:]) {
			clear(canonical)
			return nil, fmt.Errorf("AgentWaker MCP server %q changed after approved scan", id)
		}
		refs := envRefPattern.FindAllSubmatch(canonical, -1)
		envNames := make([]string, 0, len(refs))
		seen := map[string]bool{}
		for _, match := range refs {
			name := string(match[1])
			if !seen[name] {
				seen[name] = true
				envNames = append(envNames, name)
			}
		}
		sort.Strings(envNames)
		if !equalStrings(envNames, declaration.Environment) {
			clear(canonical)
			return nil, fmt.Errorf("AgentWaker MCP server %q environment references changed", id)
		}
		result[id] = append(json.RawMessage(nil), canonical...)
		clear(canonical)
	}
	return result, nil
}

func configuredEnvironmentCount(entries []rolesource.EnvironmentKey) int {
	count := 0
	for _, entry := range entries {
		if entry.Configured {
			count++
		}
	}
	return count
}

func clearParsedEnvironment(environment parsedEnv) {
	for name, value := range environment.values {
		environment.values[name] = ""
		_ = value
	}
	clear(environment.values)
}

func clearStringMap(values map[string]string) {
	for key := range values {
		values[key] = ""
	}
	clear(values)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
