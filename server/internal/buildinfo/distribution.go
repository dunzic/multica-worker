// Package buildinfo carries the reviewed offline distribution baseline.
package buildinfo

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

//go:embed distribution.json
var manifestJSON []byte

type Upstream struct {
	Repository    string `json:"repository"`
	Version       string `json:"version"`
	ReleaseCommit string `json:"release_commit"`
	Commit        string `json:"commit"`
}

type Manifest struct {
	SchemaVersion           int               `json:"schema_version"`
	Distribution            string            `json:"distribution"`
	Upstream                Upstream          `json:"upstream"`
	MinimumCLIVersion       string            `json:"minimum_cli_version"`
	RequiredCLICapabilities []string          `json:"required_cli_capabilities"`
	APIRequirements         map[string]string `json:"api_requirements"`
}

type CLIReport struct {
	Version      string   `json:"version"`
	BuildCommit  string   `json:"build_commit"`
	Distribution Manifest `json:"distribution"`
	Capabilities []string `json:"capabilities"`
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var developmentPattern = regexp.MustCompile(`-\d+-g[0-9a-f]+`)
var dirtyPattern = regexp.MustCompile(`-dirty(?:\+.*)?$`)
var hashVersionPattern = regexp.MustCompile(`^v?0\.0\.0-g[0-9a-f]+(?:-dirty)?(?:\+.*)?$`)
var fullVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+].*)?$`)

func canonicalVersion(version string) string { return "v" + strings.TrimPrefix(version, "v") }

func validVersion(version string) bool {
	return fullVersionPattern.MatchString(version) && semver.IsValid(canonicalVersion(version))
}

// CompareVersions implements SemVer precedence, including prerelease identifiers.
func CompareVersions(left, right string) (int, error) {
	if !validVersion(left) || !validVersion(right) {
		return 0, fmt.Errorf("invalid semantic versions: %q / %q", left, right)
	}
	return semver.Compare(canonicalVersion(left), canonicalVersion(right)), nil
}

func IsDevelopmentVersion(version string) bool {
	return dirtyPattern.MatchString(version) || developmentPattern.MatchString(version) || hashVersionPattern.MatchString(version)
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != 1 || m.Distribution != "dunzic/multica-worker" ||
		m.Upstream.Repository != "multica-ai/multica" || !commitPattern.MatchString(m.Upstream.Commit) ||
		!commitPattern.MatchString(m.Upstream.ReleaseCommit) ||
		!validVersion(m.Upstream.Version) || !validVersion(m.MinimumCLIVersion) ||
		len(m.RequiredCLICapabilities) == 0 || m.APIRequirements == nil {
		return fmt.Errorf("missing or invalid distribution compatibility manifest")
	}
	for _, capability := range m.RequiredCLICapabilities {
		if strings.TrimSpace(capability) == "" {
			return fmt.Errorf("empty required CLI capability")
		}
	}
	for _, capability := range m.APIRequirements {
		if capability == "" {
			return fmt.Errorf("empty API capability requirement")
		}
	}
	if semver.Compare(canonicalVersion(m.MinimumCLIVersion), canonicalVersion(m.Upstream.Version)) < 0 {
		return fmt.Errorf("minimum CLI version is below the absorbed upstream baseline")
	}
	return nil
}

func Current() (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return manifest, err
	}
	return manifest, manifest.Validate()
}

func RequireReleaseVersion(version string, manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if !validVersion(version) {
		return fmt.Errorf("invalid semantic version: %q", version)
	}
	if IsDevelopmentVersion(version) {
		return fmt.Errorf("a local development build is not a publishable CLI release")
	}
	if semver.Compare(canonicalVersion(version), canonicalVersion(manifest.MinimumCLIVersion)) < 0 {
		return fmt.Errorf("CLI version %s is below recorded minimum %s (upstream %s)", version, manifest.MinimumCLIVersion, manifest.Upstream.Version)
	}
	return nil
}

func ValidateCLI(report CLIReport, baseline Manifest, expectedVersion string) error {
	if err := baseline.Validate(); err != nil {
		return err
	}
	if !commitPattern.MatchString(report.BuildCommit) {
		return fmt.Errorf("CLI build metadata is missing; install a versioned package from this fork")
	}
	if err := RequireReleaseVersion(report.Version, baseline); err != nil {
		return err
	}
	if err := RequireReleaseVersion(report.Version, report.Distribution); err != nil {
		return err
	}
	if expectedVersion != "" && canonicalVersion(report.Version) != canonicalVersion(expectedVersion) {
		return fmt.Errorf("CLI binary version %s does not match release %s", report.Version, expectedVersion)
	}
	if semver.Compare(canonicalVersion(report.Distribution.Upstream.Version), canonicalVersion(baseline.Upstream.Version)) < 0 {
		return fmt.Errorf("CLI absorbed upstream baseline is older than this client's recorded baseline")
	}
	for _, requirements := range [][]string{baseline.RequiredCLICapabilities, report.Distribution.RequiredCLICapabilities} {
		for _, capability := range requirements {
			if !slices.Contains(report.Capabilities, capability) {
				return fmt.Errorf("CLI is missing required capability: %s", capability)
			}
		}
	}
	return nil
}
