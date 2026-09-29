package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var agentEmailCmd = &cobra.Command{
	Use:   "email",
	Short: "Configure and approve an agent's production email policy",
}

var agentEmailConfigureCmd = &cobra.Command{
	Use:   "configure <agent-id>",
	Short: "Create a new version of an agent's email policy",
	Args:  exactArgs(1),
	RunE:  runAgentEmailConfigure,
}

var agentEmailRequestApprovalCmd = &cobra.Command{
	Use:   "request-approval <agent-id>",
	Short: "Request production approval for the current email policy",
	Args:  exactArgs(1),
	RunE:  runAgentEmailRequestApproval,
}

var agentEmailApprovalStatusCmd = &cobra.Command{
	Use:   "approval-status <agent-id>",
	Short: "Show the current email policy and approval status",
	Args:  exactArgs(1),
	RunE:  runAgentEmailApprovalStatus,
}

var agentEmailApproveCmd = &cobra.Command{
	Use:   "approve <agent-id> <approval-id>",
	Short: "Approve an agent email policy version for production",
	Args:  exactArgs(2),
	RunE:  runAgentEmailApprove,
}

var agentEmailRejectCmd = &cobra.Command{
	Use:   "reject <agent-id> <approval-id>",
	Short: "Reject an agent email approval request",
	Args:  exactArgs(2),
	RunE:  runAgentEmailReject,
}

var agentEmailRevokeCmd = &cobra.Command{
	Use:   "revoke <agent-id> <approval-id>",
	Short: "Revoke an agent email approval",
	Args:  exactArgs(2),
	RunE:  runAgentEmailRevoke,
}

type agentEmailLimitsRequest struct {
	PerMinute               int `json:"per_minute"`
	PerDay                  int `json:"per_day"`
	MaxRecipientsPerMessage int `json:"max_recipients_per_message,omitempty"`
}

type agentEmailPolicyRequest struct {
	FromAddress        string                  `json:"from_address"`
	RecipientAllowlist []string                `json:"recipient_allowlist"`
	Limits             agentEmailLimitsRequest `json:"limits"`
}

type agentEmailDecisionRequest struct {
	ReasonCode string `json:"reason_code"`
}

func init() {
	agentCmd.AddCommand(agentEmailCmd)
	agentEmailCmd.AddCommand(
		agentEmailConfigureCmd,
		agentEmailRequestApprovalCmd,
		agentEmailApprovalStatusCmd,
		agentEmailApproveCmd,
		agentEmailRejectCmd,
		agentEmailRevokeCmd,
	)
	agentEmailConfigureCmd.Flags().String("from", "", "Managed sender address (required)")
	agentEmailConfigureCmd.Flags().StringArray("allow-to", nil, "Approved recipient address; repeat for each address (an empty allowlist cannot be approved)")
	agentEmailConfigureCmd.Flags().Int("per-minute", 0, "Maximum recipients per minute (required)")
	agentEmailConfigureCmd.Flags().Int("per-day", 0, "Maximum recipients per day (required)")
	agentEmailConfigureCmd.Flags().Int("max-recipients-per-message", 0, "Maximum recipients in one message (default: deployment policy)")
	agentEmailRejectCmd.Flags().String("reason", "", "Machine-readable rejection reason code (required)")
	agentEmailRevokeCmd.Flags().String("reason", "", "Machine-readable revocation reason code (required)")
}

func runAgentEmailConfigure(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("agent email configure"); err != nil {
		return err
	}
	from, _ := cmd.Flags().GetString("from")
	from = strings.TrimSpace(from)
	if from == "" {
		return fmt.Errorf("--from is required")
	}
	allowTo, _ := cmd.Flags().GetStringArray("allow-to")
	// Preserve an explicitly empty allowlist as [] rather than null. An empty
	// draft is valid, but the server must refuse to approve it for production.
	allowTo = append([]string{}, allowTo...)
	for i := range allowTo {
		allowTo[i] = strings.TrimSpace(allowTo[i])
		if allowTo[i] == "" {
			return fmt.Errorf("--allow-to cannot be empty")
		}
	}
	perMinute, _ := cmd.Flags().GetInt("per-minute")
	perDay, _ := cmd.Flags().GetInt("per-day")
	maxRecipients, _ := cmd.Flags().GetInt("max-recipients-per-message")
	if perMinute <= 0 || perDay <= 0 {
		return fmt.Errorf("--per-minute and --per-day must be positive")
	}
	if cmd.Flags().Changed("max-recipients-per-message") && maxRecipients <= 0 {
		return fmt.Errorf("--max-recipients-per-message must be positive when specified")
	}
	body := agentEmailPolicyRequest{
		FromAddress:        from,
		RecipientAllowlist: allowTo,
		Limits: agentEmailLimitsRequest{
			PerMinute: perMinute, PerDay: perDay, MaxRecipientsPerMessage: maxRecipients,
		},
	}
	return postAgentEmailControl(cmd, args[0], "/email-policy", body, "configure agent email policy")
}

func runAgentEmailRequestApproval(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("agent email request-approval"); err != nil {
		return err
	}
	return postAgentEmailControl(cmd, args[0], "/email-approval-requests", struct{}{}, "request agent email approval")
}

func runAgentEmailApprovalStatus(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("agent email approval-status"); err != nil {
		return err
	}
	client, basePath, err := agentEmailControlClient(cmd, args[0])
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.GetJSON(ctx, basePath+"/email-policy/status", &result); err != nil {
		return fmt.Errorf("get agent email approval status: %w", err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), result)
}

func runAgentEmailApprove(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("agent email approve"); err != nil {
		return err
	}
	return postAgentEmailDecision(cmd, args[0], args[1], "approve", struct{}{})
}

func runAgentEmailReject(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("agent email reject"); err != nil {
		return err
	}
	reason, err := requiredReason(cmd)
	if err != nil {
		return err
	}
	return postAgentEmailDecision(cmd, args[0], args[1], "reject", agentEmailDecisionRequest{ReasonCode: reason})
}

func runAgentEmailRevoke(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("agent email revoke"); err != nil {
		return err
	}
	reason, err := requiredReason(cmd)
	if err != nil {
		return err
	}
	return postAgentEmailDecision(cmd, args[0], args[1], "revoke", agentEmailDecisionRequest{ReasonCode: reason})
}

func requiredReason(cmd *cobra.Command) (string, error) {
	reason, _ := cmd.Flags().GetString("reason")
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("--reason is required")
	}
	return reason, nil
}

func postAgentEmailDecision(cmd *cobra.Command, agentID, approvalID, action string, body any) error {
	client, basePath, err := agentEmailControlClient(cmd, agentID)
	if err != nil {
		return err
	}
	path := basePath + "/email-approvals/" + url.PathEscape(strings.TrimSpace(approvalID)) + "/" + action
	return postEmailControl(cmd, client, path, body, action+" agent email approval")
}

func postAgentEmailControl(cmd *cobra.Command, agentID, suffix string, body any, action string) error {
	client, basePath, err := agentEmailControlClient(cmd, agentID)
	if err != nil {
		return err
	}
	return postEmailControl(cmd, client, basePath+suffix, body, action)
}

func postEmailControl(cmd *cobra.Command, client *cli.APIClient, path string, body any, action string) error {
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, path, body, &result); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return cli.PrintJSON(cmd.OutOrStdout(), result)
}

func agentEmailControlClient(cmd *cobra.Command, agentID string) (*cli.APIClient, string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, "", fmt.Errorf("agent ID is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(client.WorkspaceID) == "" {
		return nil, "", fmt.Errorf("workspace ID is required for agent email governance")
	}
	basePath := "/api/workspaces/" + url.PathEscape(client.WorkspaceID) + "/agents/" + url.PathEscape(agentID)
	return client, basePath, nil
}
