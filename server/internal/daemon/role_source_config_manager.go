package daemon

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/rolesource/agentwaker"
	"github.com/multica-ai/multica/server/internal/rolesource/manifestdir"
	"github.com/multica-ai/multica/server/internal/rolesource/signedremote"
)

func DefaultRoleSourceConfigPath(profile string) (string, error) {
	directory, err := cli.ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "role-sources.json"), nil
}

const RoleSourceConfigAbsentRevision = "absent"

var (
	ErrRoleSourceConfigBusy     = errors.New("role source config is being changed by another process")
	ErrRoleSourceConfigConflict = errors.New("role source config revision conflict")
	replaceRoleSourceConfigFile = replaceRoleSourceConfig
)

// RoleSourceManagedDocument is the secret-free desired state accepted by the
// CLI. The manager owns digest-key generation and preservation; callers can
// never import or export that key through this contract.
type RoleSourceManagedDocument struct {
	Version      int                                `json:"version"`
	AllowedRoots []string                           `json:"allowed_roots"`
	Sources      map[string]RoleSourceManagedSource `json:"sources"`
}

type RoleSourceManagedSourceSummary struct {
	ID         string                       `json:"id"`
	Kind       rolesource.Kind              `json:"kind"`
	Configured bool                         `json:"configured"`
	Attributes []rolesource.ConfigAttribute `json:"attributes,omitempty"`
}

// RoleSourceConfigSummary is safe to print locally: it contains no digest key,
// raw adapter config, or absolute path. Revision is a CAS token over the full
// private file, so even a key-only rotation invalidates stale writers.
type RoleSourceConfigSummary struct {
	Revision         string                           `json:"revision"`
	Version          int                              `json:"version"`
	ConfigFileName   string                           `json:"config_file_name"`
	AllowedRootNames []string                         `json:"allowed_root_names"`
	SourceCount      int                              `json:"source_count"`
	Sources          []RoleSourceManagedSourceSummary `json:"sources"`
	DigestKeyActive  bool                             `json:"digest_key_active"`
	ValidationStatus string                           `json:"validation_status"`
	ValidationCode   string                           `json:"validation_code,omitempty"`
	RescanRequired   bool                             `json:"rescan_required,omitempty"`
}

func ShowRoleSourceConfig(configPath string) (RoleSourceConfigSummary, error) {
	configPath, err := validateRoleSourceManagedPath(configPath)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	body, err := readRoleSourceConfigFile(configPath)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	defer clear(body)
	document, err := decodeRoleSourceConfigDocument(body)
	if err != nil {
		return RoleSourceConfigSummary{
			Revision: roleSourceConfigRevision(body), ConfigFileName: filepath.Base(configPath),
			ValidationStatus: "invalid", ValidationCode: "invalid_document",
		}, nil
	}
	defer clear(document.DigestKey)
	return summarizeRoleSourceConfig(configPath, body, document, false)
}

// ApplyRoleSourceManagedConfig validates and atomically publishes desiredBody.
// expectedRevision is mandatory: use "absent" for first creation and the exact
// revision returned by Show for an update. This prevents silent lost updates.
func ApplyRoleSourceManagedConfig(configPath, expectedRevision string, desiredBody []byte) (RoleSourceConfigSummary, error) {
	configPath, err := validateRoleSourceManagedPath(configPath)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	desired, err := decodeRoleSourceManagedDocument(desiredBody)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	expectedRevision, err = validateRoleSourceExpectedRevision(expectedRevision)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	return mutateRoleSourceConfig(configPath, expectedRevision, func(current *roleSourceConfigDocument, replacingInvalid bool) (roleSourceConfigDocument, bool, error) {
		document := roleSourceConfigDocument{
			Version: desired.Version, AllowedRoots: append([]string(nil), desired.AllowedRoots...),
			Sources: cloneRoleSourceManagedSources(desired.Sources),
		}
		if roleSourceDocumentRequiresAgentWaker(document) {
			if current != nil && len(current.DigestKey) == 32 {
				document.DigestKey = append([]byte(nil), current.DigestKey...)
			} else {
				document.DigestKey = make([]byte, 32)
				if _, err := rand.Read(document.DigestKey); err != nil {
					return roleSourceConfigDocument{}, false, fmt.Errorf("generate role source digest key: %w", err)
				}
			}
		}
		return document, replacingInvalid && roleSourceDocumentRequiresAgentWaker(document), nil
	})
}

// RotateRoleSourceDigestKey performs a key-only CAS update. Callers must obtain
// explicit operator confirmation before invoking it because every keyed
// environment-value digest changes and all AgentWaker sources need review.
func RotateRoleSourceDigestKey(configPath, expectedRevision string) (RoleSourceConfigSummary, error) {
	configPath, err := validateRoleSourceManagedPath(configPath)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	expectedRevision, err = validateRoleSourceExpectedRevision(expectedRevision)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	if expectedRevision == RoleSourceConfigAbsentRevision {
		return RoleSourceConfigSummary{}, errors.New("cannot rotate a digest key before role source config exists")
	}
	return mutateRoleSourceConfig(configPath, expectedRevision, func(current *roleSourceConfigDocument, _ bool) (roleSourceConfigDocument, bool, error) {
		if current == nil || !roleSourceDocumentRequiresAgentWaker(*current) {
			return roleSourceConfigDocument{}, false, errors.New("digest key rotation requires at least one AgentWaker source")
		}
		document := cloneRoleSourceConfigDocument(*current)
		clear(document.DigestKey)
		document.DigestKey = make([]byte, 32)
		if _, err := rand.Read(document.DigestKey); err != nil {
			return roleSourceConfigDocument{}, false, fmt.Errorf("generate role source digest key: %w", err)
		}
		return document, true, nil
	})
}

func mutateRoleSourceConfig(configPath, expectedRevision string, mutation func(*roleSourceConfigDocument, bool) (roleSourceConfigDocument, bool, error)) (RoleSourceConfigSummary, error) {
	lock, err := openAndLockRoleSourceConfig(configPath + ".lock")
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	defer unlockRoleSourceConfig(lock)

	currentBody, currentDocument, currentInfo, err := readCurrentRoleSourceConfig(configPath)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	defer clear(currentBody)
	if currentDocument != nil {
		defer clear(currentDocument.DigestKey)
	}
	actualRevision := RoleSourceConfigAbsentRevision
	if currentBody != nil {
		actualRevision = roleSourceConfigRevision(currentBody)
	}
	if actualRevision != expectedRevision {
		return RoleSourceConfigSummary{}, fmt.Errorf("%w: expected %s, found %s", ErrRoleSourceConfigConflict, expectedRevision, actualRevision)
	}

	document, rescanRequired, err := mutation(currentDocument, currentBody != nil && currentDocument == nil)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	defer clear(document.DigestKey)
	if _, err := buildRoleSourceScannerFromClone(document); err != nil {
		return RoleSourceConfigSummary{}, fmt.Errorf("validate managed role source config: %w", err)
	}
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return RoleSourceConfigSummary{}, fmt.Errorf("encode managed role source config: %w", err)
	}
	body = append(body, '\n')
	defer clear(body)
	if len(body) > maxRoleSourceConfigBytes {
		return RoleSourceConfigSummary{}, errors.New("managed role source config exceeds size limit")
	}
	summary, err := summarizeRoleSourceConfig(configPath, body, document, rescanRequired)
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	if err := ensureRoleSourceTargetUnchanged(configPath, currentInfo); err != nil {
		return RoleSourceConfigSummary{}, err
	}
	if err := writeRoleSourceConfigAtomically(configPath, body); err != nil {
		return RoleSourceConfigSummary{}, fmt.Errorf("publish managed role source config: %w", err)
	}
	return summary, nil
}

func decodeRoleSourceManagedDocument(body []byte) (RoleSourceManagedDocument, error) {
	if len(body) == 0 || len(body) > maxRoleSourceConfigBytes {
		return RoleSourceManagedDocument{}, errors.New("managed role source document size is outside the allowed range")
	}
	var document RoleSourceManagedDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return RoleSourceManagedDocument{}, fmt.Errorf("decode managed role source document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RoleSourceManagedDocument{}, errors.New("managed role source document contains trailing JSON")
	}
	return document, nil
}

func summarizeRoleSourceConfig(configPath string, body []byte, document roleSourceConfigDocument, rescanRequired bool) (RoleSourceConfigSummary, error) {
	validationStatus := "valid"
	validationCode := ""
	if _, err := buildRoleSourceScannerFromClone(document); err != nil {
		validationStatus = "invalid"
		validationCode = classifyRoleSourceConfigValidationError(err)
	}
	registry, err := newRoleSourceSummaryRegistry()
	if err != nil {
		return RoleSourceConfigSummary{}, err
	}
	rootNames := make([]string, 0, len(document.AllowedRoots))
	for _, root := range document.AllowedRoots {
		rootNames = append(rootNames, filepath.Base(root))
	}
	sort.Strings(rootNames)
	ids := make([]string, 0, len(document.Sources))
	for id := range document.Sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sources := make([]RoleSourceManagedSourceSummary, 0, len(ids))
	for _, id := range ids {
		source := document.Sources[id]
		redacted, err := registry.RedactConfig(source.Kind, source.Config)
		if err != nil {
			sources = append(sources, RoleSourceManagedSourceSummary{ID: id, Kind: source.Kind})
			continue
		}
		sources = append(sources, RoleSourceManagedSourceSummary{
			ID: id, Kind: source.Kind, Configured: redacted.Configured,
			Attributes: append([]rolesource.ConfigAttribute(nil), redacted.Attributes...),
		})
	}
	return RoleSourceConfigSummary{
		Revision: roleSourceConfigRevision(body), Version: document.Version,
		ConfigFileName: filepath.Base(configPath), AllowedRootNames: rootNames,
		SourceCount: len(sources), Sources: sources, DigestKeyActive: len(document.DigestKey) == 32,
		ValidationStatus: validationStatus, ValidationCode: validationCode, RescanRequired: rescanRequired,
	}, nil
}

func newRoleSourceSummaryRegistry() (*rolesource.Registry, error) {
	acceptRoot := func(string) error { return nil }
	manifestAdapter, err := manifestdir.New(acceptRoot)
	if err != nil {
		return nil, err
	}
	dummyKey := make([]byte, 32)
	defer clear(dummyKey)
	agentWakerAdapter, err := agentwaker.New(dummyKey, acceptRoot)
	if err != nil {
		return nil, err
	}
	remoteAdapter, err := signedremote.New()
	if err != nil {
		return nil, err
	}
	return rolesource.NewRegistry(manifestAdapter, agentWakerAdapter, remoteAdapter)
}

func classifyRoleSourceConfigValidationError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "unsupported role source config version"):
		return "unsupported_version"
	case strings.Contains(message, "config count"):
		return "invalid_source_count"
	case strings.Contains(message, "digest_key"):
		return "invalid_digest_key"
	case strings.Contains(message, "allowed_roots") || strings.Contains(message, "allowed root"):
		return "invalid_allowed_roots"
	case strings.Contains(message, "invalid role source config id"):
		return "invalid_source_id"
	case strings.Contains(message, "unsupported local role source kind"):
		return "unsupported_source_kind"
	case strings.Contains(message, "validate role source config"):
		return "invalid_source_config"
	default:
		return "invalid_configuration"
	}
}

func validateRoleSourceManagedPath(configPath string) (string, error) {
	configPath = strings.TrimSpace(configPath)
	if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
		return "", errors.New("role source config file must be a clean absolute path")
	}
	parent := filepath.Dir(configPath)
	resolved, err := canonicalRoleSourceDirectory(parent)
	if err != nil {
		return "", fmt.Errorf("validate role source config directory: %w", err)
	}
	if resolved != parent {
		return "", errors.New("role source config directory contains a symlink")
	}
	info, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("inspect role source config directory: %w", err)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("role source config directory must not be group- or world-writable")
	}
	return configPath, nil
}

func validateRoleSourceExpectedRevision(revision string) (string, error) {
	revision = strings.TrimSpace(revision)
	if revision == RoleSourceConfigAbsentRevision {
		return revision, nil
	}
	if len(revision) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(revision, "sha256:") {
		return "", errors.New("expected revision must be 'absent' or a sha256 revision returned by show")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(revision, "sha256:")); err != nil {
		return "", errors.New("expected revision must be 'absent' or a sha256 revision returned by show")
	}
	return revision, nil
}

func readCurrentRoleSourceConfig(configPath string) ([]byte, *roleSourceConfigDocument, os.FileInfo, error) {
	info, err := os.Lstat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("inspect role source config file: %w", err)
	}
	body, err := readRoleSourceConfigFile(configPath)
	if err != nil {
		return nil, nil, nil, err
	}
	document, err := decodeRoleSourceConfigDocument(body)
	if err != nil {
		return body, nil, info, nil
	}
	return body, &document, info, nil
}

func ensureRoleSourceTargetUnchanged(configPath string, previous os.FileInfo) error {
	current, err := os.Lstat(configPath)
	if previous == nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recheck role source config target: %w", err)
		}
		return fmt.Errorf("%w: config appeared during update", ErrRoleSourceConfigConflict)
	}
	if err != nil {
		return fmt.Errorf("%w: config changed during update", ErrRoleSourceConfigConflict)
	}
	if !os.SameFile(previous, current) {
		return fmt.Errorf("%w: config changed during update", ErrRoleSourceConfigConflict)
	}
	return nil
}

func writeRoleSourceConfigAtomically(configPath string, body []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(configPath), ".role-source-config-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath) //nolint:errcheck
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceRoleSourceConfigFile(tempPath, configPath)
}

func roleSourceConfigRevision(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func roleSourceDocumentRequiresAgentWaker(document roleSourceConfigDocument) bool {
	for _, source := range document.Sources {
		if source.Kind == agentwaker.Kind {
			return true
		}
	}
	return false
}

func cloneRoleSourceManagedSources(sources map[string]RoleSourceManagedSource) map[string]RoleSourceManagedSource {
	result := make(map[string]RoleSourceManagedSource, len(sources))
	for id, source := range sources {
		result[id] = RoleSourceManagedSource{Kind: source.Kind, Config: append(json.RawMessage(nil), source.Config...)}
	}
	return result
}

func cloneRoleSourceConfigDocument(document roleSourceConfigDocument) roleSourceConfigDocument {
	return roleSourceConfigDocument{
		Version: document.Version, DigestKey: append([]byte(nil), document.DigestKey...),
		AllowedRoots: append([]string(nil), document.AllowedRoots...), Sources: cloneRoleSourceManagedSources(document.Sources),
	}
}

func buildRoleSourceScannerFromClone(document roleSourceConfigDocument) (*roleSourceScanner, error) {
	cloned := cloneRoleSourceConfigDocument(document)
	defer clear(cloned.DigestKey)
	return buildRoleSourceScanner(cloned)
}
