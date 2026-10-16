package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/buildinfo"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
)

func TestVersionCompatibilityReport(t *testing.T) {
	oldVersion, oldCommit := version, buildCommit
	t.Cleanup(func() { version, buildCommit = oldVersion, oldCommit })
	manifest, err := buildinfo.Current()
	if err != nil {
		t.Fatal(err)
	}
	version, buildCommit = manifest.MinimumCLIVersion, strings.Repeat("a", 40)
	cmd := &cobra.Command{}
	cmd.Flags().String("output", "json", "")
	cmd.Flags().Bool("check-compatibility", true, "")
	cmd.Flags().String("expected-version", "v"+version, "")
	output, err := captureStdout(t, func() error { return runVersion(cmd, nil) })
	if err != nil {
		t.Fatal(err)
	}
	var report buildinfo.CLIReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if err := buildinfo.ValidateCLI(report, manifest, version); err != nil {
		t.Fatal(err)
	}
	want := append(cli.CapabilityNames(), daemon.AdvertisedCapabilities()...)
	if !slices.Equal(report.Capabilities, want) {
		t.Fatal("metadata must reflect implemented capabilities, not the required list")
	}
	if err := cmd.Flags().Set("expected-version", "99.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := runVersion(cmd, nil); err == nil {
		t.Fatal("expected-version must fail closed")
	}
}
