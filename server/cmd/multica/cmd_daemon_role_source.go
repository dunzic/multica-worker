package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
)

const maxRoleSourceManagedInputBytes = 1 << 20

var daemonRoleSourceCmd = &cobra.Command{
	Use:   "role-source",
	Short: "Manage local role source adapters",
	Long: "Manage the daemon's private role source configuration with revision checks and atomic updates. " +
		"The CLI never prints or accepts the private environment digest key.",
}

var daemonRoleSourceShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show a redacted local role source configuration summary",
	Args:  exactArgs(0),
	RunE:  runDaemonRoleSourceShow,
}

var daemonRoleSourceApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Atomically apply a secret-free role source desired-state document",
	Long: "Apply a complete desired-state JSON document containing version, allowed_roots, and sources. " +
		"Use --expected-revision absent for first creation; for updates, use the revision returned by show. " +
		"Adapter config is read from a private file or stdin and is never accepted directly in command arguments.",
	Args: exactArgs(0),
	RunE: runDaemonRoleSourceApply,
}

var daemonRoleSourceRotateKeyCmd = &cobra.Command{
	Use:   "rotate-key",
	Short: "Rotate the private AgentWaker digest key",
	Long: "Rotate the local HMAC key used for AgentWaker environment-value evidence. " +
		"This changes evidence digests and requires rescanning and reviewing every AgentWaker source.",
	Args: exactArgs(0),
	RunE: runDaemonRoleSourceRotateKey,
}

func init() {
	daemonRoleSourceCmd.PersistentFlags().String("config-file", "", "Private config path (default: profile role-sources.json; env: MULTICA_ROLE_SOURCE_CONFIG_FILE)")
	daemonRoleSourceShowCmd.Flags().String("output", "table", "Output format: table or json")
	daemonRoleSourceApplyCmd.Flags().String("document", "-", "Secret-free desired-state JSON file, or - for stdin")
	daemonRoleSourceApplyCmd.Flags().String("expected-revision", "", "Required CAS revision from show, or absent for first creation")
	daemonRoleSourceApplyCmd.Flags().String("output", "table", "Output format: table or json")
	daemonRoleSourceRotateKeyCmd.Flags().String("expected-revision", "", "Required CAS revision from show")
	daemonRoleSourceRotateKeyCmd.Flags().Bool("confirm-rescan", false, "Confirm every AgentWaker source will be rescanned and reviewed")
	daemonRoleSourceRotateKeyCmd.Flags().String("output", "table", "Output format: table or json")

	daemonRoleSourceCmd.AddCommand(daemonRoleSourceShowCmd)
	daemonRoleSourceCmd.AddCommand(daemonRoleSourceApplyCmd)
	daemonRoleSourceCmd.AddCommand(daemonRoleSourceRotateKeyCmd)
	daemonCmd.AddCommand(daemonRoleSourceCmd)
}

func runDaemonRoleSourceShow(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("daemon role-source show"); err != nil {
		return err
	}
	configPath, err := resolveRoleSourceManagedConfigPath(cmd)
	if err != nil {
		return err
	}
	summary, err := daemon.ShowRoleSourceConfig(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("role source config does not exist; create it with 'multica daemon role-source apply --expected-revision absent'")
		}
		return err
	}
	return printRoleSourceConfigSummary(cmd, summary)
}

func runDaemonRoleSourceApply(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("daemon role-source apply"); err != nil {
		return err
	}
	configPath, err := resolveRoleSourceManagedConfigPath(cmd)
	if err != nil {
		return err
	}
	expected, _ := cmd.Flags().GetString("expected-revision")
	if strings.TrimSpace(expected) == "" {
		return errors.New("--expected-revision is required; use absent for first creation or the revision returned by show")
	}
	documentPath, _ := cmd.Flags().GetString("document")
	body, err := readRoleSourceManagedInput(cmd.InOrStdin(), documentPath)
	if err != nil {
		return err
	}
	defer clear(body)
	summary, err := daemon.ApplyRoleSourceManagedConfig(configPath, expected, body)
	if err != nil {
		return err
	}
	return printRoleSourceConfigSummary(cmd, summary)
}

func runDaemonRoleSourceRotateKey(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("daemon role-source rotate-key"); err != nil {
		return err
	}
	confirmed, _ := cmd.Flags().GetBool("confirm-rescan")
	if !confirmed {
		return errors.New("--confirm-rescan is required because rotation changes AgentWaker evidence digests")
	}
	expected, _ := cmd.Flags().GetString("expected-revision")
	if strings.TrimSpace(expected) == "" {
		return errors.New("--expected-revision is required and must come from role-source show")
	}
	configPath, err := resolveRoleSourceManagedConfigPath(cmd)
	if err != nil {
		return err
	}
	summary, err := daemon.RotateRoleSourceDigestKey(configPath, expected)
	if err != nil {
		return err
	}
	return printRoleSourceConfigSummary(cmd, summary)
}

func resolveRoleSourceManagedConfigPath(cmd *cobra.Command) (string, error) {
	configured, _ := cmd.Flags().GetString("config-file")
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = strings.TrimSpace(os.Getenv("MULTICA_ROLE_SOURCE_CONFIG_FILE"))
	}
	if configured == "" {
		var err error
		configured, err = daemon.DefaultRoleSourceConfigPath(resolveProfile(cmd))
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(configured) {
		return "", errors.New("role source config path must be absolute")
	}
	return filepath.Clean(configured), nil
}

func readRoleSourceManagedInput(stdin io.Reader, documentPath string) ([]byte, error) {
	documentPath = strings.TrimSpace(documentPath)
	if documentPath == "" || documentPath == "-" {
		body, err := io.ReadAll(io.LimitReader(stdin, maxRoleSourceManagedInputBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read managed role source document from stdin: %w", err)
		}
		if len(body) == 0 || len(body) > maxRoleSourceManagedInputBytes {
			clear(body)
			return nil, errors.New("managed role source document from stdin is empty or exceeds 1 MiB")
		}
		return body, nil
	}
	absolute, err := filepath.Abs(documentPath)
	if err != nil {
		return nil, fmt.Errorf("resolve managed role source document path: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect managed role source document: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("managed role source document must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("managed role source document permissions must be 0600 or stricter")
	}
	if info.Size() <= 0 || info.Size() > maxRoleSourceManagedInputBytes {
		return nil, errors.New("managed role source document is empty or exceeds 1 MiB")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("open managed role source document: %w", err)
	}
	defer file.Close() //nolint:errcheck
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened managed role source document: %w", err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("managed role source document changed during secure open")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxRoleSourceManagedInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read managed role source document: %w", err)
	}
	if len(body) > maxRoleSourceManagedInputBytes {
		clear(body)
		return nil, errors.New("managed role source document exceeds 1 MiB")
	}
	return body, nil
}

func printRoleSourceConfigSummary(cmd *cobra.Command, summary daemon.RoleSourceConfigSummary) error {
	output, _ := cmd.Flags().GetString("output")
	switch output {
	case "json":
		return cli.PrintJSON(cmd.OutOrStdout(), summary)
	case "table", "":
	default:
		return fmt.Errorf("invalid --output %q: must be table or json", output)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Revision:          %s\n", summary.Revision)
	fmt.Fprintf(w, "Config file:       %s\n", safeRoleSourceSummaryValue(summary.ConfigFileName))
	fmt.Fprintf(w, "Version:           %d\n", summary.Version)
	fmt.Fprintf(w, "Sources:           %d\n", summary.SourceCount)
	rootNames := make([]string, 0, len(summary.AllowedRootNames))
	for _, rootName := range summary.AllowedRootNames {
		rootNames = append(rootNames, safeRoleSourceSummaryValue(rootName))
	}
	fmt.Fprintf(w, "Allowed root names: %s\n", strings.Join(rootNames, ", "))
	fmt.Fprintf(w, "Digest key active: %t\n", summary.DigestKeyActive)
	fmt.Fprintf(w, "Validation:        %s", summary.ValidationStatus)
	if summary.ValidationCode != "" {
		fmt.Fprintf(w, " (%s)", summary.ValidationCode)
	}
	fmt.Fprintln(w)
	if summary.RescanRequired {
		fmt.Fprintln(w, "Rescan required:   true")
	}
	if len(summary.Sources) == 0 {
		return nil
	}
	fmt.Fprintln(w, "\nID\tKIND\tSUMMARY")
	for _, source := range summary.Sources {
		attributes := make([]string, 0, len(source.Attributes))
		for _, attribute := range source.Attributes {
			attributes = append(attributes, safeRoleSourceSummaryValue(attribute.Name)+"="+safeRoleSourceSummaryValue(attribute.Value))
		}
		sort.Strings(attributes)
		fmt.Fprintf(w, "%s\t%s\t%s\n", source.ID, source.Kind, strings.Join(attributes, ", "))
	}
	return nil
}

func safeRoleSourceSummaryValue(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '\uFFFD'
		}
		return r
	}, value)
	const maxRunes = 128
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return value
}
