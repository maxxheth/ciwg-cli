package backup

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"ciwg-cli/internal/backup"
)

// BackupCmd is the main backup command exported for use by the root command
var BackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Backup management for WordPress containers",
	Long:  `Create and manage backups of WordPress containers, streaming them to Minio storage.`,
}

var backupCreateCmd = &cobra.Command{
	Use:   "create [hostname]",
	Short: "Create backups of WordPress containers",
	Long: `Create backups of WordPress containers and stream them to Minio storage.

Dry-run mode supports three compression estimation methods:
  - heuristic: Instant estimation based on file types (~80% accurate)
  - sample: Compress a sample and extrapolate (~90% accurate, uses --sample-size)
  - accurate: Full compression simulation (100% accurate, same speed as real backup)

Examples:
  # Standard backup
  ciwg-cli backup create wp0.example.com

  # Dry-run with instant estimation
  ciwg-cli backup create wp0.example.com --dry-run --estimate-method heuristic

  # Dry-run with sample-based estimation (compress first 100MB)
  ciwg-cli backup create wp0.example.com --dry-run --estimate-method sample

  # Dry-run with accurate estimation (full compression)
  ciwg-cli backup create wp0.example.com --dry-run --estimate-method accurate

  # Dry-run with larger sample size (200MB)
  ciwg-cli backup create wp0.example.com --dry-run --estimate-method sample --sample-size 209715200`,
	Args: cobra.MaximumNArgs(1),
	RunE: runBackupCreate,
}

var backupTestMinioCmd = &cobra.Command{
	Use:   "test-minio",
	Short: "Test Minio connection and perform read/write test",
	Long:  `Test the connection to Minio storage and perform a basic read/write test to verify bucket access.`,
	RunE:  runTestMinio,
}

var backupTestAWSCmd = &cobra.Command{
	Use:   "test-aws",
	Short: "Test AWS Glacier connection and perform read/write test",
	Long:  `Test the connection to AWS Glacier storage and perform a basic read/write test to verify vault access.`,
	RunE:  runTestAWS,
}

var backupReadCmd = &cobra.Command{
	Use:   "read [object]",
	Short: "Read/download a backup object from Minio",
	Long:  `Download or stream a backup object from Minio. If --output is not specified the object is written to stdout.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupRead,
}

var backupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List backup objects in Minio",
	Long:  `List objects in the configured Minio bucket, optionally filtered by prefix.`,
	Args:  cobra.NoArgs,
	RunE:  runBackupList,
}

var backupDeleteCmd = &cobra.Command{
	Use:   "delete [object]",
	Short: "Delete backup object(s) from Minio",
	Long: `Delete one or more backup objects from the configured Minio bucket.

Deletion modes (mutually exclusive):
  - Single object: Pass object key as argument
  - By prefix: Use --prefix to delete multiple objects matching a prefix
  - Latest: Use --latest with --prefix to delete only the most recent match
  - Delete all: Use --delete-all to delete all objects (respects --prefix)
  - Numeric range: Use --delete-range "1-10" to delete the 1st through 10th most recent backups
  - Date range: Use --delete-range-by-date "YYYYMMDD-YYYYMMDD" or "YYYYMMDD:HHMMSS-YYYYMMDD:HHMMSS"

Examples:
  # Delete a specific backup
  ciwg-cli backup delete backups/site-20240101-120000.tgz

  # Delete all backups for a site (with confirmation)
  ciwg-cli backup delete --prefix backups/mysite.com- --delete-all

  # Delete the 5 oldest backups for a site
  ciwg-cli backup delete --prefix backups/mysite.com- --delete-range 1-5

  # Delete backups from January 2024
  ciwg-cli backup delete --prefix backups/mysite.com- --delete-range-by-date 20240101-20240131

  # Dry run to preview deletions
  ciwg-cli backup delete --prefix backups/mysite.com- --delete-all --dry-run`,
	Args: cobra.MaximumNArgs(1),
	RunE: runBackupDelete,
}

var backupMonitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Monitor storage capacity and auto-migrate backups to AWS Glacier",
	Long: `Monitor the storage capacity of the Minio storage server and automatically migrate
the oldest backups to AWS Glacier when usage exceeds a threshold.

This command can be run via cron to maintain storage capacity. When capacity exceeds
the threshold (default 95%), it will:
	1. Select the oldest N% of backups (default 10%)
	2. Upload them to AWS Glacier
	3. Delete them from Minio
	4. Repeat until capacity falls below threshold
	5. If --force-delete is specified, delete the oldest backups without migrating
		 whenever the Glacier upload step fails (last-resort backpressure relief)

Example:
  # Monitor and migrate if capacity exceeds 95%
  ciwg-cli backup monitor

  # Use custom threshold and migration percentage
  ciwg-cli backup monitor --threshold 90 --migrate-percent 15

  # Use specific storage path
  ciwg-cli backup monitor --storage-path /mnt/minio-data`,
	Args: cobra.NoArgs,
	RunE: runBackupMonitor,
}

var backupConnCmd = &cobra.Command{
	Use:   "conn",
	Short: "Test connections to both Minio and AWS Glacier",
	Long: `Test connectivity and perform read/write tests for both Minio storage and AWS Glacier.
This is a convenience command that tests both services at once.

Example:
  # Test both connections
  ciwg-cli backup conn

  # Test with custom configurations
  ciwg-cli backup conn --minio-endpoint minio.example.com:9000 --aws-vault my-vault`,
	Args: cobra.NoArgs,
	RunE: runBackupConn,
}

var backupSanitizeCmd = &cobra.Command{
	Use:   "sanitize",
	Short: "Sanitize a backup by extracting specific content and removing sensitive data",
	Long: `Sanitize a backup tarball by extracting specific directories and files, 
and removing license keys from SQL files. This creates a backup suitable for 
sharing with clients without sensitive or proprietary data.

By default:
  - Extracts wp-content directory from the tarball
  - Extracts all SQL files
  - Removes license keys from MySQL/MariaDB databases

Examples:
  # Sanitize a backup with default settings
  ciwg-cli backup sanitize --input backup.tgz --output sanitized.tgz

  # Extract custom directory
  ciwg-cli backup sanitize --input backup.tgz --output clean.tgz --extract-dir "app/public"

  # Extract multiple directories
  ciwg-cli backup sanitize --input backup.tgz --output clean.tgz --extract-dir "wp-content,uploads"

  # Custom SQL file pattern
  ciwg-cli backup sanitize --input backup.tgz --output clean.tgz --extract-file "*.sql,*.dump"

  # Dry run to preview what would be extracted
  ciwg-cli backup sanitize --input backup.tgz --output clean.tgz --dry-run`,
	Args: cobra.NoArgs,
	RunE: runBackupSanitize,
}

var backupMigrateAWSCmd = &cobra.Command{
	Use:   "migrate-aws",
	Short: "Manually migrate backups from Minio to AWS Glacier",
	Long: `Manually trigger migration of backups from Minio storage to AWS Glacier.
This command gives you fine-grained control over which backups to migrate and
provides detailed diagnostic output to troubleshoot migration issues.

Migration strategies:
  - Specific object: Use --object to migrate a specific backup by its exact key
  - Oldest N backups: Use --count to migrate the N oldest backups
  - By prefix: Use --prefix to migrate backups for specific sites
  - By age: Use --older-than to migrate backups older than a duration
  - Percentage: Use --percent to migrate oldest N% of all backups

Examples:
  # Migrate a specific backup object
  ciwg-cli backup migrate-aws --object backups/mysite.com/mysite.com-20241112-120000.tgz -vv

  # Migrate 10 oldest backups with debug output
  ciwg-cli backup migrate-aws --count 10 -vv

  # Migrate backups for a specific site
  ciwg-cli backup migrate-aws --prefix backups/mysite.com/ --count 5 -vv

  # Migrate backups older than 30 days
  ciwg-cli backup migrate-aws --older-than 720h -vvv

  # Migrate oldest 10% of all backups
  ciwg-cli backup migrate-aws --percent 10 -vv

  # Dry run to preview what would be migrated
  ciwg-cli backup migrate-aws --count 10 --dry-run -v

  # Delete from Minio after successful migration
  ciwg-cli backup migrate-aws --count 5 --delete-after -vv`,
	Args: cobra.NoArgs,
	RunE: runBackupMigrateAWS,
}

var backupEstimateCapacityCmd = &cobra.Command{
	Use:   "estimate-capacity [hostname]",
	Short: "Estimate storage capacity requirements for backup schedules",
	Long: `Estimate storage capacity requirements based on retention policies and backup schedules.
Supports fleet-wide calculations, per-site analysis, growth modeling, and cost estimates.

This command helps you plan storage capacity by:
  - Scanning live sites or using existing backups as baselines
  - Applying retention policies (daily/weekly/monthly)
  - Calculating hot storage (Minio) and cold storage (AWS Glacier) requirements
  - Projecting growth over time
  - Estimating AWS Glacier storage costs

Examples:
  # Scan a single server with default retention (14 daily, 26 weekly, 6 monthly)
  ciwg-cli backup estimate-capacity wp0.ciwgserver.com --estimate-method sample

  # Scan entire fleet with server range
  ciwg-cli backup estimate-capacity --server-range "wp%d.ciwgserver.com:0-41" --estimate-method heuristic

  # Use existing backup as baseline
  ciwg-cli backup estimate-capacity --from-backup backups/mysite.com/backup.tgz

  # Manual specification for 42 sites
  ciwg-cli backup estimate-capacity --avg-compressed-size 125MB --site-count 42

  # Custom retention policy
  ciwg-cli backup estimate-capacity wp0.ciwgserver.com \
    --daily-retention 7 --weekly-retention 12 --monthly-retention 12

  # Include growth projections
  ciwg-cli backup estimate-capacity wp0.ciwgserver.com \
    --estimate-focus all --growth-rate 5 --projection-months 12

  # Cost estimates only
  ciwg-cli backup estimate-capacity wp0.ciwgserver.com \
    --estimate-type cost --aws-glacier-price 0.004

  # Export to JSON
  ciwg-cli backup estimate-capacity --server-range "wp%d.ciwgserver.com:0-41" \
    --output json > capacity-report.json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runBackupEstimateCapacity,
}

func init() {
	// Load .env early so getEnvWithDefault calls used during flag setup
	// will see values from a local .env file in development.
	// If the user passed --env on the command line, pre-load that file
	// from os.Args before flags are registered so defaults derived
	// from environment variables will reflect the chosen file.
	// Prefer a project-level .env at a known path. If that isn't present,
	// fall back to an explicit --env passed on the command line. If neither
	// are available, call godotenv.Load() which will attempt to load a .env
	// from the current working directory.
	const projectEnv = "/usr/local/bin/ciwg-cli-utils/.env"
	if err := godotenv.Load(projectEnv); err == nil {
		// loaded project-level .env successfully
	} else {
		if envPath := findEnvArg(os.Args); envPath != "" {
			_ = godotenv.Load(envPath)
		} else {
			_ = godotenv.Load()
		}
	}

	// Allow explicit env file via --env on the backup command and subcommands
	BackupCmd.PersistentFlags().String("env", "", "Path to .env file to load (overrides defaults)")
	BackupCmd.AddCommand(backupCreateCmd)
	BackupCmd.AddCommand(backupTestMinioCmd)
	BackupCmd.AddCommand(backupTestAWSCmd)
	BackupCmd.AddCommand(backupReadCmd)
	BackupCmd.AddCommand(backupListCmd)
	BackupCmd.AddCommand(backupMonitorCmd)
	BackupCmd.AddCommand(backupConnCmd)
	BackupCmd.AddCommand(backupSanitizeCmd)
	BackupCmd.AddCommand(backupDeleteCmd)
	BackupCmd.AddCommand(backupMigrateAWSCmd)
	BackupCmd.AddCommand(backupEstimateCapacityCmd)

	initCreateFlags()
	initTestMinioFlags()
	initTestAWSFlags()
	initReadFlags()
	initListFlags()
	initDeleteFlags()
	initMonitorFlags()
	initConnFlags()
	initSanitizeFlags()
	initMigrateAWSFlags()
	initEstimateCapacityFlags()
}

func initCreateFlags() {
	// Backup creation flags
	backupCreateCmd.Flags().Int("log-level", 1, "Logging level: 0=quiet, 1=normal, 2=verbose, 3=debug, 4=trace (or use -v/-vv/-vvv/-vvvv, env: BACKUP_LOG_LEVEL)")
	backupCreateCmd.Flags().CountP("vflag", "v", "Increase verbosity (-v=verbose, -vv=debug, -vvv=trace, -vvvv=ultra-trace)")
	backupCreateCmd.Flags().Bool("dry-run", false, "Print actions without executing them")
	backupCreateCmd.Flags().String("estimate-method", "", "Compression estimation method for dry-run: 'heuristic' (instant, ~80% accurate), 'sample' (fast, ~90% accurate), 'accurate' (same speed as backup, 100% accurate)")
	backupCreateCmd.Flags().Int64("sample-size", 100*1024*1024, "Sample size in bytes for 'sample' estimation method (default: 100MB)")
	backupCreateCmd.Flags().Bool("delete", false, "Stop and remove containers, and delete associated directories after backup")
	backupCreateCmd.Flags().String("container-name", "", "Pipe-delimited container names or working directories to process (e.g. wp_foo|wp_bar|/srv/foo)")
	backupCreateCmd.Flags().String("container-names", "", "Comma-delimited container names to process (e.g. wp_foo,wp_bar)")
	backupCreateCmd.Flags().Bool("local", false, "Run backups locally using host's Docker instead of SSH")
	backupCreateCmd.Flags().String("container-file", "", "File with newline-delimited container names or working directories to process")
	backupCreateCmd.Flags().String("container-parent-dir", "/var/opt/sites", "Parent directory where site working directories live (default: /var/opt/sites)")
	backupCreateCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")
	backupCreateCmd.Flags().Bool("prune", false, "After creating backup, delete all old backups except the N most recent (configure N with --remainder)")
	backupCreateCmd.Flags().Int("remainder", 5, "Number of most recent backups to keep when using --prune (default: 5)")
	backupCreateCmd.Flags().Bool("clean-aws", false, "Also clean up old backups from AWS S3 when using --prune (default: false, only cleans Minio)")

	// Smart retention flags
	backupCreateCmd.Flags().Bool("smart-retention", getEnvBoolWithDefault("BACKUP_SMART_RETENTION", false), "Enable date-aware retention (preserves weekly/monthly from daily backups, env: BACKUP_SMART_RETENTION)")
	backupCreateCmd.Flags().Int("keep-daily", getEnvIntWithDefault("BACKUP_KEEP_DAILY", 14), "Daily backups to keep with smart retention (default: 14, env: BACKUP_KEEP_DAILY)")
	backupCreateCmd.Flags().Int("keep-weekly", getEnvIntWithDefault("BACKUP_KEEP_WEEKLY", 26), "Weekly backups to keep with smart retention (default: 26, env: BACKUP_KEEP_WEEKLY)")
	backupCreateCmd.Flags().Int("keep-monthly", getEnvIntWithDefault("BACKUP_KEEP_MONTHLY", 6), "Monthly backups to keep with smart retention (default: 6, env: BACKUP_KEEP_MONTHLY)")
	backupCreateCmd.Flags().Int("weekly-day", getEnvIntWithDefault("BACKUP_WEEKLY_DAY", 0), "Day of week for weekly backups, 0=Sunday (default: 0, env: BACKUP_WEEKLY_DAY)")
	backupCreateCmd.Flags().Int("monthly-day", getEnvIntWithDefault("BACKUP_MONTHLY_DAY", 1), "Day of month for monthly backups (default: 1, env: BACKUP_MONTHLY_DAY)")

	backupCreateCmd.Flags().Bool("respect-capacity-limit", getEnvBoolWithDefault("BACKUP_RESPECT_CAPACITY_LIMIT", false), "Check storage capacity before creating backup (env: BACKUP_RESPECT_CAPACITY_LIMIT)")
	backupCreateCmd.Flags().Float64("capacity-threshold", getEnvFloat64WithDefault("BACKUP_CAPACITY_THRESHOLD", 95.0), "Storage capacity threshold percentage (default: 95.0, env: BACKUP_CAPACITY_THRESHOLD)")
	backupCreateCmd.Flags().Bool("include-aws-glacier", getEnvBoolWithDefault("BACKUP_INCLUDE_AWS_GLACIER", false), "Upload backups to AWS Glacier in addition to Minio (env: BACKUP_INCLUDE_AWS_GLACIER)")

	// Custom container / config file flags
	backupCreateCmd.Flags().String("config-file", "", "Path to YAML configuration file for custom backup configurations")
	backupCreateCmd.Flags().String("config-dir", getEnvWithDefault("BACKUP_CONFIG_DIR", ""), "Directory containing YAML config files for custom apps (env: BACKUP_CONFIG_DIR)")
	backupCreateCmd.Flags().String("database-type", "", "Database type for custom containers (postgres, mysql, mongodb)")
	backupCreateCmd.Flags().String("database-export-dir", "", "Directory where database exports should be saved")
	backupCreateCmd.Flags().String("custom-app-dir", "", "Application directory for custom containers (if different from working dir)")
	backupCreateCmd.Flags().String("database-container", "", "Name of separate database container")
	backupCreateCmd.Flags().String("database-name", "", "Database name for custom exports")
	backupCreateCmd.Flags().String("database-user", "", "Database user for custom exports")

	// Minio configuration flags with environment variable support
	backupCreateCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	// Do NOT display sensitive API keys in --help output; read from env or flags at runtime
	backupCreateCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupCreateCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupCreateCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupCreateCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupCreateCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")
	backupCreateCmd.Flags().String("bucket-path", getEnvWithDefault("MINIO_BUCKET_PATH", ""), "Path prefix within Minio bucket (e.g., 'production/backups', env: MINIO_BUCKET_PATH)")

	// AWS S3 configuration flags (alternative to Minio) with environment variable support
	backupCreateCmd.Flags().String("s3-endpoint", getEnvWithDefault("S3_ENDPOINT", ""), "S3 endpoint (optional, defaults to AWS S3 endpoint for region, env: S3_ENDPOINT)")
	backupCreateCmd.Flags().String("s3-access-key", "", "S3 access key (env: S3_ACCESS_KEY or AWS_ACCESS_KEY_ID)")
	backupCreateCmd.Flags().String("s3-secret-key", "", "S3 secret key (env: S3_SECRET_KEY or AWS_SECRET_ACCESS_KEY)")
	backupCreateCmd.Flags().String("s3-bucket", getEnvWithDefault("S3_BUCKET", ""), "S3 bucket name (env: S3_BUCKET)")
	backupCreateCmd.Flags().String("s3-region", getEnvWithDefault("S3_REGION", "us-east-1"), "S3 region (env: S3_REGION, default: us-east-1)")
	backupCreateCmd.Flags().Bool("s3-ssl", getEnvBoolWithDefault("S3_SSL", true), "Use SSL for S3 connection (env: S3_SSL, default: true)")
	backupCreateCmd.Flags().Duration("s3-http-timeout", getEnvDurationWithDefault("S3_HTTP_TIMEOUT", 0), "S3 HTTP client timeout (e.g., 0s for no timeout) (env: S3_HTTP_TIMEOUT)")
	backupCreateCmd.Flags().String("s3-bucket-path", getEnvWithDefault("S3_BUCKET_PATH", ""), "Path prefix within S3 bucket (e.g., 'production/backups', env: S3_BUCKET_PATH)")

	// Retention policy configuration
	backupCreateCmd.Flags().String("retention-policy", getEnvWithDefault("BACKUP_RETENTION_POLICY", ""), "Path to YAML retention policy file (env: BACKUP_RETENTION_POLICY)")

	// AWS Glacier configuration flags with environment variable support
	backupCreateCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault name (env: AWS_VAULT)")
	backupCreateCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID or '-' for current account (env: AWS_ACCOUNT_ID, default: -)")
	backupCreateCmd.Flags().String("aws-access-key", "", "AWS access key (env: AWS_ACCESS_KEY)")
	backupCreateCmd.Flags().String("aws-secret-access-key", "", "AWS secret access key (env: AWS_SECRET_ACCESS_KEY)")
	backupCreateCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION, default: us-east-1)")
	backupCreateCmd.Flags().Duration("aws-http-timeout", getEnvDurationWithDefault("AWS_HTTP_TIMEOUT", 0), "AWS HTTP client timeout (e.g., 0s for no timeout) (env: AWS_HTTP_TIMEOUT)")

	// SSH connection flags with environment variable support
	backupCreateCmd.Flags().StringP("user", "u", getEnvWithDefault("SSH_USER", ""), "SSH username (env: SSH_USER, default: current user)")
	backupCreateCmd.Flags().StringP("port", "p", getEnvWithDefault("SSH_PORT", "22"), "SSH port (env: SSH_PORT)")
	backupCreateCmd.Flags().StringP("key", "k", getEnvWithDefault("SSH_KEY", ""), "Path to SSH private key (env: SSH_KEY)")
	backupCreateCmd.Flags().BoolP("agent", "a", getEnvBoolWithDefault("SSH_AGENT", true), "Use SSH agent (env: SSH_AGENT)")
	backupCreateCmd.Flags().DurationP("timeout", "t", getEnvDurationWithDefault("SSH_TIMEOUT", 30*time.Second), "Connection timeout (env: SSH_TIMEOUT)")
}

func initTestMinioFlags() {
	backupTestMinioCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupTestMinioCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupTestMinioCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupTestMinioCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupTestMinioCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupTestMinioCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")
}

func initTestAWSFlags() {
	backupTestAWSCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault name (env: AWS_VAULT)")
	backupTestAWSCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID or '-' for current account (env: AWS_ACCOUNT_ID, default: -)")
	backupTestAWSCmd.Flags().String("aws-access-key", "", "AWS access key (env: AWS_ACCESS_KEY)")
	backupTestAWSCmd.Flags().String("aws-secret-access-key", "", "AWS secret access key (env: AWS_SECRET_ACCESS_KEY)")
	backupTestAWSCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION, default: us-east-1)")
	backupTestAWSCmd.Flags().Duration("aws-http-timeout", getEnvDurationWithDefault("AWS_HTTP_TIMEOUT", 0), "AWS HTTP client timeout (e.g., 0s for no timeout) (env: AWS_HTTP_TIMEOUT)")
}

func initReadFlags() {
	backupReadCmd.Flags().String("output", "", "Output file path (if empty, writes to stdout)")
	backupReadCmd.Flags().Bool("save", false, "Save backup object to current working directory (same as --output <basename>)")
	backupReadCmd.Flags().String("prefix", "", "Prefix to search for when using --latest (e.g. backups/site-)")
	backupReadCmd.Flags().Bool("latest", false, "If set, resolve the most recent object matching --prefix when object argument is omitted")

	// MinIO flags
	backupReadCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupReadCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupReadCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupReadCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupReadCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupReadCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")

	// S3 flags (alternative to MinIO)
	backupReadCmd.Flags().String("s3-endpoint", getEnvWithDefault("S3_ENDPOINT", ""), "S3 endpoint (env: S3_ENDPOINT)")
	backupReadCmd.Flags().String("s3-access-key", "", "S3 access key (env: S3_ACCESS_KEY or AWS_ACCESS_KEY_ID)")
	backupReadCmd.Flags().String("s3-secret-key", "", "S3 secret key (env: S3_SECRET_KEY or AWS_SECRET_ACCESS_KEY)")
	backupReadCmd.Flags().String("s3-bucket", getEnvWithDefault("S3_BUCKET", ""), "S3 bucket name (env: S3_BUCKET)")
	backupReadCmd.Flags().String("s3-region", getEnvWithDefault("S3_REGION", "us-east-1"), "S3 region (env: S3_REGION)")
	backupReadCmd.Flags().Bool("s3-ssl", getEnvBoolWithDefault("S3_SSL", true), "Use SSL for S3 connection (env: S3_SSL)")
	backupReadCmd.Flags().Duration("s3-http-timeout", getEnvDurationWithDefault("S3_HTTP_TIMEOUT", 0), "S3 HTTP client timeout (env: S3_HTTP_TIMEOUT)")
}

func initListFlags() {
	backupListCmd.Flags().String("prefix", "", "Prefix to filter listed objects (e.g. backups/site-)")
	backupListCmd.Flags().Int("limit", 100, "Maximum number of objects to list")
	backupListCmd.Flags().Bool("json", false, "Output JSON")

	// MinIO flags
	backupListCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupListCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupListCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupListCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupListCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupListCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")

	// S3 flags (alternative to MinIO)
	backupListCmd.Flags().String("s3-endpoint", getEnvWithDefault("S3_ENDPOINT", ""), "S3 endpoint (env: S3_ENDPOINT)")
	backupListCmd.Flags().String("s3-access-key", "", "S3 access key (env: S3_ACCESS_KEY or AWS_ACCESS_KEY_ID)")
	backupListCmd.Flags().String("s3-secret-key", "", "S3 secret key (env: S3_SECRET_KEY or AWS_SECRET_ACCESS_KEY)")
	backupListCmd.Flags().String("s3-bucket", getEnvWithDefault("S3_BUCKET", ""), "S3 bucket name (env: S3_BUCKET)")
	backupListCmd.Flags().String("s3-region", getEnvWithDefault("S3_REGION", "us-east-1"), "S3 region (env: S3_REGION)")
	backupListCmd.Flags().Bool("s3-ssl", getEnvBoolWithDefault("S3_SSL", true), "Use SSL for S3 connection (env: S3_SSL)")
	backupListCmd.Flags().Duration("s3-http-timeout", getEnvDurationWithDefault("S3_HTTP_TIMEOUT", 0), "S3 HTTP client timeout (env: S3_HTTP_TIMEOUT)")
}

func initDeleteFlags() {
	backupDeleteCmd.Flags().Bool("dry-run", false, "Preview deletions without performing them")
	backupDeleteCmd.Flags().String("prefix", "", "Prefix to select objects to delete (e.g. backups/site-)")
	backupDeleteCmd.Flags().Int("limit", 100, "Maximum number of objects to consider when using --prefix")
	backupDeleteCmd.Flags().Bool("latest", false, "If set with --prefix, delete only the most recent object matching --prefix")
	backupDeleteCmd.Flags().Bool("delete-all", false, "Delete all backups (respects --prefix if provided)")
	backupDeleteCmd.Flags().String("delete-range", "", "Delete backups by numeric range (e.g., '1-10' for 1st through 10th most recent)")
	backupDeleteCmd.Flags().String("delete-range-by-date", "", "Delete backups by date range (YYYYMMDD-YYYYMMDD or YYYYMMDD:HHMMSS-YYYYMMDD:HHMMSS)")
	backupDeleteCmd.Flags().Bool("skip-confirmation", false, "Skip interactive confirmation prompt")

	// MinIO flags
	backupDeleteCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupDeleteCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupDeleteCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupDeleteCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupDeleteCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupDeleteCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")

	// S3 flags (alternative to MinIO)
	backupDeleteCmd.Flags().String("s3-endpoint", getEnvWithDefault("S3_ENDPOINT", ""), "S3 endpoint (env: S3_ENDPOINT)")
	backupDeleteCmd.Flags().String("s3-access-key", "", "S3 access key (env: S3_ACCESS_KEY or AWS_ACCESS_KEY_ID)")
	backupDeleteCmd.Flags().String("s3-secret-key", "", "S3 secret key (env: S3_SECRET_KEY or AWS_SECRET_ACCESS_KEY)")
	backupDeleteCmd.Flags().String("s3-bucket", getEnvWithDefault("S3_BUCKET", ""), "S3 bucket name (env: S3_BUCKET)")
	backupDeleteCmd.Flags().String("s3-region", getEnvWithDefault("S3_REGION", "us-east-1"), "S3 region (env: S3_REGION)")
	backupDeleteCmd.Flags().Bool("s3-ssl", getEnvBoolWithDefault("S3_SSL", true), "Use SSL for S3 connection (env: S3_SSL)")
	backupDeleteCmd.Flags().Duration("s3-http-timeout", getEnvDurationWithDefault("S3_HTTP_TIMEOUT", 0), "S3 HTTP client timeout (env: S3_HTTP_TIMEOUT)")
}

func initMonitorFlags() {
	backupMonitorCmd.Flags().Int("log-level", 1, "Logging level: 0=quiet, 1=normal, 2=verbose, 3=debug, 4=trace (or use -v/-vv/-vvv/-vvvv, env: BACKUP_LOG_LEVEL)")
	backupMonitorCmd.Flags().CountP("vflag", "v", "Increase verbosity (-v=verbose, -vv=debug, -vvv=trace, -vvvv=ultra-trace)")
	backupMonitorCmd.Flags().Bool("dry-run", false, "Preview what would be migrated without making changes")
	backupMonitorCmd.Flags().Bool("show-mounts", false, "Display all filesystem mount points and exit (helpful for finding storage-path)")
	backupMonitorCmd.Flags().String("storage-server", getEnvWithDefault("STORAGE_SERVER_ADDR", ""), "Remote storage server address for SSH capacity checking (env: STORAGE_SERVER_ADDR)")
	backupMonitorCmd.Flags().String("storage-path", getEnvWithDefault("STORAGE_PATH", "/mnt/minio_nyc2"), "Path to monitor for storage capacity (env: STORAGE_PATH, default: /mnt/minio_nyc2)")
	backupMonitorCmd.Flags().Float64("threshold", getEnvFloat64WithDefault("STORAGE_THRESHOLD", 95.0), "Storage usage threshold percentage to trigger migration (env: STORAGE_THRESHOLD, default: 95.0)")
	backupMonitorCmd.Flags().Float64("migrate-percent", getEnvFloat64WithDefault("MIGRATE_PERCENT", 10.0), "Percentage of oldest backups to migrate when threshold exceeded (env: MIGRATE_PERCENT, default: 10.0)")
	backupMonitorCmd.Flags().Bool("force-delete", getEnvBoolWithDefault("STORAGE_FORCE_DELETE", false), "Delete oldest backups without migrating when AWS fails (env: STORAGE_FORCE_DELETE)")
	backupMonitorCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupMonitorCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupMonitorCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupMonitorCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupMonitorCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupMonitorCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")
	backupMonitorCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault name (env: AWS_VAULT)")
	backupMonitorCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID or '-' for current account (env: AWS_ACCOUNT_ID, default: -)")
	backupMonitorCmd.Flags().String("aws-access-key", "", "AWS access key (env: AWS_ACCESS_KEY)")
	backupMonitorCmd.Flags().String("aws-secret-access-key", "", "AWS secret access key (env: AWS_SECRET_ACCESS_KEY)")
	backupMonitorCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION, default: us-east-1)")
	backupMonitorCmd.Flags().Duration("aws-http-timeout", getEnvDurationWithDefault("AWS_HTTP_TIMEOUT", 0), "AWS HTTP client timeout (e.g., 0s for no timeout) (env: AWS_HTTP_TIMEOUT)")

	// SSH connection flags for remote storage server
	backupMonitorCmd.Flags().StringP("user", "u", getEnvWithDefault("SSH_USER", ""), "SSH username for storage server (env: SSH_USER, default: current user)")
	backupMonitorCmd.Flags().StringP("port", "p", getEnvWithDefault("SSH_PORT", "22"), "SSH port (env: SSH_PORT)")
	backupMonitorCmd.Flags().StringP("key", "k", getEnvWithDefault("SSH_KEY", ""), "Path to SSH private key (env: SSH_KEY)")
	backupMonitorCmd.Flags().BoolP("agent", "a", getEnvBoolWithDefault("SSH_AGENT", true), "Use SSH agent (env: SSH_AGENT)")
	backupMonitorCmd.Flags().DurationP("timeout", "t", getEnvDurationWithDefault("SSH_TIMEOUT", 30*time.Second), "Connection timeout (env: SSH_TIMEOUT)")
}

func initConnFlags() {
	backupConnCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupConnCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupConnCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupConnCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupConnCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupConnCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")
	backupConnCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault name (env: AWS_VAULT)")
	backupConnCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID or '-' for current account (env: AWS_ACCOUNT_ID, default: -)")
	backupConnCmd.Flags().String("aws-access-key", "", "AWS access key (env: AWS_ACCESS_KEY)")
	backupConnCmd.Flags().String("aws-secret-access-key", "", "AWS secret access key (env: AWS_SECRET_ACCESS_KEY)")
	backupConnCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION, default: us-east-1)")
	backupConnCmd.Flags().Duration("aws-http-timeout", getEnvDurationWithDefault("AWS_HTTP_TIMEOUT", 0), "AWS HTTP client timeout (e.g., 0s for no timeout) (env: AWS_HTTP_TIMEOUT)")
}

func initSanitizeFlags() {
	backupSanitizeCmd.Flags().String("input", "", "Path to input backup tarball (required)")
	backupSanitizeCmd.Flags().String("output", "", "Path to output sanitized tarball (required)")
	backupSanitizeCmd.Flags().String("extract-dir", "wp-content", "Comma-separated list of directories to extract from tarball (default: wp-content)")
	backupSanitizeCmd.Flags().String("extract-file", "*.sql", "Comma-separated list of file patterns to extract (default: *.sql)")
	backupSanitizeCmd.Flags().Bool("dry-run", false, "Preview what would be extracted without making changes")
	backupSanitizeCmd.MarkFlagRequired("input")
	backupSanitizeCmd.MarkFlagRequired("output")
}

func initMigrateAWSFlags() {
	backupMigrateAWSCmd.Flags().Int("log-level", 1, "Logging level: 0=quiet, 1=normal, 2=verbose, 3=debug, 4=trace (or use -v/-vv/-vvv/-vvvv, env: BACKUP_LOG_LEVEL)")
	backupMigrateAWSCmd.Flags().CountP("vflag", "v", "Increase verbosity (-v=verbose, -vv=debug, -vvv=trace, -vvvv=ultra-trace)")
	backupMigrateAWSCmd.Flags().Bool("dry-run", false, "Preview what would be migrated without making changes")
	backupMigrateAWSCmd.Flags().String("prefix", "", "Prefix to filter backups (e.g., backups/mysite.com/)")
	backupMigrateAWSCmd.Flags().String("object", "", "Specific backup object key to migrate (e.g., backups/site.com/backup.tgz, mutually exclusive with --count, --percent, and --older-than)")
	backupMigrateAWSCmd.Flags().Int("count", 0, "Number of oldest backups to migrate (mutually exclusive with --object, --percent, and --older-than)")
	backupMigrateAWSCmd.Flags().Float64("percent", 0, "Percentage of oldest backups to migrate (e.g., 10 for 10%, mutually exclusive with --object, --count, and --older-than)")
	backupMigrateAWSCmd.Flags().Duration("older-than", 0, "Migrate backups older than this duration (e.g., 720h for 30 days, mutually exclusive with --object, --count, and --percent)")
	backupMigrateAWSCmd.Flags().Bool("delete-after", false, "Delete backups from Minio after successful migration to AWS Glacier")
	backupMigrateAWSCmd.Flags().Int("limit", 0, "Maximum number of backups to list for selection (0=unlimited)")

	// Minio configuration for migrate-aws
	backupMigrateAWSCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupMigrateAWSCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupMigrateAWSCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupMigrateAWSCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupMigrateAWSCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupMigrateAWSCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (env: MINIO_HTTP_TIMEOUT)")

	// AWS configuration for migrate-aws
	backupMigrateAWSCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault name (env: AWS_VAULT)")
	backupMigrateAWSCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID or '-' for current account (env: AWS_ACCOUNT_ID)")
	backupMigrateAWSCmd.Flags().String("aws-access-key", "", "AWS access key (env: AWS_ACCESS_KEY)")
	backupMigrateAWSCmd.Flags().String("aws-secret-access-key", "", "AWS secret access key (env: AWS_SECRET_ACCESS_KEY)")
	backupMigrateAWSCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION)")
	backupMigrateAWSCmd.Flags().Duration("aws-http-timeout", getEnvDurationWithDefault("AWS_HTTP_TIMEOUT", 0), "AWS HTTP client timeout (env: AWS_HTTP_TIMEOUT)")
}

func initEstimateCapacityFlags() {
	backupEstimateCapacityCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")
	backupEstimateCapacityCmd.Flags().String("estimate-method", "heuristic", "Compression estimation method: 'heuristic' (~20s/site, 80% accurate), 'sample' (~30s/site, 90% accurate), 'accurate' (~3-5min/site over SSH, 100% accurate)")
	backupEstimateCapacityCmd.Flags().Int64("sample-size", 100*1024*1024, "Sample size in bytes for 'sample' estimation method (default: 100MB)")

	// Baseline input methods
	backupEstimateCapacityCmd.Flags().String("from-backup", "", "Use existing backup file as baseline (path to backup in Minio)")
	backupEstimateCapacityCmd.Flags().String("avg-compressed-size", "", "Manual average compressed size per site (e.g., '125MB', '1.5GB')")
	backupEstimateCapacityCmd.Flags().Int("site-count", 0, "Number of sites (required when using --avg-compressed-size)")

	// Retention policy
	backupEstimateCapacityCmd.Flags().Int("daily-retention", getEnvIntWithDefault("BACKUP_DAILY_RETENTION", 14), "Number of daily backups to retain (default: 14, env: BACKUP_DAILY_RETENTION)")
	backupEstimateCapacityCmd.Flags().Int("weekly-retention", getEnvIntWithDefault("BACKUP_WEEKLY_RETENTION", 26), "Number of weekly backups to retain (default: 26, env: BACKUP_WEEKLY_RETENTION)")
	backupEstimateCapacityCmd.Flags().Int("monthly-retention", getEnvIntWithDefault("BACKUP_MONTHLY_RETENTION", 6), "Number of monthly backups to retain (default: 6, env: BACKUP_MONTHLY_RETENTION)")

	// Focus and output control
	backupEstimateCapacityCmd.Flags().String("estimate-focus", "all", "Focus: 'growth-modeling', 'static-capacity', or 'all' (default: all)")
	backupEstimateCapacityCmd.Flags().String("estimate-type", "all", "What to estimate: 'cost', 'size', or 'all' (default: all)")
	backupEstimateCapacityCmd.Flags().String("output", "stdout", "Output format: 'stdout', 'json', or 'csv' (default: stdout)")

	// Growth modeling
	backupEstimateCapacityCmd.Flags().Float64("growth-rate", 0, "Monthly growth rate percentage for projections (e.g., 5 for 5% monthly growth)")
	backupEstimateCapacityCmd.Flags().Int("projection-months", 12, "Number of months to project growth (default: 12)")
	backupEstimateCapacityCmd.Flags().Float64("buffer-percent", 20, "Safety buffer percentage to add to calculations (default: 20%)")

	// Cost estimation
	backupEstimateCapacityCmd.Flags().Float64("aws-glacier-price", 0.004, "AWS Glacier price per GB per month (default: $0.004)")
	backupEstimateCapacityCmd.Flags().Float64("aws-retrieval-price", 0.01, "AWS Glacier retrieval price per GB (default: $0.01)")

	// Storage recommendations
	backupEstimateCapacityCmd.Flags().String("available-storage", "", "Available Minio storage capacity (e.g., '500GB', '2TB') for recommendations")

	// SSH flags for live scanning
	backupEstimateCapacityCmd.Flags().StringP("user", "u", getEnvWithDefault("SSH_USER", ""), "SSH username (env: SSH_USER)")
	backupEstimateCapacityCmd.Flags().StringP("port", "p", getEnvWithDefault("SSH_PORT", "22"), "SSH port (env: SSH_PORT)")
	backupEstimateCapacityCmd.Flags().StringP("key", "k", getEnvWithDefault("SSH_KEY", ""), "Path to SSH private key (env: SSH_KEY)")
	backupEstimateCapacityCmd.Flags().BoolP("agent", "a", getEnvBoolWithDefault("SSH_AGENT", true), "Use SSH agent (env: SSH_AGENT)")
	backupEstimateCapacityCmd.Flags().DurationP("timeout", "t", getEnvDurationWithDefault("SSH_TIMEOUT", 30*time.Second), "Connection timeout (env: SSH_TIMEOUT)")

	// Minio configuration for reading existing backups
	backupEstimateCapacityCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupEstimateCapacityCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupEstimateCapacityCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupEstimateCapacityCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupEstimateCapacityCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")

	// Optional: container parent directory
	backupEstimateCapacityCmd.Flags().String("container-parent-dir", "/var/opt/sites", "Parent directory where site working directories live (default: /var/opt/sites)")
}

// getMinioConfig creates Minio configuration from command flags
func getMinioConfig(cmd *cobra.Command) (*backup.MinioConfig, error) {
	endpoint := mustGetStringFlag(cmd, "minio-endpoint")
	if endpoint == "" {
		endpoint = getEnvWithDefault("MINIO_ENDPOINT", "")
	}
	if endpoint == "" {
		// MinIO is optional if S3 is configured, return nil
		return nil, nil
	}

	accessKey := mustGetStringFlag(cmd, "minio-access-key")
	if accessKey == "" {
		accessKey = getEnvWithDefault("MINIO_ACCESS_KEY", "")
	}
	if accessKey == "" {
		return nil, fmt.Errorf("minio-access-key is required (use --minio-access-key or set MINIO_ACCESS_KEY)")
	}

	secretKey := mustGetStringFlag(cmd, "minio-secret-key")
	if secretKey == "" {
		secretKey = getEnvWithDefault("MINIO_SECRET_KEY", "")
	}
	if secretKey == "" {
		return nil, fmt.Errorf("minio-secret-key is required (use --minio-secret-key or set MINIO_SECRET_KEY)")
	}

	bucket := mustGetStringFlag(cmd, "minio-bucket")
	useSSL := mustGetBoolFlag(cmd, "minio-ssl")
	httpTimeout := mustGetDurationFlag(cmd, "minio-http-timeout")

	// Get bucket path if available
	var bucketPath string
	if cmd.Flags().Lookup("bucket-path") != nil {
		bucketPath = mustGetStringFlag(cmd, "bucket-path")
	}

	return &backup.MinioConfig{
		Endpoint:    endpoint,
		AccessKey:   accessKey,
		SecretKey:   secretKey,
		Bucket:      bucket,
		UseSSL:      useSSL,
		BucketPath:  bucketPath,
		HTTPTimeout: httpTimeout,
	}, nil
}

// getS3Config creates S3 configuration from command flags
func getS3Config(cmd *cobra.Command) (*backup.S3Config, error) {
	// Check if S3 bucket is provided - if not, S3 is not configured
	bucket := mustGetStringFlag(cmd, "s3-bucket")
	if bucket == "" {
		bucket = getEnvWithDefault("S3_BUCKET", "")
	}
	if bucket == "" {
		// S3 is optional, return nil if not configured
		return nil, nil
	}

	accessKey := mustGetStringFlag(cmd, "s3-access-key")
	if accessKey == "" {
		accessKey = getEnvWithDefault("S3_ACCESS_KEY", "")
		if accessKey == "" {
			// Fallback to standard AWS environment variables
			accessKey = getEnvWithDefault("AWS_ACCESS_KEY_ID", "")
		}
	}
	if accessKey == "" {
		return nil, fmt.Errorf("s3-access-key is required when using S3 (use --s3-access-key or set S3_ACCESS_KEY/AWS_ACCESS_KEY_ID)")
	}

	secretKey := mustGetStringFlag(cmd, "s3-secret-key")
	if secretKey == "" {
		secretKey = getEnvWithDefault("S3_SECRET_KEY", "")
		if secretKey == "" {
			// Fallback to standard AWS environment variables
			secretKey = getEnvWithDefault("AWS_SECRET_ACCESS_KEY", "")
		}
	}
	if secretKey == "" {
		return nil, fmt.Errorf("s3-secret-key is required when using S3 (use --s3-secret-key or set S3_SECRET_KEY/AWS_SECRET_ACCESS_KEY)")
	}

	endpoint := mustGetStringFlag(cmd, "s3-endpoint")
	region := mustGetStringFlag(cmd, "s3-region")
	useSSL := mustGetBoolFlag(cmd, "s3-ssl")
	httpTimeout := mustGetDurationFlag(cmd, "s3-http-timeout")

	// Get bucket path if available
	var bucketPath string
	if cmd.Flags().Lookup("s3-bucket-path") != nil {
		bucketPath = mustGetStringFlag(cmd, "s3-bucket-path")
	}

	return &backup.S3Config{
		Endpoint:    endpoint,
		AccessKey:   accessKey,
		SecretKey:   secretKey,
		Bucket:      bucket,
		Region:      region,
		UseSSL:      useSSL,
		BucketPath:  bucketPath,
		HTTPTimeout: httpTimeout,
	}, nil
}

// getAWSConfig creates AWS configuration from command flags
func getAWSConfig(cmd *cobra.Command) (*backup.AWSConfig, error) {
	vault := mustGetStringFlag(cmd, "aws-vault")
	if vault == "" {
		vault = getEnvWithDefault("AWS_VAULT", "")
	}
	if vault == "" {
		// AWS is optional, so return nil if not configured
		return nil, nil
	}

	accessKey := mustGetStringFlag(cmd, "aws-access-key")
	if accessKey == "" {
		accessKey = getEnvWithDefault("AWS_ACCESS_KEY", "")
	}

	secretKey := mustGetStringFlag(cmd, "aws-secret-access-key")
	if secretKey == "" {
		secretKey = getEnvWithDefault("AWS_SECRET_ACCESS_KEY", "")
	}

	region := mustGetStringFlag(cmd, "aws-region")
	if region == "" {
		region = getEnvWithDefault("AWS_REGION", "us-east-1")
	}

	accountID := mustGetStringFlag(cmd, "aws-account-id")
	if accountID == "" {
		accountID = getEnvWithDefault("AWS_ACCOUNT_ID", "-")
	}

	httpTimeout := mustGetDurationFlag(cmd, "aws-http-timeout")

	return &backup.AWSConfig{
		Vault:       vault,
		AccountID:   accountID,
		AccessKey:   accessKey,
		SecretKey:   secretKey,
		Region:      region,
		HTTPTimeout: httpTimeout,
	}, nil
}
