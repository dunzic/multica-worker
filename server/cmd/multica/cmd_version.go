package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/buildinfo"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
)

func init() {
	versionCmd.Flags().String("output", "text", "Output format: text or json")
	versionCmd.Flags().Bool("check-compatibility", false, "Validate this release against its recorded upstream baseline and capabilities")
	versionCmd.Flags().String("expected-version", "", "Require the binary version to match the downloaded release")
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	RunE:  runVersion,
}

func runVersion(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")
	manifest, err := buildinfo.Current()
	if err != nil {
		return err
	}
	capabilities := append(cli.CapabilityNames(), daemon.AdvertisedCapabilities()...)
	check, _ := cmd.Flags().GetBool("check-compatibility")
	expected, _ := cmd.Flags().GetString("expected-version")
	if check || expected != "" {
		if err := buildinfo.ValidateCLI(buildinfo.CLIReport{
			Version: version, BuildCommit: buildCommit, Distribution: manifest, Capabilities: capabilities,
		}, manifest, expected); err != nil {
			return err
		}
	}

	if output == "json" {
		info := map[string]any{
			"version":      version,
			"commit":       commit,
			"date":         date,
			"go":           runtime.Version(),
			"os":           runtime.GOOS,
			"arch":         runtime.GOARCH,
			"build_commit": buildCommit,
			"distribution": manifest,
			"capabilities": capabilities,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}

	fmt.Printf("multica %s (commit: %s, built: %s)\n", version, commit, date)
	fmt.Printf("go: %s, os/arch: %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return nil
}
