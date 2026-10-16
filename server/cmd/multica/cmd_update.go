package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/buildinfo"
	"github.com/multica-ai/multica/server/internal/cli"
)

var updateDownloadTimeout time.Duration = cli.DefaultUpdateDownloadTimeout

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update multica to the latest version",
	RunE:  runUpdate,
}

func init() {
	updateCmd.Flags().DurationVar(&updateDownloadTimeout, "download-timeout", cli.DefaultUpdateDownloadTimeout, "Maximum time to wait for the release archive download")
}

func runUpdate(_ *cobra.Command, _ []string) error {
	if updateDownloadTimeout <= 0 {
		return fmt.Errorf("download timeout must be greater than zero")
	}
	if _, err := buildinfo.CompareVersions(version, version); err != nil || buildinfo.IsDevelopmentVersion(version) {
		return fmt.Errorf("local development builds cannot install public updates; use a reviewed versioned package")
	}

	fmt.Fprintf(os.Stderr, "Current version: %s (commit: %s, built: %s)\n", version, commit, date)

	// Check the configured release source.
	latest, err := cli.FetchLatestRelease()
	if err != nil {
		return fmt.Errorf("could not check latest version: %w", err)
	}
	baseline, err := buildinfo.Current()
	if err != nil {
		return err
	}
	if err := buildinfo.RequireReleaseVersion(latest.TagName, baseline); err != nil {
		return err
	}
	if !cli.IsReleaseVersion(latest.TagName) {
		return fmt.Errorf("the stable update channel did not return a stable release")
	}
	if !cli.IsNewerVersion(latest.TagName, version) {
		fmt.Fprintln(os.Stderr, "Already up to date or newer than the published release; keeping the installed version.")
		return nil
	}
	fmt.Fprintf(os.Stderr, "Latest version:  %s\n\n", latest.TagName)

	// Detect installation method and update accordingly.
	if cli.IsBrewInstall() {
		fmt.Fprintln(os.Stderr, "Updating via Homebrew...")
		output, err := cli.UpdateViaBrew()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", output)
			return err
		}
		fmt.Fprintln(os.Stderr, "Update complete.")
		return nil
	}

	// Not installed via brew — download the binary from the configured release source.
	targetVersion := latest.TagName
	fmt.Fprintf(os.Stderr, "Downloading %s from the configured release source...\n", targetVersion)
	output, err := cli.UpdateViaDownloadWithTimeout(targetVersion, updateDownloadTimeout)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s\nUpdate complete.\n", output)
	return nil
}
