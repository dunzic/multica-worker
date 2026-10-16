package buildinfo

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

func TestSharedSemanticVersionCases(t *testing.T) {
	data, err := os.ReadFile("semver-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Left, Right string
		Result      int
		Invalid     bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Left+"/"+tc.Right, func(t *testing.T) {
			valid := validVersion(tc.Left) && validVersion(tc.Right)
			if tc.Invalid {
				if valid {
					t.Fatal("accepted invalid version")
				}
				return
			}
			if !valid {
				t.Fatal("rejected valid version")
			}
			if got := semver.Compare(canonicalVersion(tc.Left), canonicalVersion(tc.Right)); got != tc.Result {
				t.Fatalf("got %d, want %d", got, tc.Result)
			}
		})
	}
}

func TestCLICompatibility(t *testing.T) {
	baseline, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	newReport := func() CLIReport {
		manifest, _ := Current()
		return CLIReport{Version: baseline.MinimumCLIVersion, BuildCommit: strings.Repeat("a", 40), Distribution: manifest, Capabilities: append([]string(nil), manifest.RequiredCLICapabilities...)}
	}
	if err := ValidateCLI(newReport(), baseline, "v"+baseline.MinimumCLIVersion); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CLIReport){
		"old CLI":                       func(r *CLIReport) { r.Version = "0.1.0" },
		"prerelease below stable floor": func(r *CLIReport) { r.Version += "-rc.1" },
		"missing metadata":              func(r *CLIReport) { r.Distribution = Manifest{} },
		"missing commit":                func(r *CLIReport) { r.BuildCommit = "" },
		"foreign fork":                  func(r *CLIReport) { r.Distribution.Distribution = "multica-ai/multica" },
		"older upstream":                func(r *CLIReport) { r.Distribution.Upstream.Version = "0.1.0" },
		"missing capability despite high version": func(r *CLIReport) { r.Version = "99.0.0"; r.Capabilities = nil },
		"baseline inconsistency":                  func(r *CLIReport) { r.Distribution.MinimumCLIVersion = "0.1.0" },
	} {
		t.Run(name, func(t *testing.T) {
			report := newReport()
			mutate(&report)
			if ValidateCLI(report, baseline, "") == nil {
				t.Fatal("accepted incompatible CLI")
			}
		})
	}
	if ValidateCLI(newReport(), baseline, "v99.0.0") == nil {
		t.Fatal("accepted a mismatched release tag")
	}
}

func TestDevelopmentVersionRejection(t *testing.T) {
	manifest, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"0.0.0-gabcdef", "0.6.3-rc.1-7-gabcdef", "0.6.3-dirty+local.1"} {
		if !IsDevelopmentVersion(version) {
			t.Fatalf("missed development version %s", version)
		}
		if err := RequireReleaseVersion(version, manifest); err == nil || !strings.Contains(err.Error(), "development") {
			t.Fatalf("expected development rejection: %v", err)
		}
	}
	if IsDevelopmentVersion("0.6.3-rc.1") {
		t.Fatal("a prerelease tag is not a git-describe build")
	}
}
