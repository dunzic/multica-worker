package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestNormalizeAgentEmailAddress(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "lowercase and trim", input: "  Alerts+Daily@Example.COM ", want: "alerts+daily@example.com"},
		{name: "IDN domain", input: "Owner@BÜCHER.example", want: "owner@xn--bcher-kva.example"},
		{name: "display name", input: "Owner <owner@example.com>", wantErr: true},
		{name: "comment", input: "owner(comment)@example.com", wantErr: true},
		{name: "quoted local", input: `"owner"@example.com`, wantErr: true},
		{name: "unicode local", input: "用户@example.com", wantErr: true},
		{name: "double dot", input: "owner..mail@example.com", wantErr: true},
		{name: "leading domain hyphen", input: "owner@-example.com", wantErr: true},
		{name: "line break", input: "owner@example.com\r\nBcc:a@example.com", wantErr: true},
		{name: "trailing line break", input: "owner@example.com\r\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAgentEmailAddress(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("address = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalizeAgentEmailPolicy_NormalizesDeterministically(t *testing.T) {
	firstInput := validAgentEmailPolicyInput()
	firstInput.Sender.FromAddress = " NOTIFY@Example.COM "
	firstInput.RecipientAllowlist = []string{
		"Second@Example.com",
		"owner@BÜCHER.example",
		"second@example.com",
	}
	first, firstJSON, firstDigest, err := CanonicalizeAgentEmailPolicy(firstInput)
	if err != nil {
		t.Fatalf("canonicalize first policy: %v", err)
	}

	secondInput := validAgentEmailPolicyInput()
	secondInput.RecipientAllowlist = []string{"OWNER@xn--bcher-kva.example", "second@example.com"}
	second, secondJSON, secondDigest, err := CanonicalizeAgentEmailPolicy(secondInput)
	if err != nil {
		t.Fatalf("canonicalize second policy: %v", err)
	}

	if string(firstJSON) != string(secondJSON) || firstDigest != secondDigest {
		t.Fatalf("equivalent inputs differ:\n%s\n%s\n%s\n%s", firstJSON, secondJSON, firstDigest, secondDigest)
	}
	if first.Sender.FromAddress != "notify@example.com" || second.Sender.FromAddress != "notify@example.com" {
		t.Fatalf("sender normalization failed: %q / %q", first.Sender.FromAddress, second.Sender.FromAddress)
	}
	wantAllowlist := []string{"owner@xn--bcher-kva.example", "second@example.com"}
	if len(first.RecipientPolicy.Allowlist) != len(wantAllowlist) {
		t.Fatalf("allowlist = %#v", first.RecipientPolicy.Allowlist)
	}
	for i := range wantAllowlist {
		if first.RecipientPolicy.Allowlist[i] != wantAllowlist[i] {
			t.Fatalf("allowlist = %#v, want %#v", first.RecipientPolicy.Allowlist, wantAllowlist)
		}
	}
	if !strings.HasPrefix(firstDigest, "sha256:") || len(firstDigest) != 71 {
		t.Fatalf("digest = %q", firstDigest)
	}
}

func TestCanonicalizeAgentEmailPolicy_EveryAuthorizationFieldChangesDigest(t *testing.T) {
	base := validAgentEmailPolicyInput()
	_, _, baseDigest, err := CanonicalizeAgentEmailPolicy(base)
	if err != nil {
		t.Fatalf("canonicalize base policy: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*AgentEmailPolicyInput)
	}{
		{name: "agent id", mutate: func(v *AgentEmailPolicyInput) { v.AgentID = uuid.MustParse("10000000-0000-0000-0000-000000000099") }},
		{name: "agent prompt", mutate: func(v *AgentEmailPolicyInput) { v.AgentPromptDigest = testAgentEmailDigest("1") }},
		{name: "plugin release", mutate: func(v *AgentEmailPolicyInput) {
			v.Plugin.ReleaseID = uuid.MustParse("20000000-0000-0000-0000-000000000099")
		}},
		{name: "plugin artifact", mutate: func(v *AgentEmailPolicyInput) { v.Plugin.ArtifactDigest = testAgentEmailDigest("2") }},
		{name: "plugin entry", mutate: func(v *AgentEmailPolicyInput) { v.Plugin.EntryDigest = testAgentEmailDigest("3") }},
		{name: "provider route id", mutate: func(v *AgentEmailPolicyInput) {
			v.ProviderRoute.ID = uuid.MustParse("30000000-0000-0000-0000-000000000099")
		}},
		{name: "provider route digest", mutate: func(v *AgentEmailPolicyInput) { v.ProviderRoute.Digest = testAgentEmailDigest("4") }},
		{name: "sender id", mutate: func(v *AgentEmailPolicyInput) { v.Sender.ID = uuid.MustParse("40000000-0000-0000-0000-000000000099") }},
		{name: "from address", mutate: func(v *AgentEmailPolicyInput) { v.Sender.FromAddress = "other@example.com" }},
		{name: "recipient allowlist", mutate: func(v *AgentEmailPolicyInput) { v.RecipientAllowlist = []string{"other@example.com"} }},
		{name: "provider minute", mutate: func(v *AgentEmailPolicyInput) { v.Limits.ProviderPerMinute++ }},
		{name: "provider day", mutate: func(v *AgentEmailPolicyInput) { v.Limits.ProviderPerDay++ }},
		{name: "workspace minute", mutate: func(v *AgentEmailPolicyInput) { v.Limits.WorkspacePerMinute++ }},
		{name: "workspace day", mutate: func(v *AgentEmailPolicyInput) { v.Limits.WorkspacePerDay++ }},
		{name: "agent minute", mutate: func(v *AgentEmailPolicyInput) { v.Limits.AgentPerMinute++ }},
		{name: "agent day", mutate: func(v *AgentEmailPolicyInput) { v.Limits.AgentPerDay++ }},
		{name: "sender minute", mutate: func(v *AgentEmailPolicyInput) { v.Limits.SenderPerMinute++ }},
		{name: "sender day", mutate: func(v *AgentEmailPolicyInput) { v.Limits.SenderPerDay++ }},
		{name: "message recipient cap", mutate: func(v *AgentEmailPolicyInput) { v.Limits.MaxRecipientsPerMessage++ }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := base
			input.RecipientAllowlist = append([]string(nil), base.RecipientAllowlist...)
			tt.mutate(&input)
			_, _, digest, err := CanonicalizeAgentEmailPolicy(input)
			if err != nil {
				t.Fatalf("canonicalize mutated policy: %v", err)
			}
			if digest == baseDigest {
				t.Fatalf("digest did not change from %s", baseDigest)
			}
		})
	}
}

func TestCanonicalizeAgentEmailPolicy_RejectsIncompleteDeploymentInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AgentEmailPolicyInput)
	}{
		{name: "empty route", mutate: func(v *AgentEmailPolicyInput) { v.ProviderRoute.ID = uuid.Nil }},
		{name: "uppercase digest", mutate: func(v *AgentEmailPolicyInput) { v.Plugin.EntryDigest = "sha256:" + strings.Repeat("A", 64) }},
		{name: "wrong interface", mutate: func(v *AgentEmailPolicyInput) { v.InterfaceVersion = "notification-email/v2" }},
		{name: "empty sender", mutate: func(v *AgentEmailPolicyInput) { v.Sender.FromAddress = "" }},
		{name: "zero quota", mutate: func(v *AgentEmailPolicyInput) { v.Limits.WorkspacePerMinute = 0 }},
		{name: "recipient cap too large", mutate: func(v *AgentEmailPolicyInput) { v.Limits.MaxRecipientsPerMessage = 1001 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validAgentEmailPolicyInput()
			tt.mutate(&input)
			_, _, _, err := CanonicalizeAgentEmailPolicy(input)
			if !errors.Is(err, ErrInvalidAgentEmailPolicy) {
				t.Fatalf("error = %v, want ErrInvalidAgentEmailPolicy", err)
			}
		})
	}
}

func TestAgentEmailPromptDigest_BindsEffectiveRuntimePrompt(t *testing.T) {
	ordinary := db.Agent{Name: "Writer", Instructions: "Exact\nbytes"}
	base, err := AgentEmailPromptDigest(ordinary)
	if err != nil {
		t.Fatalf("ordinary digest: %v", err)
	}
	changedName, _ := AgentEmailPromptDigest(db.Agent{Name: "Renamed", Instructions: ordinary.Instructions})
	changedInstructions, _ := AgentEmailPromptDigest(db.Agent{Name: ordinary.Name, Instructions: "Exact\nbytes\n"})
	if base == changedName || base == changedInstructions {
		t.Fatal("ordinary Agent identity or instruction change did not change digest")
	}

	mika := db.Agent{
		Name:         "Mika",
		Instructions: "Workspace note",
		SystemKey:    pgtype.Text{String: MikaSystemKey, Valid: true},
	}
	mikaDigest, err := AgentEmailPromptDigest(mika)
	if err != nil {
		t.Fatalf("Mika digest: %v", err)
	}
	plainDigest, _ := AgentEmailPromptDigest(db.Agent{Name: mika.Name, Instructions: mika.Instructions})
	mika.Instructions = "Changed workspace note"
	changedMikaDigest, _ := AgentEmailPromptDigest(mika)
	if mikaDigest == plainDigest || mikaDigest == changedMikaDigest {
		t.Fatal("Mika product prompt composition or workspace notes were not bound")
	}
}

type fakeAgentEmailAuthorityQueries struct {
	user      db.User
	userErr   error
	member    db.Member
	memberErr error
	gotEmail  string
	gotMember db.GetMemberByUserAndWorkspaceParams
}

func (f *fakeAgentEmailAuthorityQueries) GetUserByEmail(_ context.Context, email string) (db.User, error) {
	f.gotEmail = email
	return f.user, f.userErr
}

func (f *fakeAgentEmailAuthorityQueries) GetMemberByUserAndWorkspace(_ context.Context, arg db.GetMemberByUserAndWorkspaceParams) (db.Member, error) {
	f.gotMember = arg
	return f.member, f.memberErr
}

func TestAgentEmailApprovalAuthorityResolver(t *testing.T) {
	userID := testAgentEmailPGUUID("50000000-0000-0000-0000-000000000001")
	workspaceID := testAgentEmailPGUUID("60000000-0000-0000-0000-000000000001")
	queries := &fakeAgentEmailAuthorityQueries{
		user:   db.User{ID: userID, Email: "approver@example.com"},
		member: db.Member{UserID: userID, WorkspaceID: workspaceID, Role: "admin"},
	}
	resolver := AgentEmailApprovalAuthorityResolver{
		Queries:       queries,
		ApproverEmail: " Approver@EXAMPLE.com ",
	}
	authority, err := resolver.Resolve(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("resolve authority: %v", err)
	}
	if queries.gotEmail != "approver@example.com" {
		t.Fatalf("email lookup = %q", queries.gotEmail)
	}
	if queries.gotMember.UserID.Bytes != userID.Bytes || queries.gotMember.WorkspaceID.Bytes != workspaceID.Bytes {
		t.Fatalf("membership lookup = %#v", queries.gotMember)
	}
	if authority.Email != "approver@example.com" || authority.UserID.Bytes != userID.Bytes || !isCanonicalSHA256Digest(authority.Digest) {
		t.Fatalf("authority = %#v", authority)
	}

	secondDigest, err := agentEmailApprovalAuthorityDigest("APPROVER@example.com", userID)
	if err != nil || secondDigest != authority.Digest {
		t.Fatalf("normalized digest = %q, err = %v", secondDigest, err)
	}
	otherUserDigest, err := agentEmailApprovalAuthorityDigest("approver@example.com", testAgentEmailPGUUID("50000000-0000-0000-0000-000000000002"))
	if err != nil || otherUserDigest == authority.Digest {
		t.Fatalf("stable user identity was not bound: %q, err = %v", otherUserDigest, err)
	}
}

func TestAgentEmailApprovalAuthorityResolver_FailsClosed(t *testing.T) {
	userID := testAgentEmailPGUUID("50000000-0000-0000-0000-000000000001")
	workspaceID := testAgentEmailPGUUID("60000000-0000-0000-0000-000000000001")
	tests := []struct {
		name    string
		email   string
		queries *fakeAgentEmailAuthorityQueries
	}{
		{name: "empty deployment config", queries: &fakeAgentEmailAuthorityQueries{}},
		{name: "invalid deployment config", email: "Approver <approver@example.com>", queries: &fakeAgentEmailAuthorityQueries{}},
		{name: "user missing", email: "approver@example.com", queries: &fakeAgentEmailAuthorityQueries{userErr: pgx.ErrNoRows}},
		{name: "membership missing", email: "approver@example.com", queries: &fakeAgentEmailAuthorityQueries{user: db.User{ID: userID}, memberErr: pgx.ErrNoRows}},
		{name: "role downgraded", email: "approver@example.com", queries: &fakeAgentEmailAuthorityQueries{user: db.User{ID: userID}, member: db.Member{Role: "member"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := AgentEmailApprovalAuthorityResolver{Queries: tt.queries, ApproverEmail: tt.email}
			_, err := resolver.Resolve(context.Background(), workspaceID)
			if !errors.Is(err, ErrAgentEmailApproverUnavailable) {
				t.Fatalf("error = %v, want ErrAgentEmailApproverUnavailable", err)
			}
		})
	}
}

func validAgentEmailPolicyInput() AgentEmailPolicyInput {
	return AgentEmailPolicyInput{
		AgentID:           uuid.MustParse("10000000-0000-0000-0000-000000000001"),
		AgentPromptDigest: testAgentEmailDigest("a"),
		Plugin: AgentEmailPluginRef{
			ReleaseID:      uuid.MustParse("20000000-0000-0000-0000-000000000001"),
			ArtifactDigest: testAgentEmailDigest("b"),
			EntryDigest:    testAgentEmailDigest("c"),
		},
		InterfaceVersion: AgentEmailInterfaceVersion,
		ProviderRoute: AgentEmailProviderRouteRef{
			ID:     uuid.MustParse("30000000-0000-0000-0000-000000000001"),
			Digest: testAgentEmailDigest("d"),
		},
		Sender: AgentEmailSenderRef{
			ID:          uuid.MustParse("40000000-0000-0000-0000-000000000001"),
			FromAddress: "notify@example.com",
		},
		RecipientAllowlist: []string{"owner@xn--bcher-kva.example", "second@example.com"},
		Limits: AgentEmailRatePolicyV1{
			ProviderPerMinute:       100,
			ProviderPerDay:          1000,
			WorkspacePerMinute:      80,
			WorkspacePerDay:         800,
			AgentPerMinute:          10,
			AgentPerDay:             200,
			SenderPerMinute:         90,
			SenderPerDay:            900,
			MaxRecipientsPerMessage: 20,
		},
	}
}

func testAgentEmailDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func testAgentEmailPGUUID(value string) pgtype.UUID {
	id := uuid.MustParse(value)
	return pgtype.UUID{Bytes: id, Valid: true}
}
