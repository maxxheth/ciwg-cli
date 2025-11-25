package dnsbackup

import (
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "dns-backup",
	Short: "Backup and restore Cloudflare DNS records",
	Long: `Manage Cloudflare DNS zone backups.

Use 'export' to capture DNS records, 'plan' to preview a restore, and 'apply' to perform the changes
(with --dry-run for safety).`,
}

var exportCmd = &cobra.Command{
	Use:   "export [zone]",
	Short: "Export DNS records for a zone",
	Args:  cobra.ExactArgs(1),
	RunE:  runExport,
}

var planCmd = &cobra.Command{
	Use:   "plan [zone]",
	Short: "Generate a change plan for a zone and backup snapshot",
	Args:  cobra.ExactArgs(1),
	RunE:  runPlan,
}

var applyCmd = &cobra.Command{
	Use:   "apply [zone]",
	Short: "Apply a snapshot to a zone (supports dry-run)",
	Args:  cobra.ExactArgs(1),
	RunE:  runApply,
}

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Test Cloudflare token connectivity",
	Args:  cobra.NoArgs,
	RunE:  runTest,
}

func init() {
	const projectEnv = "/usr/local/bin/ciwg-cli-utils/.env"
	if err := godotenv.Load(projectEnv); err == nil {
		// nothing else to do
	} else {
		if envPath := findEnvArg(os.Args); envPath != "" {
			_ = godotenv.Load(envPath)
		} else {
			_ = godotenv.Load()
		}
	}

	Cmd.PersistentFlags().String("env", "", "Path to .env file to load before executing")
	Cmd.PersistentFlags().String("token", getEnvWithDefault("CLOUDFLARE_DNS_BACKUP_TOKEN", ""), "Cloudflare API token (env: CLOUDFLARE_DNS_BACKUP_TOKEN)")
	Cmd.PersistentFlags().Duration("timeout", 30*time.Second, "Per-call timeout when talking to Cloudflare")

	Cmd.AddCommand(exportCmd)
	Cmd.AddCommand(planCmd)
	Cmd.AddCommand(applyCmd)
	Cmd.AddCommand(testCmd)

	initExportFlags()
	initPlanFlags()
	initApplyFlags()
	initTestFlags()
}

func initExportFlags() {
	exportCmd.Flags().String("output", "", "File to write the snapshot to (default: stdout)")
	exportCmd.Flags().String("format", "json", "Snapshot format: json or yaml")
	exportCmd.Flags().Bool("pretty", true, "Pretty-print JSON/YAML output")
	exportCmd.Flags().StringSlice("metadata", nil, "Optional metadata key=value pairs to include in snapshot")
}

func initPlanFlags() {
	planCmd.Flags().String("snapshot", "", "Path to the snapshot file (json or yaml)")
	planCmd.Flags().String("snapshot-format", "", "Snapshot format override (json|yaml)")
	planCmd.Flags().Bool("delete-missing", getEnvBoolWithDefault("DNS_BACKUP_DELETE_MISSING", false), "Mark records not present in the snapshot for deletion")
	planCmd.Flags().String("output", "", "Optional file to write the plan to")
	planCmd.Flags().String("format", "json", "Plan output format (json|yaml)")
	planCmd.Flags().Bool("pretty", true, "Pretty-print the plan output")
	planCmd.Flags().Bool("print-plan", false, "Write the full plan to stdout")
}

func initApplyFlags() {
	applyCmd.Flags().String("snapshot", "", "Path to the snapshot file (json or yaml)")
	applyCmd.Flags().String("snapshot-format", "", "Snapshot format override (json|yaml)")
	applyCmd.Flags().Bool("delete-missing", getEnvBoolWithDefault("DNS_BACKUP_DELETE_MISSING", false), "Delete DNS records that are absent from the snapshot")
	applyCmd.Flags().Bool("dry-run", true, "Preview changes without applying them")
	applyCmd.Flags().Bool("print-plan", false, "Display the full plan when running")
	applyCmd.Flags().String("plan-output", "", "Write the computed plan to this file")
	applyCmd.Flags().String("plan-format", "json", "Plan serialization format (json|yaml)")
	applyCmd.Flags().Bool("plan-pretty", true, "Pretty-print plan output")
	applyCmd.Flags().Bool("yes", false, "Apply changes without prompting (required when not using --dry-run)")
}

func initTestFlags() {}
