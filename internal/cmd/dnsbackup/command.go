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

var createCmd = &cobra.Command{
	Use:   "create [zone]",
	Short: "Create and upload DNS backup for a zone",
	Long: `Create a DNS backup by exporting records and uploading to Minio storage.
Optionally also upload to AWS Glacier for long-term archival.`,
	Args: cobra.ExactArgs(1),
	RunE: runCreate,
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List DNS backups in Minio",
	Args:  cobra.NoArgs,
	RunE:  runList,
}

var readCmd = &cobra.Command{
	Use:   "read [object]",
	Short: "Read/download a DNS backup from Minio",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runRead,
}

var deleteCmd = &cobra.Command{
	Use:   "delete [object...]",
	Short: "Delete DNS backup(s) from Minio",
	Args:  cobra.ArbitraryArgs,
	RunE:  runDelete,
}

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Monitor DNS backup storage and migrate to AWS Glacier",
	Long: `Monitor DNS backup storage and automatically migrate oldest backups to AWS Glacier
when storage needs to be freed up.`,
	Args: cobra.NoArgs,
	RunE: runMonitor,
}

var migrateAWSCmd = &cobra.Command{
	Use:   "migrate-aws",
	Short: "Manually migrate DNS backups to AWS Glacier",
	Long: `Manually trigger migration of DNS backups from Minio to AWS Glacier.
This provides fine-grained control over backup archival.`,
	Args: cobra.NoArgs,
	RunE: runMigrateAWS,
}

var testMinioCmd = &cobra.Command{
	Use:   "test-minio",
	Short: "Test Minio connection for DNS backups",
	Args:  cobra.NoArgs,
	RunE:  runTestMinio,
}

var testAWSCmd = &cobra.Command{
	Use:   "test-aws",
	Short: "Test AWS Glacier connection for DNS backups",
	Args:  cobra.NoArgs,
	RunE:  runTestAWS,
}

var connCmd = &cobra.Command{
	Use:   "conn",
	Short: "Test connections to both Minio and AWS Glacier",
	Args:  cobra.NoArgs,
	RunE:  runConn,
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
	Cmd.AddCommand(createCmd)
	Cmd.AddCommand(listCmd)
	Cmd.AddCommand(readCmd)
	Cmd.AddCommand(deleteCmd)
	Cmd.AddCommand(monitorCmd)
	Cmd.AddCommand(migrateAWSCmd)
	Cmd.AddCommand(testMinioCmd)
	Cmd.AddCommand(testAWSCmd)
	Cmd.AddCommand(connCmd)
	Cmd.AddCommand(planCmd)
	Cmd.AddCommand(applyCmd)
	Cmd.AddCommand(testCmd)

	initExportFlags()
	initCreateFlags()
	initListFlags()
	initReadFlags()
	initDeleteFlags()
	initMonitorFlags()
	initMigrateAWSFlags()
	initTestMinioFlags()
	initTestAWSFlags()
	initConnFlags()
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

func defaultDNSBucket() string {
	bucket := os.Getenv("MINIO_DNS_BUCKET")
	if bucket != "" {
		return bucket
	}
	return getEnvWithDefault("MINIO_BUCKET", "backups")
}

func initCreateFlags() {
	createCmd.Flags().String("format", "json", "Backup format (json or yaml)")
	createCmd.Flags().Bool("upload-minio", true, "Upload backup to Minio")
	createCmd.Flags().Bool("upload-glacier", false, "Also upload backup to AWS Glacier")

	// Minio flags
	createCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	createCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	createCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	createCmd.Flags().String("minio-bucket", defaultDNSBucket(), "Minio bucket (env: MINIO_BUCKET, overrides with MINIO_DNS_BUCKET)")
	createCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio (env: MINIO_SSL)")
	createCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix in bucket (env: MINIO_BUCKET_PATH)")
	createCmd.Flags().Duration("minio-http-timeout", 0, "Minio HTTP timeout (env: MINIO_HTTP_TIMEOUT)")

	// AWS flags
	createCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault (env: AWS_VAULT)")
	createCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID (env: AWS_ACCOUNT_ID)")
	createCmd.Flags().String("aws-access-key", getEnvWithDefault("AWS_ACCESS_KEY", ""), "AWS access key (env: AWS_ACCESS_KEY)")
	createCmd.Flags().String("aws-secret-access-key", getEnvWithDefault("AWS_SECRET_ACCESS_KEY", ""), "AWS secret key (env: AWS_SECRET_ACCESS_KEY)")
	createCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION)")
	createCmd.Flags().Duration("aws-http-timeout", 0, "AWS HTTP timeout (env: AWS_HTTP_TIMEOUT)")
}

func initListFlags() {
	listCmd.Flags().String("prefix", "", "Filter by prefix")
	listCmd.Flags().Int("limit", 100, "Maximum number of backups to list")
	listCmd.Flags().Bool("json", false, "Output as JSON")

	// Minio flags
	listCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	listCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	listCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	listCmd.Flags().String("minio-bucket", defaultDNSBucket(), "Minio bucket (env: MINIO_BUCKET, overrides with MINIO_DNS_BUCKET)")
	listCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio (env: MINIO_SSL)")
	listCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix in bucket (env: MINIO_BUCKET_PATH)")
	listCmd.Flags().Duration("minio-http-timeout", 0, "Minio HTTP timeout (env: MINIO_HTTP_TIMEOUT)")
}

func initReadFlags() {
	readCmd.Flags().String("output", "", "Output file path")
	readCmd.Flags().String("format", "json", "Output format (json or yaml)")
	readCmd.Flags().Bool("latest", false, "Read most recent backup")
	readCmd.Flags().String("prefix", "", "Prefix to search when using --latest")

	// Minio flags
	readCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	readCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	readCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	readCmd.Flags().String("minio-bucket", defaultDNSBucket(), "Minio bucket (env: MINIO_BUCKET, overrides with MINIO_DNS_BUCKET)")
	readCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio (env: MINIO_SSL)")
	readCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix in bucket (env: MINIO_BUCKET_PATH)")
	readCmd.Flags().Duration("minio-http-timeout", 0, "Minio HTTP timeout (env: MINIO_HTTP_TIMEOUT)")
}

func initDeleteFlags() {
	deleteCmd.Flags().Bool("dry-run", false, "Preview deletions without performing them")
	deleteCmd.Flags().Bool("delete-all", false, "Delete all backups (respects --prefix)")
	deleteCmd.Flags().String("prefix", "", "Filter by prefix")

	// Minio flags
	deleteCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	deleteCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	deleteCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	deleteCmd.Flags().String("minio-bucket", defaultDNSBucket(), "Minio bucket (env: MINIO_BUCKET, overrides with MINIO_DNS_BUCKET)")
	deleteCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio (env: MINIO_SSL)")
	deleteCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix in bucket (env: MINIO_BUCKET_PATH)")
	deleteCmd.Flags().Duration("minio-http-timeout", 0, "Minio HTTP timeout (env: MINIO_HTTP_TIMEOUT)")
}

func initMonitorFlags() {
	monitorCmd.Flags().Float64("migrate-percent", 10.0, "Percentage of oldest backups to migrate")
	monitorCmd.Flags().Bool("dry-run", false, "Preview migrations without performing them")

	// Minio flags
	monitorCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	monitorCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	monitorCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	monitorCmd.Flags().String("minio-bucket", defaultDNSBucket(), "Minio bucket (env: MINIO_BUCKET, overrides with MINIO_DNS_BUCKET)")
	monitorCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio (env: MINIO_SSL)")
	monitorCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix in bucket (env: MINIO_BUCKET_PATH)")
	monitorCmd.Flags().Duration("minio-http-timeout", 0, "Minio HTTP timeout (env: MINIO_HTTP_TIMEOUT)")

	// AWS flags
	monitorCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault (env: AWS_VAULT)")
	monitorCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID (env: AWS_ACCOUNT_ID)")
	monitorCmd.Flags().String("aws-access-key", getEnvWithDefault("AWS_ACCESS_KEY", ""), "AWS access key (env: AWS_ACCESS_KEY)")
	monitorCmd.Flags().String("aws-secret-access-key", getEnvWithDefault("AWS_SECRET_ACCESS_KEY", ""), "AWS secret key (env: AWS_SECRET_ACCESS_KEY)")
	monitorCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION)")
	monitorCmd.Flags().Duration("aws-http-timeout", 0, "AWS HTTP timeout (env: AWS_HTTP_TIMEOUT)")
}

func initMigrateAWSFlags() {
	migrateAWSCmd.Flags().Float64("percent", 10.0, "Percentage of oldest backups to migrate")
	migrateAWSCmd.Flags().Bool("dry-run", false, "Preview migrations without performing them")

	// Minio flags
	migrateAWSCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	migrateAWSCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	migrateAWSCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	migrateAWSCmd.Flags().String("minio-bucket", defaultDNSBucket(), "Minio bucket (env: MINIO_BUCKET, overrides with MINIO_DNS_BUCKET)")
	migrateAWSCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio (env: MINIO_SSL)")
	migrateAWSCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix in bucket (env: MINIO_BUCKET_PATH)")
	migrateAWSCmd.Flags().Duration("minio-http-timeout", 0, "Minio HTTP timeout (env: MINIO_HTTP_TIMEOUT)")

	// AWS flags
	migrateAWSCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault (env: AWS_VAULT)")
	migrateAWSCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID (env: AWS_ACCOUNT_ID)")
	migrateAWSCmd.Flags().String("aws-access-key", getEnvWithDefault("AWS_ACCESS_KEY", ""), "AWS access key (env: AWS_ACCESS_KEY)")
	migrateAWSCmd.Flags().String("aws-secret-access-key", getEnvWithDefault("AWS_SECRET_ACCESS_KEY", ""), "AWS secret key (env: AWS_SECRET_ACCESS_KEY)")
	migrateAWSCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION)")
	migrateAWSCmd.Flags().Duration("aws-http-timeout", 0, "AWS HTTP timeout (env: AWS_HTTP_TIMEOUT)")
}

func initTestMinioFlags() {
	testMinioCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	testMinioCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	testMinioCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	testMinioCmd.Flags().String("minio-bucket", defaultDNSBucket(), "Minio bucket (env: MINIO_BUCKET, overrides with MINIO_DNS_BUCKET)")
	testMinioCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio (env: MINIO_SSL)")
	testMinioCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix in bucket (env: MINIO_BUCKET_PATH)")
	testMinioCmd.Flags().Duration("minio-http-timeout", 0, "Minio HTTP timeout (env: MINIO_HTTP_TIMEOUT)")
}

func initTestAWSFlags() {
	testAWSCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault (env: AWS_VAULT)")
	testAWSCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID (env: AWS_ACCOUNT_ID)")
	testAWSCmd.Flags().String("aws-access-key", getEnvWithDefault("AWS_ACCESS_KEY", ""), "AWS access key (env: AWS_ACCESS_KEY)")
	testAWSCmd.Flags().String("aws-secret-access-key", getEnvWithDefault("AWS_SECRET_ACCESS_KEY", ""), "AWS secret key (env: AWS_SECRET_ACCESS_KEY)")
	testAWSCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION)")
	testAWSCmd.Flags().Duration("aws-http-timeout", 0, "AWS HTTP timeout (env: AWS_HTTP_TIMEOUT)")
}

func initConnFlags() {
	connCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	connCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	connCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	connCmd.Flags().String("minio-bucket", defaultDNSBucket(), "Minio bucket (env: MINIO_BUCKET, overrides with MINIO_DNS_BUCKET)")
	connCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio (env: MINIO_SSL)")
	connCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix in bucket (env: MINIO_BUCKET_PATH)")
	connCmd.Flags().Duration("minio-http-timeout", 0, "Minio HTTP timeout (env: MINIO_HTTP_TIMEOUT)")

	connCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault (env: AWS_VAULT)")
	connCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID (env: AWS_ACCOUNT_ID)")
	connCmd.Flags().String("aws-access-key", getEnvWithDefault("AWS_ACCESS_KEY", ""), "AWS access key (env: AWS_ACCESS_KEY)")
	connCmd.Flags().String("aws-secret-access-key", getEnvWithDefault("AWS_SECRET_ACCESS_KEY", ""), "AWS secret key (env: AWS_SECRET_ACCESS_KEY)")
	connCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION)")
	connCmd.Flags().Duration("aws-http-timeout", 0, "AWS HTTP timeout (env: AWS_HTTP_TIMEOUT)")
}
