package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() { attachmentCmd.AddCommand(newReportFeedbackCommand()) }

func newReportFeedbackCommand() *cobra.Command {
	root := &cobra.Command{Use: "feedback", Short: "Read or save a report's native feedback records", Long: `Feedback belongs to the original attachment and inherits its workspace and conversation permissions.
HTML stays sandboxed presentation. This API, not HTML scripts or browser storage, persists shared data.
Configure 1-20 fields (text, number, date, boolean, select, multi_select).
Use stable field and row IDs. Read current revisions before saving; 409 means review the newer data, never force overwrite.
Task credentials may initialize their own uploaded report and seed new rows only. They cannot change an existing schema or overwrite human feedback.
Web and desktop show these records in the report preview's Report feedback panel.`}
	for _, op := range []string{"get", "history", "configure", "save", "archive"} {
		use := op + " <attachment-id>"
		n := 1
		if op == "save" || op == "archive" {
			use += " <row-id>"
			n = 2
		}
		c := &cobra.Command{Use: use, Args: exactArgs(n), Short: map[string]string{"get": "Read fields, records and revisions", "history": "Read the change log", "configure": "Configure fields using a JSON file", "save": "Create or update a record using a JSON file", "archive": "Archive a record using its current revision"}[op]}
		operation := op
		c.RunE = func(cmd *cobra.Command, args []string) error { return runReportFeedback(cmd, args, operation) }
		if op == "configure" || op == "save" || op == "archive" {
			c.Flags().String("file", "", "JSON payload path, or - for stdin")
			_ = c.MarkFlagRequired("file")
		}
		if op == "get" || op == "history" {
			c.Flags().String("cursor", "", "next_cursor from the previous page")
		}
		root.AddCommand(c)
	}
	root.Example = `  multica attachment feedback configure <attachment-id> --file fields.json
  # fields.json: {"expected_revision":0,"fields":[{"id":"status","label":"Status","type":"select","required":false,"options":["pending","done"]}]}
  multica attachment feedback save <attachment-id> <stable-row-uuid> --file row.json
  # row.json: {"expected_revision":0,"schema_revision":1,"label":"Item A","source_attachment_id":"","values":{"status":"pending"}}
  multica attachment feedback get <attachment-id>
  multica attachment feedback history <attachment-id>
  # archive JSON: {"expected_revision":2}; historical values remain in the change log.`
	return root
}

func readReportPayload(reader io.Reader) (json.RawMessage, error) {
	body, err := io.ReadAll(io.LimitReader(reader, 512*1024+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 512*1024 || !json.Valid(body) {
		return nil, fmt.Errorf("payload must be one valid JSON object, at most 512 KiB")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return nil, fmt.Errorf("payload must be a JSON object")
	}
	return body, nil
}

func runReportFeedback(cmd *cobra.Command, args []string, op string) error {
	for _, id := range args {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("invalid attachment or row UUID: %s", id)
		}
	}
	path := "/api/attachments/" + url.PathEscape(args[0]) + "/report-feedback"
	var payload json.RawMessage
	if op == "configure" || op == "save" || op == "archive" {
		name, _ := cmd.Flags().GetString("file")
		if name == "" {
			return fmt.Errorf("--file is required")
		}
		reader := cmd.InOrStdin()
		if name != "-" {
			f, err := os.Open(name)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		var err error
		payload, err = readReportPayload(reader)
		if err != nil {
			return err
		}
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), cli.AtLeastAPITimeout(30*time.Second))
	defer cancel()
	var out json.RawMessage
	switch op {
	case "get", "history":
		if op == "history" {
			path += "/history"
		}
		cursor, _ := cmd.Flags().GetString("cursor")
		if cursor != "" {
			key := "after"
			if op == "history" {
				key = "before"
			}
			path += "?" + url.Values{key: []string{cursor}}.Encode()
		}
		err = client.GetJSON(ctx, path, &out)
	case "configure":
		err = client.PutJSON(ctx, path+"/schema", payload, &out)
	case "save":
		err = client.PutJSON(ctx, path+"/rows/"+url.PathEscape(args[1]), payload, &out)
	case "archive":
		err = client.PostJSON(ctx, path+"/rows/"+url.PathEscape(args[1])+"/archive", payload, &out)
	default:
		return fmt.Errorf("unknown feedback operation")
	}
	if err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}
