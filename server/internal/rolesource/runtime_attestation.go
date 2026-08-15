package rolesource

import (
	"encoding/json"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	RuntimeConfigUnattested             = "unattested"
	RuntimeConfigNotLoaded              = "not_loaded"
	RuntimeConfigMissing                = "config_missing"
	RuntimeConfigKindMismatch           = "kind_mismatch"
	RuntimeConfigAdapterVersionMismatch = "adapter_version_mismatch"
	RuntimeConfigInvalidAttestation     = "invalid_attestation"
	RuntimeConfigLoaded                 = "loaded"
)

// RuntimeConfigAttestationStatus evaluates one stored daemon statement
// against one source without exposing the source's private config identifier.
// It is shared by the read surface and lifecycle resume gate so they cannot
// disagree about whether a runtime loaded the selected adapter configuration.
func RuntimeConfigAttestationStatus(source db.RoleSource, attestation db.RoleSourceRuntimeAttestation) string {
	if !attestation.RuntimeID.Valid {
		return RuntimeConfigUnattested
	}
	if source.RuntimeID != attestation.RuntimeID || source.WorkspaceID != attestation.WorkspaceID {
		return RuntimeConfigInvalidAttestation
	}
	var loadedConfigs []protocol.RoleSourceLoadedConfig
	if err := json.Unmarshal(attestation.Sources, &loadedConfigs); err != nil {
		return RuntimeConfigInvalidAttestation
	}
	revision := ""
	if attestation.ConfigRevision.Valid {
		revision = attestation.ConfigRevision.String
	}
	if err := protocol.ValidateRoleSourceConfigAttestation(protocol.RoleSourceConfigAttestation{
		ContractVersion: attestation.ContractVersion,
		Loaded:          attestation.Loaded,
		AttestationID:   attestation.AttestationID,
		Revision:        revision,
		Sources:         loadedConfigs,
	}); err != nil {
		return RuntimeConfigInvalidAttestation
	}
	if !attestation.Loaded {
		return RuntimeConfigNotLoaded
	}
	expected, err := protocol.RoleSourceConfigIDDigest(util.UUIDToString(source.RuntimeID), source.DaemonConfigID)
	if err != nil {
		return RuntimeConfigInvalidAttestation
	}
	for _, config := range loadedConfigs {
		if config.ConfigIDDigest != expected {
			continue
		}
		if config.Kind != source.Kind {
			return RuntimeConfigKindMismatch
		}
		if config.AdapterVersion != source.AdapterVersion {
			return RuntimeConfigAdapterVersionMismatch
		}
		return RuntimeConfigLoaded
	}
	return RuntimeConfigMissing
}
