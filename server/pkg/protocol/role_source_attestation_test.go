package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoleSourceConfigAttestationIsCanonicalAndTamperEvident(t *testing.T) {
	firstDigest, err := RoleSourceConfigIDDigest("runtime-1", "agentwaker-main")
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := RoleSourceConfigIDDigest("runtime-1", "manifest-main")
	if err != nil {
		t.Fatal(err)
	}
	sources := []RoleSourceLoadedConfig{
		{ConfigIDDigest: firstDigest, Kind: "agentwaker", AdapterVersion: "1.0.0"},
		{ConfigIDDigest: secondDigest, Kind: "manifest_directory", AdapterVersion: "1.0.0"},
	}
	if sources[1].ConfigIDDigest < sources[0].ConfigIDDigest {
		sources[0], sources[1] = sources[1], sources[0]
	}
	attestation, err := NewRoleSourceConfigAttestation(true, "sha256:"+strings.Repeat("a", 64), []RoleSourceLoadedConfig{
		sources[0], sources[1],
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRoleSourceConfigAttestation(attestation); err != nil {
		t.Fatalf("valid attestation rejected: %v", err)
	}

	tampered := attestation
	tampered.Sources = append([]RoleSourceLoadedConfig(nil), attestation.Sources...)
	tampered.Sources[0].AdapterVersion = "9.9.9"
	if err := ValidateRoleSourceConfigAttestation(tampered); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered attestation error = %v", err)
	}

	unsorted := []RoleSourceLoadedConfig{attestation.Sources[1], attestation.Sources[0]}
	if _, err := NewRoleSourceConfigAttestation(true, attestation.Revision, unsorted); err == nil || !strings.Contains(err.Error(), "sorted") {
		t.Fatalf("unsorted attestation error = %v", err)
	}
}

func TestRoleSourceConfigAttestationRejectsUnknownWireFields(t *testing.T) {
	for _, body := range []string{
		`{"contract_version":"role-source-config-attestation-v1","loaded":false,"attestation_id":"sha256:` + strings.Repeat("a", 64) + `","private_path":"/secret"}`,
		`{"contract_version":"role-source-config-attestation-v1","loaded":true,"attestation_id":"sha256:` + strings.Repeat("a", 64) + `","revision":"sha256:` + strings.Repeat("b", 64) + `","sources":[{"config_id_digest":"sha256:` + strings.Repeat("c", 64) + `","kind":"agentwaker","adapter_version":"1.0.0","raw_config":{}}]}`,
	} {
		var attestation RoleSourceConfigAttestation
		if err := json.Unmarshal([]byte(body), &attestation); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("unknown wire field error = %v", err)
		}
	}
}

func TestRoleSourceConfigEvidenceIsRuntimeScoped(t *testing.T) {
	configA, err := RoleSourceConfigIDDigest("runtime-a", "production")
	if err != nil {
		t.Fatal(err)
	}
	configB, err := RoleSourceConfigIDDigest("runtime-b", "production")
	if err != nil {
		t.Fatal(err)
	}
	revision := "sha256:" + strings.Repeat("c", 64)
	revisionA, err := RoleSourceConfigRevisionDigest("runtime-a", revision)
	if err != nil {
		t.Fatal(err)
	}
	revisionB, err := RoleSourceConfigRevisionDigest("runtime-b", revision)
	if err != nil {
		t.Fatal(err)
	}
	if configA == configB || revisionA == revisionB || configA == "sha256:"+strings.Repeat("0", 64) {
		t.Fatalf("runtime-scoped evidence collided: config=%q revision=%q", configA, revisionA)
	}
}

func TestRoleSourceConfigAttestationUnloadedStateCannotCarryPrivateMetadata(t *testing.T) {
	unloaded, err := NewRoleSourceConfigAttestation(false, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if unloaded.Loaded || unloaded.Revision != "" || len(unloaded.Sources) != 0 {
		t.Fatalf("unloaded attestation contains state: %+v", unloaded)
	}
	if err := ValidateRoleSourceConfigAttestation(unloaded); err != nil {
		t.Fatal(err)
	}

	unloaded.Revision = "sha256:" + strings.Repeat("b", 64)
	if err := ValidateRoleSourceConfigAttestation(unloaded); err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("unloaded metadata error = %v", err)
	}
}
