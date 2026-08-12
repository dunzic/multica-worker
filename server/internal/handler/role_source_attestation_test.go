package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRoleSourceRuntimeConfigStatusFailsClosedOnDrift(t *testing.T) {
	runtimeID, err := util.ParseUUID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	configDigest, err := protocol.RoleSourceConfigIDDigest("11111111-1111-4111-8111-111111111111", "production")
	if err != nil {
		t.Fatal(err)
	}
	otherDigest, err := protocol.RoleSourceConfigIDDigest("11111111-1111-4111-8111-111111111111", "other")
	if err != nil {
		t.Fatal(err)
	}
	source := db.RoleSource{RuntimeID: runtimeID, DaemonConfigID: "production", Kind: "agentwaker", AdapterVersion: "1.0.0"}
	now := pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC), Valid: true}
	revision := pgtype.Text{String: "sha256:" + strings.Repeat("f", 64), Valid: true}
	encode := func(configs []protocol.RoleSourceLoadedConfig) []byte {
		body, err := json.Marshal(configs)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	tests := []struct {
		name    string
		loaded  bool
		configs []protocol.RoleSourceLoadedConfig
		body    []byte
		want    string
	}{
		{name: "not loaded", loaded: false, body: []byte(`[]`), want: "not_loaded"},
		{name: "missing", loaded: true, configs: []protocol.RoleSourceLoadedConfig{{ConfigIDDigest: otherDigest, Kind: "agentwaker", AdapterVersion: "1.0.0"}}, want: "config_missing"},
		{name: "kind drift", loaded: true, configs: []protocol.RoleSourceLoadedConfig{{ConfigIDDigest: configDigest, Kind: "manifest_directory", AdapterVersion: "1.0.0"}}, want: "kind_mismatch"},
		{name: "version drift", loaded: true, configs: []protocol.RoleSourceLoadedConfig{{ConfigIDDigest: configDigest, Kind: "agentwaker", AdapterVersion: "2.0.0"}}, want: "adapter_version_mismatch"},
		{name: "loaded", loaded: true, configs: []protocol.RoleSourceLoadedConfig{{ConfigIDDigest: configDigest, Kind: "agentwaker", AdapterVersion: "1.0.0"}}, want: "loaded"},
		{name: "corrupt", loaded: true, configs: []protocol.RoleSourceLoadedConfig{{ConfigIDDigest: configDigest, Kind: "agentwaker", AdapterVersion: "1.0.0"}}, body: []byte(`{"not":"an-array"}`), want: "invalid_attestation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := test.body
			if body == nil {
				body = encode(test.configs)
			}
			evidenceRevision := revision
			attestationRevision := revision.String
			if !test.loaded {
				evidenceRevision = pgtype.Text{}
				attestationRevision = ""
			}
			attestation, err := protocol.NewRoleSourceConfigAttestation(test.loaded, attestationRevision, test.configs)
			if err != nil {
				t.Fatal(err)
			}
			got := roleSourceRuntimeConfigStatusFromEvidence(
				source, protocol.RoleSourceConfigAttestationContractV1, test.loaded, attestation.AttestationID,
				evidenceRevision, body, now, now,
			)
			if got.Status != test.want || got.AttestationID == nil || got.ObservedAt == nil || got.ChangedAt == nil {
				t.Fatalf("status = %+v, want %q with evidence metadata", got, test.want)
			}
			if test.loaded && got.Revision == nil {
				t.Fatalf("loaded status = %+v, want revision metadata", got)
			}
		})
	}
}

func TestRoleSourceRuntimeConfigStatusRejectsTamperedStoredEvidence(t *testing.T) {
	runtimeID, err := util.ParseUUID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	configDigest, err := protocol.RoleSourceConfigIDDigest("11111111-1111-4111-8111-111111111111", "production")
	if err != nil {
		t.Fatal(err)
	}
	configs := []protocol.RoleSourceLoadedConfig{{ConfigIDDigest: configDigest, Kind: "agentwaker", AdapterVersion: "1.0.0"}}
	body, err := json.Marshal(configs)
	if err != nil {
		t.Fatal(err)
	}
	now := pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC), Valid: true}
	got := roleSourceRuntimeConfigStatusFromEvidence(
		db.RoleSource{RuntimeID: runtimeID, DaemonConfigID: "production", Kind: "agentwaker", AdapterVersion: "1.0.0"},
		protocol.RoleSourceConfigAttestationContractV1, true, "sha256:"+strings.Repeat("a", 64),
		pgtype.Text{String: "sha256:" + strings.Repeat("f", 64), Valid: true}, body, now, now,
	)
	if got.Status != "invalid_attestation" {
		t.Fatalf("tampered stored evidence status = %q, want invalid_attestation", got.Status)
	}
}

func TestRoleSourceRuntimeAttestationHistoryResponseOmitsDaemonConfigCommitments(t *testing.T) {
	body, err := json.Marshal(roleSourceRuntimeAttestationObservationResponse{
		Status: "loaded", ContractVersion: protocol.RoleSourceConfigAttestationContractV1,
		Loaded: true, AttestationID: "sha256:" + strings.Repeat("a", 64),
		FirstObservedAt: "2026-08-13T00:00:00Z", LastObservedAt: "2026-08-13T00:00:00Z", ObservationCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"config_id", "config_id_digest", "sources", "root_path", "allowed_roots", "raw_config"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("runtime attestation history exposed %q: %s", forbidden, body)
		}
	}
}

func TestRoleSourceRuntimeConfigStatusDistinguishesUnattestedRuntime(t *testing.T) {
	got := roleSourceRuntimeConfigStatus(db.RoleSource{}, db.RoleSourceRuntimeAttestation{})
	if got.Status != "unattested" || got.AttestationID != nil || got.Revision != nil || got.ObservedAt != nil {
		t.Fatalf("unattested status = %+v", got)
	}
}
