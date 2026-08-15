package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const (
	RoleSourceConfigAttestationContractV1 = "role-source-config-attestation-v1"
	MaxRoleSourceAttestedConfigs          = 512
)

var (
	roleSourceAttestationDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	roleSourceConfigIDPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	roleSourceAttestedKindPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
)

// RoleSourceLoadedConfig is the minimum source-neutral fact needed to prove
// that a daemon loaded the config selected by a role_source row. It must never
// contain paths, adapter config, allowed roots, or secret material.
type RoleSourceLoadedConfig struct {
	ConfigIDDigest string `json:"config_id_digest"`
	Kind           string `json:"kind"`
	AdapterVersion string `json:"adapter_version"`
}

// RoleSourceConfigIDDigest creates a runtime-scoped commitment to a private
// local config ID. A daemon may serve several workspaces; salting by runtime
// prevents one workspace's evidence API from exposing or correlating another
// workspace's raw local IDs.
func RoleSourceConfigIDDigest(runtimeID, configID string) (string, error) {
	if runtimeID == "" || len(runtimeID) > 128 {
		return "", errors.New("invalid role source attestation runtime id")
	}
	if !roleSourceConfigIDPattern.MatchString(configID) {
		return "", errors.New("invalid role source config id")
	}
	digest := sha256.Sum256([]byte(runtimeID + "\x00" + configID))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// RoleSourceConfigRevisionDigest scopes the exact private-file revision to one
// runtime before it crosses the daemon boundary. This retains change evidence
// without giving members of separate workspaces a shared daemon fingerprint.
func RoleSourceConfigRevisionDigest(runtimeID, revision string) (string, error) {
	if runtimeID == "" || len(runtimeID) > 128 {
		return "", errors.New("invalid role source attestation runtime id")
	}
	if !roleSourceAttestationDigestPattern.MatchString(revision) {
		return "", errors.New("invalid role source private config revision")
	}
	digest := sha256.Sum256([]byte(runtimeID + "\x00" + revision))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (config *RoleSourceLoadedConfig) UnmarshalJSON(body []byte) error {
	type wire RoleSourceLoadedConfig
	var decoded wire
	if err := decodeStrictRoleSourceAttestationJSON(body, &decoded); err != nil {
		return err
	}
	*config = RoleSourceLoadedConfig(decoded)
	return nil
}

// RoleSourceConfigAttestation is a bounded, redacted statement about the
// role-source configuration actually loaded by one daemon process. Revision
// is a runtime-scoped commitment to the exact private config file;
// AttestationID additionally commits to the redacted adapter commitments and
// is the server acknowledgement token.
type RoleSourceConfigAttestation struct {
	ContractVersion string                   `json:"contract_version"`
	Loaded          bool                     `json:"loaded"`
	AttestationID   string                   `json:"attestation_id"`
	Revision        string                   `json:"revision,omitempty"`
	Sources         []RoleSourceLoadedConfig `json:"sources,omitempty"`
}

func (attestation *RoleSourceConfigAttestation) UnmarshalJSON(body []byte) error {
	type wire RoleSourceConfigAttestation
	var decoded wire
	if err := decodeStrictRoleSourceAttestationJSON(body, &decoded); err != nil {
		return err
	}
	*attestation = RoleSourceConfigAttestation(decoded)
	return nil
}

func decodeStrictRoleSourceAttestationJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("role source attestation contains trailing JSON")
	}
	return nil
}

type roleSourceConfigAttestationDigestInput struct {
	ContractVersion string                   `json:"contract_version"`
	Loaded          bool                     `json:"loaded"`
	Revision        string                   `json:"revision,omitempty"`
	Sources         []RoleSourceLoadedConfig `json:"sources,omitempty"`
}

// NewRoleSourceConfigAttestation validates and seals a normalized attestation.
// Callers must supply Sources in strictly increasing config_id_digest order.
func NewRoleSourceConfigAttestation(loaded bool, revision string, sources []RoleSourceLoadedConfig) (RoleSourceConfigAttestation, error) {
	attestation := RoleSourceConfigAttestation{
		ContractVersion: RoleSourceConfigAttestationContractV1,
		Loaded:          loaded,
		Revision:        revision,
		Sources:         append([]RoleSourceLoadedConfig(nil), sources...),
	}
	if err := validateRoleSourceConfigAttestationBody(attestation); err != nil {
		return RoleSourceConfigAttestation{}, err
	}
	digest, err := roleSourceConfigAttestationDigest(attestation)
	if err != nil {
		return RoleSourceConfigAttestation{}, err
	}
	attestation.AttestationID = digest
	return attestation, nil
}

// ValidateRoleSourceConfigAttestation rejects non-canonical or forged wire
// statements before they reach persistence.
func ValidateRoleSourceConfigAttestation(attestation RoleSourceConfigAttestation) error {
	if err := validateRoleSourceConfigAttestationBody(attestation); err != nil {
		return err
	}
	if !roleSourceAttestationDigestPattern.MatchString(attestation.AttestationID) {
		return errors.New("invalid role source attestation id")
	}
	digest, err := roleSourceConfigAttestationDigest(attestation)
	if err != nil {
		return err
	}
	if digest != attestation.AttestationID {
		return errors.New("role source attestation id does not match body")
	}
	return nil
}

func validateRoleSourceConfigAttestationBody(attestation RoleSourceConfigAttestation) error {
	if attestation.ContractVersion != RoleSourceConfigAttestationContractV1 {
		return errors.New("unsupported role source attestation contract")
	}
	if !attestation.Loaded {
		if attestation.Revision != "" || len(attestation.Sources) != 0 {
			return errors.New("unloaded role source attestation must not contain revision or sources")
		}
		return nil
	}
	if !roleSourceAttestationDigestPattern.MatchString(attestation.Revision) {
		return errors.New("invalid role source config revision")
	}
	if len(attestation.Sources) == 0 || len(attestation.Sources) > MaxRoleSourceAttestedConfigs {
		return fmt.Errorf("role source attestation source count must be between 1 and %d", MaxRoleSourceAttestedConfigs)
	}
	previousDigest := ""
	for _, source := range attestation.Sources {
		if !roleSourceAttestationDigestPattern.MatchString(source.ConfigIDDigest) {
			return errors.New("invalid role source attested config id digest")
		}
		if previousDigest != "" && source.ConfigIDDigest <= previousDigest {
			return errors.New("role source attested configs must be sorted and unique")
		}
		if !roleSourceAttestedKindPattern.MatchString(source.Kind) {
			return errors.New("invalid role source attested kind")
		}
		if len(source.AdapterVersion) == 0 || len(source.AdapterVersion) > 100 {
			return errors.New("invalid role source attested adapter version")
		}
		previousDigest = source.ConfigIDDigest
	}
	return nil
}

func roleSourceConfigAttestationDigest(attestation RoleSourceConfigAttestation) (string, error) {
	body, err := json.Marshal(roleSourceConfigAttestationDigestInput{
		ContractVersion: attestation.ContractVersion,
		Loaded:          attestation.Loaded,
		Revision:        attestation.Revision,
		Sources:         attestation.Sources,
	})
	if err != nil {
		return "", fmt.Errorf("encode role source config attestation: %w", err)
	}
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
