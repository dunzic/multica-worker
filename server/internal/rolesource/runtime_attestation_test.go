package rolesource

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRuntimeConfigAttestationStatusUsesOneStrictContract(t *testing.T) {
	runtimeID := util.MustParseUUID("00000000-0000-4000-8000-000000000051")
	workspaceID := util.MustParseUUID("00000000-0000-4000-8000-000000000052")
	source := db.RoleSource{
		RuntimeID: runtimeID, WorkspaceID: workspaceID, DaemonConfigID: "production",
		Kind: "agentwaker_directory", AdapterVersion: "1.0.0",
	}
	configDigest, err := protocol.RoleSourceConfigIDDigest(util.UUIDToString(runtimeID), source.DaemonConfigID)
	if err != nil {
		t.Fatal(err)
	}
	build := func(configs []protocol.RoleSourceLoadedConfig) db.RoleSourceRuntimeAttestation {
		t.Helper()
		attestation, err := protocol.NewRoleSourceConfigAttestation(true, "sha256:"+strings.Repeat("a", 64), configs)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(attestation.Sources)
		if err != nil {
			t.Fatal(err)
		}
		return db.RoleSourceRuntimeAttestation{
			RuntimeID: runtimeID, WorkspaceID: workspaceID, ContractVersion: attestation.ContractVersion,
			Loaded: true, AttestationID: attestation.AttestationID,
			ConfigRevision: pgtype.Text{String: attestation.Revision, Valid: true}, Sources: body,
		}
	}

	loaded := build([]protocol.RoleSourceLoadedConfig{{ConfigIDDigest: configDigest, Kind: source.Kind, AdapterVersion: source.AdapterVersion}})
	if got := RuntimeConfigAttestationStatus(source, loaded); got != RuntimeConfigLoaded {
		t.Fatalf("loaded status=%q", got)
	}
	kindMismatch := build([]protocol.RoleSourceLoadedConfig{{ConfigIDDigest: configDigest, Kind: "manifest_directory", AdapterVersion: source.AdapterVersion}})
	if got := RuntimeConfigAttestationStatus(source, kindMismatch); got != RuntimeConfigKindMismatch {
		t.Fatalf("kind mismatch status=%q", got)
	}
	versionMismatch := build([]protocol.RoleSourceLoadedConfig{{ConfigIDDigest: configDigest, Kind: source.Kind, AdapterVersion: "2.0.0"}})
	if got := RuntimeConfigAttestationStatus(source, versionMismatch); got != RuntimeConfigAdapterVersionMismatch {
		t.Fatalf("version mismatch status=%q", got)
	}
	otherDigest, _ := protocol.RoleSourceConfigIDDigest(util.UUIDToString(runtimeID), "other")
	missing := build([]protocol.RoleSourceLoadedConfig{{ConfigIDDigest: otherDigest, Kind: source.Kind, AdapterVersion: source.AdapterVersion}})
	if got := RuntimeConfigAttestationStatus(source, missing); got != RuntimeConfigMissing {
		t.Fatalf("missing status=%q", got)
	}

	tampered := loaded
	tampered.AttestationID = "sha256:" + strings.Repeat("0", 64)
	if got := RuntimeConfigAttestationStatus(source, tampered); got != RuntimeConfigInvalidAttestation {
		t.Fatalf("tampered status=%q", got)
	}
	crossRuntime := loaded
	crossRuntime.RuntimeID = util.MustParseUUID("00000000-0000-4000-8000-000000000053")
	if got := RuntimeConfigAttestationStatus(source, crossRuntime); got != RuntimeConfigInvalidAttestation {
		t.Fatalf("cross-runtime status=%q", got)
	}
}

func TestRuntimeConfigAttestationStatusPreservesUnloadedAndUnattested(t *testing.T) {
	runtimeID := util.MustParseUUID("00000000-0000-4000-8000-000000000061")
	workspaceID := util.MustParseUUID("00000000-0000-4000-8000-000000000062")
	source := db.RoleSource{RuntimeID: runtimeID, WorkspaceID: workspaceID, DaemonConfigID: "production"}
	if got := RuntimeConfigAttestationStatus(source, db.RoleSourceRuntimeAttestation{}); got != RuntimeConfigUnattested {
		t.Fatalf("zero evidence status=%q", got)
	}
	unloaded, err := protocol.NewRoleSourceConfigAttestation(false, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(unloaded.Sources)
	row := db.RoleSourceRuntimeAttestation{
		RuntimeID: runtimeID, WorkspaceID: workspaceID, ContractVersion: unloaded.ContractVersion,
		Loaded: false, AttestationID: unloaded.AttestationID, Sources: body,
	}
	if got := RuntimeConfigAttestationStatus(source, row); got != RuntimeConfigNotLoaded {
		t.Fatalf("unloaded status=%q", got)
	}
}
