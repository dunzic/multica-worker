package rolesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestRoleSourceSecretTransferLifecyclePostgres is an opt-in production gate.
// It exercises the whole one-time secret path against a fully migrated real
// PostgreSQL database, including a transaction failure after consumption. The
// test deliberately uses values that can be searched for in every durable
// control-plane representation to prove that only the materialized Agent owns
// plaintext after a successful apply.
func TestRoleSourceSecretTransferLifecyclePostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_SECRET_TRANSFER_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_SECRET_TRANSFER_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	const (
		roleID      = "secret-live-role"
		environment = "API_TOKEN"
		secretValue = "rs-live-plaintext-4d57a4b9"
		mcpID       = "platform"
		mcpSecret   = "rs-live-mcp-plaintext-c42d10f8"
		secretKeyID = "live_v1"
	)
	mcpDefinition := json.RawMessage(fmt.Sprintf(`{"command":"live-mcp","env":{"API_TOKEN":"${API_TOKEN}","MCP_SECRET":%q}}`, mcpSecret))
	mcpDigest := canonicalJSONDigest(t, mcpDefinition)
	body := []byte("# Live secret transfer role\n\nMaterialize one encrypted transfer.\n")
	bodySum := sha256.Sum256(body)
	instructions := ArtifactRef{
		Digest: "sha256:" + hex.EncodeToString(bodySum[:]), Path: "roles/secret-live/instructions.md",
		MediaType: "text/markdown", SizeBytes: int64(len(body)),
	}
	manifest := Manifest{ContractVersion: ContractVersion, Roles: []Role{{
		ID: roleID, DisplayName: "Secret live " + uuid.NewString(), Instructions: instructions,
		Skills: []Skill{}, CapabilityBindings: []CapabilityBinding{},
		Environment: []EnvironmentKey{{
			Name: environment, Required: true, Secret: true, Configured: true,
			ValueDigest: "hmac-sha256:" + strings.Repeat("a", 64),
		}},
		MCP:         []MCPServer{{ID: mcpID, DefinitionHash: mcpDigest, Environment: []string{environment}}},
		Automations: []Automation{},
	}}, Capabilities: []Capability{}}
	fixture := createApplyFailureFixtureForManifest(t, ctx, pool, manifest, map[string][]byte{instructions.Digest: body})
	defer cleanupApplyFailureFixture(t, pool, fixture)
	defer cleanupSecretTransferMaterializedAgent(t, pool, fixture.workspaceID)

	control := newSecretTransferLiveControl(t, pool, fixture.artifactReader, secretKeyID)
	request := RequestSecretTransferInput{
		WorkspaceID: fixture.workspaceID.String(), SourceID: fixture.sourceID.String(),
		PlanDigest: fixture.input.PlanDigest, ApprovalID: fixture.input.ApprovalID,
		RoleID: roleID, RequestKey: "secret-live-" + uuid.NewString(), ActorUserID: fixture.actorID.String(),
	}
	transfer, err := control.RequestSecretTransfer(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if transfer.Status != "pending" || transfer.PublicKey == "" || len(transfer.PrivateKeyCiphertext) < 60 || len(transfer.Envelope) != 0 {
		t.Fatalf("pending transfer is incomplete: %+v", transfer)
	}
	idempotentRequest, err := control.RequestSecretTransfer(ctx, request)
	if err != nil || idempotentRequest.ID != transfer.ID {
		t.Fatalf("idempotent request id=%v want=%v err=%v", idempotentRequest.ID, transfer.ID, err)
	}

	claimed, err := control.ClaimNextSecretTransfer(ctx, fixture.runtimeID.String(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Transfer.ID != transfer.ID || claimed.Transfer.Status != "claimed" || !claimed.Transfer.LeaseToken.Valid {
		t.Fatalf("claimed transfer=%+v want id=%v", claimed.Transfer, transfer.ID)
	}
	if _, err := control.ClaimNextSecretTransfer(ctx, fixture.runtimeID.String(), 30*time.Second); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim should find no work, got %v", err)
	}

	payload := SecretEnvelopePayload{
		Environment: map[string]string{environment: secretValue},
		MCPServers:  map[string]json.RawMessage{mcpID: append(json.RawMessage(nil), mcpDefinition...)},
	}
	envelope, err := SealSecretEnvelope(claimed.Transfer.PublicKey, claimed.Claims, payload)
	if err != nil {
		t.Fatal(err)
	}
	ClearSecretEnvelopePayload(&payload)
	wrongLease := ReportSecretTransferInput{
		WorkspaceID: fixture.workspaceID.String(), SourceID: fixture.sourceID.String(), TransferID: transferID(transfer),
		RuntimeID: fixture.runtimeID.String(), LeaseToken: uuid.NewString(), Status: "completed", Envelope: &envelope,
	}
	if _, err := control.ReportSecretTransfer(ctx, wrongLease); !errors.Is(err, ErrSecretTransferLeaseLost) {
		t.Fatalf("stale lease report error=%v", err)
	}
	report := wrongLease
	report.LeaseToken = transferIDFromPG(claimed.Transfer.LeaseToken)
	submitted, err := control.ReportSecretTransfer(ctx, report)
	if err != nil || submitted.Status != "submitted" || !submitted.EnvelopeDigest.Valid {
		t.Fatalf("submitted transfer=%+v err=%v", submitted, err)
	}
	idempotentReport, err := control.ReportSecretTransfer(ctx, report)
	if err != nil || idempotentReport.ID != submitted.ID || idempotentReport.EnvelopeDigest != submitted.EnvelopeDigest {
		t.Fatalf("idempotent report=%+v err=%v", idempotentReport, err)
	}
	assertSubmittedTransferCiphertextOnly(t, ctx, pool, fixture.sourceID, transfer.ID, secretValue, mcpSecret)

	// Create another challenge before the plan advances. It remains pending and
	// proves the production expiry query clears all recoverable key material.
	expiryRequest := request
	expiryRequest.RequestKey = "secret-expiry-" + uuid.NewString()
	expiring, err := control.RequestSecretTransfer(ctx, expiryRequest)
	if err != nil {
		t.Fatal(err)
	}

	fixture.input.SecretTransferIDs = map[string]string{roleID: transferID(transfer)}
	control.applyFailurePoint = func(point string) error {
		if point == applyFaultSecretsConsumed {
			return errInjectedApplyFailure
		}
		return nil
	}
	if _, _, err := control.ApplyPlan(ctx, fixture.input); !errors.Is(err, errInjectedApplyFailure) {
		t.Fatalf("faulted apply error=%v", err)
	}
	assertSecretApplyRolledBack(t, ctx, pool, fixture, transfer.ID, submitted.EnvelopeDigest.String)

	control.applyFailurePoint = nil
	applyRow, receipt, err := control.ApplyPlan(ctx, fixture.input)
	if err != nil || applyRow.Status != "succeeded" {
		t.Fatalf("apply row=%+v receipt=%+v err=%v", applyRow, receipt, err)
	}
	if len(receipt.SecretTransfers) != 1 || receipt.SecretTransfers[0].RoleID != roleID ||
		receipt.SecretTransfers[0].TransferID != transferID(transfer) ||
		receipt.SecretTransfers[0].EnvelopeDigest != submitted.EnvelopeDigest.String {
		t.Fatalf("secret receipt=%+v", receipt.SecretTransfers)
	}
	receiptBody, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext(t, "apply receipt", receiptBody, secretValue, mcpSecret)

	retryRow, retryReceipt, err := control.ApplyPlan(ctx, fixture.input)
	if err != nil || retryRow.ID != applyRow.ID || retryReceipt.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("idempotent apply row=%+v receipt=%+v err=%v", retryRow, retryReceipt, err)
	}
	assertConsumedTransferScrubbed(t, ctx, pool, fixture.sourceID, transfer.ID)
	assertMaterializedAgentSecrets(t, ctx, pool, fixture, roleID, environment, secretValue, mcpID, mcpDefinition)
	assertSecretAuditAndControlPlaneRedaction(t, ctx, pool, fixture.sourceID, transfer.ID, secretValue, mcpSecret)

	if _, err := pool.Exec(ctx, `UPDATE role_source_secret_transfer SET expires_at=now()-interval '1 second' WHERE id=$1 AND source_id=$2`, expiring.ID, fixture.sourceID); err != nil {
		t.Fatal(err)
	}
	expired, err := db.New(pool).ExpireRoleSourceSecretTransfers(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0] != expiring.ID {
		t.Fatalf("expired ids=%v want=%v", expired, expiring.ID)
	}
	assertExpiredTransferScrubbed(t, ctx, pool, fixture.sourceID, expiring.ID)
}

func newSecretTransferLiveControl(t *testing.T, database controlPlaneDB, reader ArtifactReader, keyID string) *ControlPlane {
	t.Helper()
	catalog, err := NewCatalog(Descriptor{
		Kind: "fake_source", DisplayName: "Live secret source", AdapterVersion: "1.0.0", ContractVersion: ContractVersion,
		Capabilities: AdapterCapabilities{SecretTransfer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControlPlane(database, catalog)
	if err != nil {
		t.Fatal(err)
	}
	control.SetArtifactReader(reader)
	box, err := secretbox.New(bytes.Repeat([]byte{0x5a}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	if err := control.SetSecretBox(box, keyID); err != nil {
		t.Fatal(err)
	}
	return control
}

func canonicalJSONDigest(t *testing.T, body []byte) string {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func transferID(row db.RoleSourceSecretTransfer) string {
	return transferIDFromPG(row.ID)
}

func transferIDFromPG(id pgtype.UUID) string {
	return util.UUIDToString(id)
}

func assertSubmittedTransferCiphertextOnly(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID uuid.UUID, transferIDValue interface{}, forbidden ...string) {
	t.Helper()
	var durable []byte
	if err := pool.QueryRow(ctx, `SELECT convert_to(claims::text || coalesce(envelope::text,'') || encode(private_key_ciphertext,'hex'),'UTF8') FROM role_source_secret_transfer WHERE source_id=$1 AND id=$2`, sourceID, transferIDValue).Scan(&durable); err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext(t, "submitted transfer", durable, forbidden...)
}

func assertSecretApplyRolledBack(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture, id interface{}, envelopeDigest string) {
	t.Helper()
	var status, digest, current string
	var envelope, private []byte
	if err := pool.QueryRow(ctx, `SELECT status,envelope_digest,envelope,private_key_ciphertext FROM role_source_secret_transfer WHERE source_id=$1 AND id=$2`, fixture.sourceID, id).Scan(&status, &digest, &envelope, &private); err != nil {
		t.Fatal(err)
	}
	if status != "submitted" || digest != envelopeDigest || len(envelope) == 0 || bytes.Equal(private, make([]byte, len(private))) {
		t.Fatalf("secret consume escaped rollback status=%s digest=%s envelope=%d private_zero=%v", status, digest, len(envelope), bytes.Equal(private, make([]byte, len(private))))
	}
	if err := pool.QueryRow(ctx, `SELECT current_snapshot_digest FROM role_source WHERE id=$1`, fixture.sourceID).Scan(&current); err != nil || current != fixture.fromDigest {
		t.Fatalf("faulted apply advanced snapshot=%s err=%v", current, err)
	}
	var agents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE workspace_id=$1`, fixture.workspaceID).Scan(&agents); err != nil || agents != 0 {
		t.Fatalf("faulted apply committed agents=%d err=%v", agents, err)
	}
}

func assertConsumedTransferScrubbed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID uuid.UUID, id interface{}) {
	t.Helper()
	var status string
	var envelope []byte
	var private []byte
	var leaseValid bool
	if err := pool.QueryRow(ctx, `SELECT status,envelope,private_key_ciphertext,lease_token IS NOT NULL FROM role_source_secret_transfer WHERE source_id=$1 AND id=$2`, sourceID, id).Scan(&status, &envelope, &private, &leaseValid); err != nil {
		t.Fatal(err)
	}
	if status != "consumed" || len(envelope) != 0 || len(private) != 60 || !bytes.Equal(private, make([]byte, 60)) || leaseValid {
		t.Fatalf("consumed transfer not scrubbed status=%s envelope=%d private=%x lease=%v", status, len(envelope), private, leaseValid)
	}
}

func assertExpiredTransferScrubbed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID uuid.UUID, id interface{}) {
	t.Helper()
	var status, errorCode string
	var envelope, private []byte
	var leaseValid bool
	if err := pool.QueryRow(ctx, `SELECT status,error_code,envelope,private_key_ciphertext,lease_token IS NOT NULL FROM role_source_secret_transfer WHERE source_id=$1 AND id=$2`, sourceID, id).Scan(&status, &errorCode, &envelope, &private, &leaseValid); err != nil {
		t.Fatal(err)
	}
	if status != "expired" || errorCode != "expired" || len(envelope) != 0 || len(private) != 60 || !bytes.Equal(private, make([]byte, 60)) || leaseValid {
		t.Fatalf("expired transfer not scrubbed status=%s error=%s envelope=%d private=%x lease=%v", status, errorCode, len(envelope), private, leaseValid)
	}
}

func assertMaterializedAgentSecrets(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture, roleID, environment, secretValue, mcpID string, mcpDefinition json.RawMessage) {
	t.Helper()
	var customEnv, mcpConfig []byte
	if err := pool.QueryRow(ctx, `
		SELECT agent.custom_env,agent.mcp_config
		FROM role_source_object_mapping mapping
		JOIN agent ON agent.id=mapping.target_id AND agent.workspace_id=mapping.workspace_id
		WHERE mapping.source_id=$1 AND mapping.workspace_id=$2
		  AND mapping.source_kind='role' AND mapping.source_parent_id='' AND mapping.source_object_id=$3
	`, fixture.sourceID, fixture.workspaceID, roleID).Scan(&customEnv, &mcpConfig); err != nil {
		t.Fatal(err)
	}
	environmentValues := map[string]string{}
	if err := json.Unmarshal(customEnv, &environmentValues); err != nil || environmentValues[environment] != secretValue {
		t.Fatalf("materialized environment=%s err=%v", customEnv, err)
	}
	_, servers, err := decodeTargetMCP(mcpConfig)
	if err != nil {
		t.Fatalf("materialized MCP=%s err=%v", mcpConfig, err)
	}
	if canonicalJSONDigest(t, servers[mcpID]) != canonicalJSONDigest(t, mcpDefinition) {
		t.Fatalf("materialized MCP=%s does not match source definition=%s", servers[mcpID], mcpDefinition)
	}
}

func cleanupSecretTransferMaterializedAgent(t *testing.T, pool *pgxpool.Pool, workspaceID uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `DELETE FROM agent WHERE workspace_id=$1`, workspaceID); err != nil {
		t.Errorf("delete secret-transfer materialized Agent: %v", err)
	}
}

func assertSecretAuditAndControlPlaneRedaction(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID uuid.UUID, transferIDValue interface{}, forbidden ...string) {
	t.Helper()
	var eventCounts []byte
	if err := pool.QueryRow(ctx, `
		SELECT jsonb_build_object(
		  'requested',count(*) FILTER (WHERE event_type='secret_transfer_requested'),
		  'claimed',count(*) FILTER (WHERE event_type='secret_transfer_claimed'),
		  'submitted',count(*) FILTER (WHERE event_type='secret_transfer_submitted'),
		  'consumed',count(*) FILTER (WHERE event_type='secret_transfer_consumed')
		)::text
		FROM role_source_audit_event
		WHERE source_id=$1 AND payload->>'operation_id'=$2::text
	`, sourceID, transferIDValue).Scan(&eventCounts); err != nil {
		t.Fatal(err)
	}
	if string(eventCounts) != `{"claimed": 1, "consumed": 1, "requested": 1, "submitted": 1}` {
		t.Fatalf("secret audit counts=%s", eventCounts)
	}
	var durable []byte
	if err := pool.QueryRow(ctx, `
		SELECT convert_to(
		  coalesce((SELECT string_agg(payload::text,'') FROM role_source_audit_event WHERE source_id=$1),'') ||
		  coalesce((SELECT string_agg(receipt::text,'') FROM role_source_apply WHERE source_id=$1),'') ||
		  coalesce((SELECT string_agg(receipt_digest,'') FROM role_source_outbox WHERE source_id=$1),'')
		,'UTF8')
	`, sourceID).Scan(&durable); err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext(t, "audit/apply/outbox", durable, forbidden...)
}

func assertNoPlaintext(t *testing.T, location string, body []byte, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if bytes.Contains(body, []byte(value)) {
			t.Fatalf("%s exposed plaintext marker %q", location, value)
		}
	}
}
