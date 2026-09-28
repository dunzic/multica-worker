package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// emailMcpCmd is the built-in, task-scoped Email MCP server. It uses the
// newline-delimited JSON-RPC transport required by MCP stdio servers. The
// command deliberately has no identity flags: daemon-provided environment
// variables are the only source of actor identity.
var emailMcpCmd = &cobra.Command{
	Use:    "email-mcp",
	Short:  "Run the task-scoped Email MCP server over stdio",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runEmailMcp,
}

const emailMcpProtocolVersion = "2025-06-18"

var emailMcpProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

func init() {
	emailMcpCmd.GroupID = groupRuntime
	rootCmd.AddCommand(emailMcpCmd)
}

type emailMcpServer struct {
	client *cli.APIClient
}

// newEmailMcpServer builds a client from the daemon task environment. In
// particular, it does not use resolveToken or any command flag because those
// paths can fall back to a user's profile token outside a managed task.
func newEmailMcpServer() (*emailMcpServer, error) {
	token := strings.TrimSpace(os.Getenv("MULTICA_TOKEN"))
	if !strings.HasPrefix(token, "mat_") {
		return nil, fmt.Errorf("email MCP requires MULTICA_TOKEN to be a task-scoped mat_ token")
	}

	identity := []struct {
		name  string
		value string
	}{
		{"MULTICA_AGENT_ID", strings.TrimSpace(os.Getenv("MULTICA_AGENT_ID"))},
		{"MULTICA_TASK_ID", strings.TrimSpace(os.Getenv("MULTICA_TASK_ID"))},
		{"MULTICA_WORKSPACE_ID", strings.TrimSpace(os.Getenv("MULTICA_WORKSPACE_ID"))},
	}
	for _, field := range identity {
		if field.value == "" {
			return nil, fmt.Errorf("email MCP requires daemon-provided %s", field.name)
		}
	}

	serverURL := strings.TrimSpace(os.Getenv("MULTICA_SERVER_URL"))
	if serverURL == "" {
		return nil, fmt.Errorf("email MCP requires daemon-provided MULTICA_SERVER_URL")
	}

	client := cli.NewAPIClient(normalizeAPIBaseURL(serverURL), identity[2].value, token)
	client.AgentID = identity[0].value
	client.TaskID = identity[1].value
	return &emailMcpServer{client: client}, nil
}

func runEmailMcp(cmd *cobra.Command, _ []string) error {
	server, err := newEmailMcpServer()
	if err != nil {
		return err
	}
	return serveEmailMcp(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), server)
}

type emailMcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type emailMcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *emailMcpError  `json:"error,omitempty"`
}

type emailMcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type emailMcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type emailMcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type emailMcpToolResult struct {
	Content []emailMcpContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

type emailMcpEnvelope struct {
	To       []string `json:"to"`
	Subject  string   `json:"subject"`
	HTMLBody string   `json:"html_body,omitempty"`
	TextBody string   `json:"text_body,omitempty"`
}

type emailMcpSendArgs struct {
	emailMcpEnvelope
	IdempotencyKey string `json:"idempotency_key"`
}

type emailMcpStatusArgs struct {
	MessageID string `json:"message_id"`
}

func serveEmailMcp(ctx context.Context, in io.Reader, out io.Writer, server *emailMcpServer) error {
	scanner := bufio.NewScanner(in)
	// HTML bodies are allowed in tool arguments. Keep enough headroom for a
	// useful message while still bounding memory used by malformed input.
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		request, errorCode := decodeEmailMcpRequest([]byte(line))
		if errorCode != 0 {
			message := "invalid request"
			if errorCode == -32700 {
				message = "parse error"
			}
			if err := encoder.Encode(emailMcpResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &emailMcpError{Code: errorCode, Message: message},
			}); err != nil {
				return err
			}
			continue
		}
		response := server.handle(ctx, request)
		if response == nil {
			continue
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func decodeEmailMcpRequest(line []byte) (emailMcpRequest, int) {
	var value any
	if err := json.Unmarshal(line, &value); err != nil {
		return emailMcpRequest{}, -32700
	}
	if _, ok := value.(map[string]any); !ok {
		return emailMcpRequest{}, -32600
	}
	var request emailMcpRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return emailMcpRequest{}, -32600
	}
	return request, 0
}

func (s *emailMcpServer) handle(ctx context.Context, request emailMcpRequest) *emailMcpResponse {
	id := request.ID
	if id == nil {
		id = json.RawMessage("null")
	}
	response := &emailMcpResponse{JSONRPC: "2.0", ID: id}
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		response.Error = &emailMcpError{Code: -32600, Message: "invalid request"}
		return response
	}
	if request.ID == nil {
		// Valid notifications, including notifications/initialized, never
		// receive a response.
		return nil
	}

	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": negotiateEmailMcpProtocolVersion(request.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]string{
				"name":    "multica-email",
				"version": version,
			},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": emailMcpTools()}
	case "tools/call":
		params, err := decodeToolCallParams(request.Params)
		if err != nil {
			response.Error = &emailMcpError{Code: -32602, Message: err.Error()}
			return response
		}
		result := s.callTool(ctx, params.Name, params.Arguments)
		response.Result = result
	default:
		response.Error = &emailMcpError{Code: -32601, Message: "method not found"}
	}
	return response
}

func negotiateEmailMcpProtocolVersion(raw json.RawMessage) string {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &params) == nil && emailMcpProtocolVersions[params.ProtocolVersion] {
		return params.ProtocolVersion
	}
	return emailMcpProtocolVersion
}

func emailMcpTools() []emailMcpTool {
	return []emailMcpTool{
		{Name: "preview", Description: "Preview an email without sending it", InputSchema: emailMcpEnvelopeSchema(false)},
		{Name: "send", Description: "Queue an email through the managed notification gateway", InputSchema: emailMcpEnvelopeSchema(true)},
		{Name: "status", Description: "Show safe delivery status for an email", InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"message_id": map[string]any{"type": "string", "minLength": 1}},
			"required":             []string{"message_id"},
			"additionalProperties": false,
		}},
	}
}

func emailMcpEnvelopeSchema(send bool) map[string]any {
	properties := map[string]any{
		"to":        map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "minItems": 1},
		"subject":   map[string]any{"type": "string", "minLength": 1},
		"html_body": map[string]any{"type": "string", "minLength": 1},
		"text_body": map[string]any{"type": "string", "minLength": 1},
	}
	required := []string{"to", "subject"}
	if send {
		properties["idempotency_key"] = map[string]any{"type": "string", "minLength": 1}
		required = append(required, "idempotency_key")
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"anyOf":                []any{map[string]any{"required": []string{"html_body"}}, map[string]any{"required": []string{"text_body"}}},
		"additionalProperties": false,
	}
}

type emailMcpToolCallParams struct {
	Name      string
	Arguments json.RawMessage
}

func decodeToolCallParams(raw json.RawMessage) (emailMcpToolCallParams, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return emailMcpToolCallParams{}, fmt.Errorf("tools/call params are required")
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return emailMcpToolCallParams{}, fmt.Errorf("invalid tools/call params: %w", err)
	}
	if strings.TrimSpace(params.Name) == "" {
		return emailMcpToolCallParams{}, fmt.Errorf("tools/call name is required")
	}
	if len(params.Arguments) == 0 || string(params.Arguments) == "null" {
		params.Arguments = json.RawMessage("{}")
	}
	return emailMcpToolCallParams{Name: params.Name, Arguments: params.Arguments}, nil
}

func (s *emailMcpServer) callTool(ctx context.Context, name string, raw json.RawMessage) emailMcpToolResult {
	var result any
	var err error
	switch name {
	case "preview":
		var args emailMcpEnvelope
		err = decodeEmailMcpArguments(raw, map[string]bool{"to": true, "subject": true, "html_body": true, "text_body": true}, &args)
		if err == nil {
			err = normalizeEmailMcpEnvelope(&args)
		}
		if err == nil {
			result, err = s.postEmail(ctx, "/v1/emails/preview", args, "")
		}
	case "send":
		var args emailMcpSendArgs
		err = decodeEmailMcpArguments(raw, map[string]bool{"to": true, "subject": true, "html_body": true, "text_body": true, "idempotency_key": true}, &args)
		if err == nil {
			err = normalizeEmailMcpEnvelope(&args.emailMcpEnvelope)
		}
		if err == nil && strings.TrimSpace(args.IdempotencyKey) == "" {
			err = fmt.Errorf("idempotency_key is required")
		}
		if err == nil {
			result, err = s.postEmail(ctx, "/v1/emails/send", args.emailMcpEnvelope, strings.TrimSpace(args.IdempotencyKey))
		}
	case "status":
		var args emailMcpStatusArgs
		err = decodeEmailMcpArguments(raw, map[string]bool{"message_id": true}, &args)
		if err == nil && strings.TrimSpace(args.MessageID) == "" {
			err = fmt.Errorf("message_id is required")
		}
		if err == nil {
			result = map[string]any{}
			apiCtx, cancel := cli.APIContext(ctx)
			err = s.client.GetJSON(apiCtx, "/v1/emails/"+url.PathEscape(strings.TrimSpace(args.MessageID)), &result)
			cancel()
		}
	default:
		err = fmt.Errorf("unknown tool %q", name)
	}
	if err != nil {
		return emailMcpToolResult{Content: []emailMcpContent{{Type: "text", Text: safeEmailMcpError(err)}}, IsError: true}
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return emailMcpToolResult{Content: []emailMcpContent{{Type: "text", Text: "encode tool result: " + marshalErr.Error()}}, IsError: true}
	}
	return emailMcpToolResult{Content: []emailMcpContent{{Type: "text", Text: string(data)}}}
}

func safeEmailMcpError(err error) string {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) {
		return err.Error()
	}
	var payload struct {
		ErrorCode string `json:"error_code"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &payload) == nil && isSafeEmailErrorCode(payload.ErrorCode) {
		return fmt.Sprintf("email gateway request failed with HTTP %d: %s", httpErr.StatusCode, payload.ErrorCode)
	}
	return fmt.Sprintf("email gateway request failed with HTTP %d", httpErr.StatusCode)
}

func isSafeEmailErrorCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, char := range code {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func decodeEmailMcpArguments(raw json.RawMessage, allowed map[string]bool, out any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("tool arguments must be a JSON object")
	}
	for field := range fields {
		if !allowed[field] {
			return fmt.Errorf("unsupported tool argument %q", field)
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func normalizeEmailMcpEnvelope(args *emailMcpEnvelope) error {
	if len(args.To) == 0 {
		return fmt.Errorf("at least one to address is required")
	}
	for i := range args.To {
		args.To[i] = strings.TrimSpace(args.To[i])
		if args.To[i] == "" {
			return fmt.Errorf("to cannot contain an empty address")
		}
	}
	if strings.TrimSpace(args.Subject) == "" {
		return fmt.Errorf("subject is required")
	}
	if strings.TrimSpace(args.HTMLBody) == "" && strings.TrimSpace(args.TextBody) == "" {
		return fmt.Errorf("at least one email body is required")
	}
	return nil
}

func (s *emailMcpServer) postEmail(ctx context.Context, path string, body any, idempotencyKey string) (map[string]any, error) {
	apiCtx, cancel := cli.APIContext(ctx)
	defer cancel()
	var result map[string]any
	if idempotencyKey == "" {
		if err := s.client.PostJSON(apiCtx, path, body, &result); err != nil {
			return nil, err
		}
		return result, nil
	}
	headers := make(http.Header)
	headers.Set("Idempotency-Key", idempotencyKey)
	if err := s.client.PostJSONWithHeaders(apiCtx, path, body, headers, &result); err != nil {
		return nil, err
	}
	return result, nil
}
