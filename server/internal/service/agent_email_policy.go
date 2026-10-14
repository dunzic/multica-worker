package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/net/idna"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	AgentEmailInterfaceVersion            = "notification-email/v1"
	agentEmailPolicySchema                = "agent-email-policy/v1"
	agentEmailRecipientPolicySchema       = "agent-email-recipient-policy/v1"
	agentEmailRatePolicySchema            = "agent-email-rate-policy/v1"
	agentEmailAgentPromptSchema           = "agent-email-agent-prompt/v1"
	agentEmailApprovalAuthoritySchema     = "agent-email-approval-authority/v1"
	maxAgentEmailRecipientsPerMessage     = 1000
	maxCanonicalAgentEmailAddressLength   = 254
	maxCanonicalAgentEmailLocalPartLength = 64
)

var (
	ErrInvalidAgentEmailPolicy            = errors.New("invalid Agent email policy")
	ErrAgentEmailApproverUnavailable      = errors.New("Agent email approver unavailable")
	ErrInvalidAgentEmailApprovalAuthority = errors.New("invalid Agent email approval authority")
)

// AgentEmailPluginRef identifies the immutable plugin bytes and entry that a
// task must have in its execution manifest.
type AgentEmailPluginRef struct {
	ReleaseID      uuid.UUID `json:"release_id"`
	ArtifactDigest string    `json:"artifact_digest"`
	EntryDigest    string    `json:"entry_digest"`
}

// AgentEmailProviderRouteRef identifies code-owned provider semantics. Secret
// credential values are intentionally absent so credential rotation does not
// invalidate an approval.
type AgentEmailProviderRouteRef struct {
	ID     uuid.UUID `json:"id"`
	Digest string    `json:"digest"`
}

// AgentEmailSenderRef identifies the deployment-managed sender selected by a
// policy. An Agent request may not supply either field.
type AgentEmailSenderRef struct {
	ID          uuid.UUID `json:"id"`
	FromAddress string    `json:"from_address"`
}

// AgentEmailRatePolicyV1 contains every quota enforced by the reservation SQL,
// plus the per-message recipient cap. All limits count recipients, not calls.
type AgentEmailRatePolicyV1 struct {
	Schema                  string `json:"schema"`
	ProviderPerMinute       int64  `json:"provider_per_minute"`
	ProviderPerDay          int64  `json:"provider_per_day"`
	WorkspacePerMinute      int64  `json:"workspace_per_minute"`
	WorkspacePerDay         int64  `json:"workspace_per_day"`
	AgentPerMinute          int64  `json:"agent_per_minute"`
	AgentPerDay             int64  `json:"agent_per_day"`
	SenderPerMinute         int64  `json:"sender_per_minute"`
	SenderPerDay            int64  `json:"sender_per_day"`
	MaxRecipientsPerMessage int64  `json:"max_recipients_per_message"`
}

// AgentEmailRecipientPolicyV1 is stored verbatim in recipient_policy JSONB.
type AgentEmailRecipientPolicyV1 struct {
	Schema    string   `json:"schema"`
	Allowlist []string `json:"allowlist"`
}

// AgentEmailPolicyInput is the typed, pre-canonical policy assembled from
// server-owned runtime, plugin and deployment descriptors.
type AgentEmailPolicyInput struct {
	AgentID            uuid.UUID
	AgentPromptDigest  string
	Plugin             AgentEmailPluginRef
	InterfaceVersion   string
	ProviderRoute      AgentEmailProviderRouteRef
	Sender             AgentEmailSenderRef
	RecipientAllowlist []string
	Limits             AgentEmailRatePolicyV1
}

// CanonicalAgentEmailPolicyV1 is the only byte representation hashed into a
// config digest. Keep fields typed and ordered; arbitrary JSONB must never be
// accepted as hash input.
type CanonicalAgentEmailPolicyV1 struct {
	Schema            string                      `json:"schema"`
	AgentID           uuid.UUID                   `json:"agent_id"`
	AgentPromptDigest string                      `json:"agent_prompt_digest"`
	Plugin            AgentEmailPluginRef         `json:"plugin"`
	InterfaceVersion  string                      `json:"interface_version"`
	ProviderRoute     AgentEmailProviderRouteRef  `json:"provider_route"`
	Sender            AgentEmailSenderRef         `json:"sender"`
	RecipientPolicy   AgentEmailRecipientPolicyV1 `json:"recipient_policy"`
	RatePolicy        AgentEmailRatePolicyV1      `json:"rate_policy"`
}

// CanonicalizeAgentEmailPolicy validates and normalizes every production
// authorization input, then returns the canonical bytes and their SHA-256
// digest. An empty recipient allowlist is a valid draft but cannot be approved.
func CanonicalizeAgentEmailPolicy(input AgentEmailPolicyInput) (CanonicalAgentEmailPolicyV1, []byte, string, error) {
	if input.AgentID == uuid.Nil || input.Plugin.ReleaseID == uuid.Nil ||
		input.ProviderRoute.ID == uuid.Nil || input.Sender.ID == uuid.Nil {
		return CanonicalAgentEmailPolicyV1{}, nil, "", fmt.Errorf("%w: required UUID is empty", ErrInvalidAgentEmailPolicy)
	}
	for name, digest := range map[string]string{
		"agent prompt":    input.AgentPromptDigest,
		"plugin artifact": input.Plugin.ArtifactDigest,
		"plugin entry":    input.Plugin.EntryDigest,
		"provider route":  input.ProviderRoute.Digest,
	} {
		if !isCanonicalSHA256Digest(digest) {
			return CanonicalAgentEmailPolicyV1{}, nil, "", fmt.Errorf("%w: %s digest is not canonical", ErrInvalidAgentEmailPolicy, name)
		}
	}
	if input.InterfaceVersion != AgentEmailInterfaceVersion {
		return CanonicalAgentEmailPolicyV1{}, nil, "", fmt.Errorf("%w: unsupported interface version", ErrInvalidAgentEmailPolicy)
	}
	fromAddress, err := NormalizeAgentEmailAddress(input.Sender.FromAddress)
	if err != nil {
		return CanonicalAgentEmailPolicyV1{}, nil, "", fmt.Errorf("%w: invalid sender address", ErrInvalidAgentEmailPolicy)
	}
	allowlist, err := normalizeAgentEmailAllowlist(input.RecipientAllowlist)
	if err != nil {
		return CanonicalAgentEmailPolicyV1{}, nil, "", fmt.Errorf("%w: invalid recipient allowlist", ErrInvalidAgentEmailPolicy)
	}
	limits := input.Limits
	limits.Schema = agentEmailRatePolicySchema
	if err := validateAgentEmailRatePolicy(limits); err != nil {
		return CanonicalAgentEmailPolicyV1{}, nil, "", err
	}

	policy := CanonicalAgentEmailPolicyV1{
		Schema:            agentEmailPolicySchema,
		AgentID:           input.AgentID,
		AgentPromptDigest: input.AgentPromptDigest,
		Plugin:            input.Plugin,
		InterfaceVersion:  input.InterfaceVersion,
		ProviderRoute:     input.ProviderRoute,
		Sender: AgentEmailSenderRef{
			ID:          input.Sender.ID,
			FromAddress: fromAddress,
		},
		RecipientPolicy: AgentEmailRecipientPolicyV1{
			Schema:    agentEmailRecipientPolicySchema,
			Allowlist: allowlist,
		},
		RatePolicy: limits,
	}
	canonical, err := json.Marshal(policy)
	if err != nil {
		return CanonicalAgentEmailPolicyV1{}, nil, "", fmt.Errorf("marshal canonical Agent email policy: %w", err)
	}
	return policy, canonical, agentEmailDigestBytes(canonical), nil
}

// AgentEmailPromptDigest hashes the exact identity and effective instructions
// sent to the runtime. Mika's product-owned prompt is composed here so a server
// binary update, rename or workspace-note change invalidates old approval.
func AgentEmailPromptDigest(agent db.Agent) (string, error) {
	if strings.TrimSpace(agent.Name) == "" {
		return "", fmt.Errorf("%w: Agent name is empty", ErrInvalidAgentEmailPolicy)
	}
	instructions := agent.Instructions
	if agent.SystemKey.Valid && agent.SystemKey.String == MikaSystemKey {
		instructions = ComposeMikaInstructions(agent.Name, agent.Instructions)
	}
	envelope := struct {
		Schema       string `json:"schema"`
		Name         string `json:"name"`
		Instructions string `json:"instructions"`
	}{
		Schema:       agentEmailAgentPromptSchema,
		Name:         agent.Name,
		Instructions: instructions,
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal Agent email prompt: %w", err)
	}
	return agentEmailDigestBytes(canonical), nil
}

// NormalizeAgentEmailAddress produces the sole address representation accepted
// by policy and authority digests. It accepts an ASCII dot-atom local part and
// an IDN domain, but rejects display names, comments and quoted local parts.
func NormalizeAgentEmailAddress(raw string) (string, error) {
	if strings.ContainsFunc(raw, unicode.IsControl) {
		return "", errors.New("email contains a control character")
	}
	value := strings.TrimSpace(raw)
	if value == "" || strings.Count(value, "@") != 1 {
		return "", errors.New("email must be a single addr-spec")
	}
	local, domain, _ := strings.Cut(value, "@")
	if !validAgentEmailLocalPart(local) {
		return "", errors.New("email local part is invalid")
	}
	asciiDomain, err := idna.Lookup.ToASCII(domain)
	if err != nil || !validAgentEmailDomain(asciiDomain) {
		return "", errors.New("email domain is invalid")
	}
	normalized := strings.ToLower(local) + "@" + strings.ToLower(asciiDomain)
	if len(normalized) > maxCanonicalAgentEmailAddressLength {
		return "", errors.New("email is too long")
	}
	parsed, err := mail.ParseAddress(normalized)
	if err != nil || parsed.Name != "" || parsed.Address != normalized {
		return "", errors.New("email is not a canonical addr-spec")
	}
	return normalized, nil
}

func validAgentEmailLocalPart(local string) bool {
	if local == "" || len(local) > maxCanonicalAgentEmailLocalPartLength ||
		strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return false
	}
	for _, r := range local {
		if r > unicode.MaxASCII || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-/=?^_`{|}~.", r)) {
			return false
		}
	}
	return true
}

func validAgentEmailDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func normalizeAgentEmailAllowlist(input []string) ([]string, error) {
	seen := make(map[string]struct{}, len(input))
	for _, raw := range input {
		normalized, err := NormalizeAgentEmailAddress(raw)
		if err != nil {
			return nil, err
		}
		seen[normalized] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for address := range seen {
		result = append(result, address)
	}
	slices.Sort(result)
	return result, nil
}

func validateAgentEmailRatePolicy(policy AgentEmailRatePolicyV1) error {
	values := []int64{
		policy.ProviderPerMinute, policy.ProviderPerDay,
		policy.WorkspacePerMinute, policy.WorkspacePerDay,
		policy.AgentPerMinute, policy.AgentPerDay,
		policy.SenderPerMinute, policy.SenderPerDay,
		policy.MaxRecipientsPerMessage,
	}
	for _, value := range values {
		if value <= 0 {
			return fmt.Errorf("%w: all rate limits must be positive", ErrInvalidAgentEmailPolicy)
		}
	}
	if policy.MaxRecipientsPerMessage > maxAgentEmailRecipientsPerMessage {
		return fmt.Errorf("%w: max recipients exceeds persistence limit", ErrInvalidAgentEmailPolicy)
	}
	return nil
}

func isCanonicalSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range strings.TrimPrefix(value, "sha256:") {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

type agentEmailApprovalAuthorityQueries interface {
	GetUserByEmail(context.Context, string) (db.User, error)
	GetMemberByUserAndWorkspace(context.Context, db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
}

// AgentEmailApprovalAuthority binds deployment configuration to one stable
// user identity. Digest changes invalidate decisions issued by an old account.
type AgentEmailApprovalAuthority struct {
	Email  string
	UserID pgtype.UUID
	Digest string
}

// AgentEmailApprovalAuthorityResolver resolves the deployment approver inside
// each workspace and requires its current membership to remain owner/admin.
type AgentEmailApprovalAuthorityResolver struct {
	Queries       agentEmailApprovalAuthorityQueries
	ApproverEmail string
}

func NewAgentEmailApprovalAuthorityResolver(queries *db.Queries, approverEmail string) AgentEmailApprovalAuthorityResolver {
	return AgentEmailApprovalAuthorityResolver{Queries: queries, ApproverEmail: approverEmail}
}

func (r AgentEmailApprovalAuthorityResolver) Resolve(ctx context.Context, workspaceID pgtype.UUID) (AgentEmailApprovalAuthority, error) {
	if r.Queries == nil || !workspaceID.Valid {
		return AgentEmailApprovalAuthority{}, ErrAgentEmailApproverUnavailable
	}
	email, err := NormalizeAgentEmailAddress(r.ApproverEmail)
	if err != nil {
		return AgentEmailApprovalAuthority{}, ErrAgentEmailApproverUnavailable
	}
	user, err := r.Queries.GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentEmailApprovalAuthority{}, ErrAgentEmailApproverUnavailable
	}
	if err != nil {
		return AgentEmailApprovalAuthority{}, fmt.Errorf("resolve Agent email approver user: %w", err)
	}
	if !user.ID.Valid {
		return AgentEmailApprovalAuthority{}, ErrAgentEmailApproverUnavailable
	}
	member, err := r.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      user.ID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentEmailApprovalAuthority{}, ErrAgentEmailApproverUnavailable
	}
	if err != nil {
		return AgentEmailApprovalAuthority{}, fmt.Errorf("resolve Agent email approver membership: %w", err)
	}
	if member.Role != "owner" && member.Role != "admin" {
		return AgentEmailApprovalAuthority{}, ErrAgentEmailApproverUnavailable
	}
	digest, err := agentEmailApprovalAuthorityDigest(email, user.ID)
	if err != nil {
		return AgentEmailApprovalAuthority{}, err
	}
	return AgentEmailApprovalAuthority{Email: email, UserID: user.ID, Digest: digest}, nil
}

func agentEmailApprovalAuthorityDigest(email string, userID pgtype.UUID) (string, error) {
	if !userID.Valid {
		return "", ErrInvalidAgentEmailApprovalAuthority
	}
	normalized, err := NormalizeAgentEmailAddress(email)
	if err != nil {
		return "", ErrInvalidAgentEmailApprovalAuthority
	}
	envelope := struct {
		Schema string    `json:"schema"`
		Email  string    `json:"email"`
		UserID uuid.UUID `json:"user_id"`
	}{
		Schema: agentEmailApprovalAuthoritySchema,
		Email:  normalized,
		UserID: uuid.UUID(userID.Bytes),
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal Agent email approval authority: %w", err)
	}
	return agentEmailDigestBytes(canonical), nil
}

func agentEmailDigestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
