package rolesource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type testSecretBox struct{ marker byte }

func (b testSecretBox) SealWithAAD(plaintext, aad []byte) ([]byte, error) {
	return append([]byte{b.marker}, append(append([]byte(nil), aad...), plaintext...)...), nil
}

func (b testSecretBox) OpenWithAAD(sealed, aad []byte) ([]byte, error) {
	if len(sealed) < 1+len(aad) || sealed[0] != b.marker || !bytes.Equal(sealed[1:1+len(aad)], aad) {
		return nil, errors.New("authentication failed")
	}
	return append([]byte(nil), sealed[1+len(aad):]...), nil
}

func secretEnvelopeFixture(t *testing.T) (SecretEnvelopeKeyPair, SecretEnvelopeClaims, SecretEnvelopePayload) {
	t.Helper()
	keyPair, err := NewSecretEnvelopeKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	claims := SecretEnvelopeClaims{
		ContractVersion: SecretEnvelopeContractVersion,
		TransferID:      "00000000-0000-4000-8000-000000000051",
		WorkspaceID:     "00000000-0000-4000-8000-000000000001",
		SourceID:        "00000000-0000-4000-8000-000000000042",
		RoleID:          "writer",
		SnapshotDigest:  testSHA256("a"),
		ExpiresAt:       time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339Nano),
	}
	payload := SecretEnvelopePayload{
		Environment: map[string]string{"API_TOKEN": "super-secret-token", "API_BASE": "https://api.example.com"},
		MCPServers: map[string]json.RawMessage{
			"platform": json.RawMessage(`{"env":{"API_TOKEN":"${API_TOKEN}"},"command":"platform-mcp"}`),
		},
	}
	return keyPair, claims, payload
}

func TestSecretEnvelopeRoundTripDoesNotExposePlaintext(t *testing.T) {
	keyPair, claims, payload := secretEnvelopeFixture(t)
	envelope, err := SealSecretEnvelope(keyPair.PublicKey, claims, payload)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"super-secret-token", "platform-mcp", "API_BASE"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("secret envelope wire exposed %q", forbidden)
		}
	}
	opened, err := OpenSecretEnvelope(keyPair.PrivateKey, envelope, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if opened.Environment["API_TOKEN"] != "super-secret-token" || !strings.Contains(string(opened.MCPServers["platform"]), "platform-mcp") {
		t.Fatalf("opened payload = %+v", opened)
	}
}

func TestSecretEnvelopeDigestSurvivesJSONBRepresentationChanges(t *testing.T) {
	keyPair, claims, payload := secretEnvelopeFixture(t)
	envelope, err := SealSecretEnvelope(keyPair.PublicKey, claims, payload)
	if err != nil {
		t.Fatal(err)
	}
	canonicalBody, canonicalDigest, err := encodeSecretEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var jsonbValue map[string]any
	if err := json.Unmarshal(canonicalBody, &jsonbValue); err != nil {
		t.Fatal(err)
	}
	jsonbBody, err := json.Marshal(jsonbValue)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(jsonbBody, canonicalBody) {
		t.Fatal("test fixture did not change the JSON byte representation")
	}
	storedEnvelope, err := decodeStoredSecretEnvelope(jsonbBody)
	if err != nil {
		t.Fatal(err)
	}
	_, storedDigest, err := encodeSecretEnvelope(storedEnvelope)
	if err != nil || storedDigest != canonicalDigest {
		t.Fatalf("stored digest=%s want=%s err=%v", storedDigest, canonicalDigest, err)
	}
}

func TestSecretEnvelopeClaimsPreventCrossTenantRoleAndSnapshotReplay(t *testing.T) {
	keyPair, claims, payload := secretEnvelopeFixture(t)
	envelope, err := SealSecretEnvelope(keyPair.PublicKey, claims, payload)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*SecretEnvelopeClaims){
		func(value *SecretEnvelopeClaims) { value.WorkspaceID = "00000000-0000-4000-8000-000000000099" },
		func(value *SecretEnvelopeClaims) { value.SourceID = "00000000-0000-4000-8000-000000000099" },
		func(value *SecretEnvelopeClaims) { value.RoleID = "reviewer" },
		func(value *SecretEnvelopeClaims) { value.SnapshotDigest = testSHA256("b") },
		func(value *SecretEnvelopeClaims) { value.TransferID = "00000000-0000-4000-8000-000000000099" },
	}
	for _, mutate := range mutations {
		copy := envelope
		mutate(&copy.Claims)
		if _, err := OpenSecretEnvelope(keyPair.PrivateKey, copy, time.Now().UTC()); !errors.Is(err, ErrInvalidSecretEnvelope) {
			t.Fatalf("mutated claims error = %v", err)
		}
	}
}

func TestPersistedSecretTransferIdentityToleratesBoundedDBClockSkewAndBindsExpiry(t *testing.T) {
	expiresAt := time.Date(2026, 8, 14, 9, 30, 0, 123456789, time.UTC)
	claims := SecretEnvelopeClaims{
		ContractVersion: SecretEnvelopeContractVersion,
		TransferID:      "00000000-0000-4000-8000-000000000051",
		WorkspaceID:     "00000000-0000-4000-8000-000000000001",
		SourceID:        "00000000-0000-4000-8000-000000000042",
		RoleID:          "writer",
		SnapshotDigest:  testSHA256("a"),
		ExpiresAt:       expiresAt.Format(time.RFC3339Nano),
	}
	claimsBody, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	row := db.RoleSourceSecretTransfer{
		ID: util.MustParseUUID(claims.TransferID), WorkspaceID: util.MustParseUUID(claims.WorkspaceID),
		SourceID: util.MustParseUUID(claims.SourceID), RoleID: claims.RoleID, SnapshotDigest: claims.SnapshotDigest,
		Claims: claimsBody,
		// The DB clock is 30 seconds behind the application clock. The
		// authenticated expiry is still exactly the application's 15-minute TTL.
		CreatedAt: pgtype.Timestamptz{Time: expiresAt.Add(-15*time.Minute - 30*time.Second), Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt.Truncate(time.Microsecond), Valid: true},
	}
	if err := ValidatePersistedSecretTransferIdentity(row); err != nil {
		t.Fatalf("bounded database clock skew rejected: %v", err)
	}
	// PostgreSQL jsonb does not preserve the byte representation inserted by
	// the application. Decryption AAD must therefore be regenerated from the
	// validated typed claims instead of reusing row.Claims.
	row.Claims = []byte(fmt.Sprintf(`{
		"workspace_id": %q, "transfer_id": %q, "source_id": %q,
		"snapshot_digest": %q, "role_id": %q, "expires_at": %q,
		"contract_version": %q
	}`, claims.WorkspaceID, claims.TransferID, claims.SourceID, claims.SnapshotDigest, claims.RoleID, claims.ExpiresAt, claims.ContractVersion))
	_, claimsAAD, err := validatedStoredSecretTransferWithAAD(row)
	if err != nil {
		t.Fatalf("jsonb-reformatted claims rejected: %v", err)
	}
	wantAAD, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(claimsAAD, wantAAD) || bytes.Equal(claimsAAD, row.Claims) {
		t.Fatalf("canonical AAD=%s want=%s row=%s err=%v", claimsAAD, wantAAD, row.Claims, err)
	}

	tamperedExpiry := row
	tamperedExpiry.ExpiresAt.Time = expiresAt.Add(time.Second)
	if err := ValidatePersistedSecretTransferIdentity(tamperedExpiry); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("row expiry tamper error=%v", err)
	}

	excessiveSkew := row
	excessiveSkew.CreatedAt.Time = expiresAt.Add(-15*time.Minute - secretTransferClockSkew - time.Second)
	if err := ValidatePersistedSecretTransferIdentity(excessiveSkew); err == nil || !strings.Contains(err.Error(), "creation window") {
		t.Fatalf("excessive clock skew error=%v", err)
	}
}

func TestSecretKeyringKeepsPreviousDecryptOnlyKeyDuringRotation(t *testing.T) {
	control := &ControlPlane{}
	if err := control.SetSecretBox(testSecretBox{marker: 2}, "v2"); err != nil {
		t.Fatal(err)
	}
	if err := control.AddSecretDecryptionBox(testSecretBox{marker: 1}, "v1"); err != nil {
		t.Fatal(err)
	}
	current, ok := control.secretBoxFor("v2")
	if !ok {
		t.Fatal("current key is unavailable")
	}
	sealed, err := current.SealWithAAD([]byte("private"), []byte("claims"))
	if err != nil || sealed[0] != 2 {
		t.Fatalf("current key seal = %x err=%v", sealed, err)
	}
	previous, ok := control.secretBoxFor("v1")
	if !ok {
		t.Fatal("previous key is unavailable")
	}
	legacy, err := (testSecretBox{marker: 1}).SealWithAAD([]byte("legacy"), []byte("claims"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := previous.OpenWithAAD(legacy, []byte("claims"))
	if err != nil || string(opened) != "legacy" {
		t.Fatalf("previous key open = %q err=%v", opened, err)
	}
	if err := control.AddSecretDecryptionBox(testSecretBox{marker: 3}, "v2"); err == nil {
		t.Fatal("duplicate key id accepted")
	}
}

func TestSecretEnvelopeRejectsWrongKeyTamperingAndExpiry(t *testing.T) {
	keyPair, claims, payload := secretEnvelopeFixture(t)
	envelope, err := SealSecretEnvelope(keyPair.PublicKey, claims, payload)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewSecretEnvelopeKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSecretEnvelope(other.PrivateKey, envelope, time.Now().UTC()); !errors.Is(err, ErrInvalidSecretEnvelope) {
		t.Fatalf("wrong key error = %v", err)
	}
	tampered := envelope
	last := len(tampered.Ciphertext) - 1
	if tampered.Ciphertext[last] == 'A' {
		tampered.Ciphertext = tampered.Ciphertext[:last] + "B"
	} else {
		tampered.Ciphertext = tampered.Ciphertext[:last] + "A"
	}
	if _, err := OpenSecretEnvelope(keyPair.PrivateKey, tampered, time.Now().UTC()); !errors.Is(err, ErrInvalidSecretEnvelope) {
		t.Fatalf("tampered ciphertext error = %v", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSecretEnvelope(keyPair.PrivateKey, envelope, expiresAt); !errors.Is(err, ErrExpiredSecretEnvelope) {
		t.Fatalf("expired envelope error = %v", err)
	}
}

func TestSecretEnvelopeRejectsOversizedOrUnsafePayload(t *testing.T) {
	keyPair, claims, payload := secretEnvelopeFixture(t)
	payload.Environment["BAD-KEY"] = "value"
	if _, err := SealSecretEnvelope(keyPair.PublicKey, claims, payload); !errors.Is(err, ErrInvalidSecretEnvelope) {
		t.Fatalf("unsafe key error = %v", err)
	}
	delete(payload.Environment, "BAD-KEY")
	payload.Environment["TOO_BIG"] = strings.Repeat("x", 32<<10+1)
	if _, err := SealSecretEnvelope(keyPair.PublicKey, claims, payload); !errors.Is(err, ErrInvalidSecretEnvelope) {
		t.Fatalf("oversized value error = %v", err)
	}
}
