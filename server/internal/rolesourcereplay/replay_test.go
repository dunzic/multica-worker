package rolesourcereplay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationCanonicalRoundTripAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	auth, err := NewAuthorization(Summary{
		OutboxID: "00000000-0000-4000-8000-000000000001", Status: "dead", Attempt: 20, NextGeneration: 1,
		ExpectedReceiptDigest: "sha256:" + strings.Repeat("a", 64),
	}, "dependency_recovered", "sha256:"+strings.Repeat("b", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := CanonicalAuthorization(auth)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Authorization
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.ExpiresAt.Sub(decoded.RequestedAt) != AuthorizationTTL {
		t.Fatalf("canonical authorization=%s err=%v", body, err)
	}
	auth.ExpiresAt = auth.ExpiresAt.Add(time.Second)
	if _, err := CanonicalAuthorization(auth); err == nil {
		t.Fatal("authorization with widened lifetime was accepted")
	}
	if _, err := NewAuthorization(Summary{
		OutboxID: "00000000-0000-4000-8000-000000000001", Status: "dead", Attempt: 19, NextGeneration: 1,
		ExpectedReceiptDigest: "sha256:" + strings.Repeat("a", 64),
	}, "dependency_recovered", "sha256:"+strings.Repeat("b", 64), now); err == nil {
		t.Fatal("dead event before terminal attempt was accepted")
	}
}

func TestReplayKeyringRejectsAliasesAndSignaturesRequireTwoKeys(t *testing.T) {
	publicA, privateA, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicB, privateB, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]string{
		"requester_1": base64.StdEncoding.EncodeToString(publicA),
		"approver_1":  base64.StdEncoding.EncodeToString(publicB),
	})
	keys, err := DecodePublicKeyring(encoded)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("authorization")
	input := ExecuteInput{PublicKeys: keys,
		Requester: Signature{KeyID: "requester_1", Value: ed25519.Sign(privateA, message)},
		Approver:  Signature{KeyID: "approver_1", Value: ed25519.Sign(privateB, message)},
	}
	if err := verifySignatures(message, input); err != nil {
		t.Fatal(err)
	}
	input.Approver.Value[0] ^= 0xff
	if err := verifySignatures(message, input); err == nil {
		t.Fatal("tampered signature was accepted")
	}
	aliases, _ := json.Marshal(map[string]string{
		"requester_1": base64.StdEncoding.EncodeToString(publicA),
		"approver_1":  base64.StdEncoding.EncodeToString(publicA),
	})
	if _, err := DecodePublicKeyring(aliases); err == nil {
		t.Fatal("one public key under two identities was accepted")
	}
	duplicateID := []byte(`{"requester_1":"` + base64.StdEncoding.EncodeToString(publicA) + `","requester_1":"` + base64.StdEncoding.EncodeToString(publicB) + `"}`)
	if _, err := DecodePublicKeyring(duplicateID); err == nil {
		t.Fatal("duplicate JSON key id was accepted")
	}
	emptyThenValidDuplicate := []byte(`{"requester_1":"","requester_1":"` + base64.StdEncoding.EncodeToString(publicA) + `","approver_1":"` + base64.StdEncoding.EncodeToString(publicB) + `"}`)
	if _, err := DecodePublicKeyring(emptyThenValidDuplicate); err == nil {
		t.Fatal("duplicate JSON key id with an empty first value was accepted")
	}
	if bytes.Equal(publicA, publicB) {
		t.Fatal("test generated duplicate keys")
	}
}
