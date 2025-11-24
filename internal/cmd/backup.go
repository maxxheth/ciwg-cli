package cmd

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/backup"
)

var backupCmd = &cobra.Command{
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
	backupCmd.PersistentFlags().String("env", "", "Path to .env file to load (overrides defaults)")
	rootCmd.AddCommand(backupCmd)
	backupCmd.AddCommand(backupCreateCmd)
	backupCmd.AddCommand(backupTestMinioCmd)
	backupCmd.AddCommand(backupTestAWSCmd)
	backupCmd.AddCommand(backupReadCmd)
	backupCmd.AddCommand(backupListCmd)
	backupCmd.AddCommand(backupMonitorCmd)
	backupCmd.AddCommand(backupConnCmd)
	backupCmd.AddCommand(backupSanitizeCmd)
	backupCmd.AddCommand(backupDeleteCmd)
	backupCmd.AddCommand(backupMigrateAWSCmd)
	backupCmd.AddCommand(backupEstimateCapacityCmd)

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

	// AWS S3 configuration flags with environment variable support
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

	// Minio test command flags
	backupTestMinioCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupTestMinioCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupTestMinioCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupTestMinioCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupTestMinioCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupTestMinioCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")

	// AWS test command flags
	backupTestAWSCmd.Flags().String("aws-vault", getEnvWithDefault("AWS_VAULT", ""), "AWS Glacier vault name (env: AWS_VAULT)")
	backupTestAWSCmd.Flags().String("aws-account-id", getEnvWithDefault("AWS_ACCOUNT_ID", "-"), "AWS account ID or '-' for current account (env: AWS_ACCOUNT_ID, default: -)")
	backupTestAWSCmd.Flags().String("aws-access-key", "", "AWS access key (env: AWS_ACCESS_KEY)")
	backupTestAWSCmd.Flags().String("aws-secret-access-key", "", "AWS secret access key (env: AWS_SECRET_ACCESS_KEY)")
	backupTestAWSCmd.Flags().String("aws-region", getEnvWithDefault("AWS_REGION", "us-east-1"), "AWS region (env: AWS_REGION, default: us-east-1)")
	backupTestAWSCmd.Flags().Duration("aws-http-timeout", getEnvDurationWithDefault("AWS_HTTP_TIMEOUT", 0), "AWS HTTP client timeout (e.g., 0s for no timeout) (env: AWS_HTTP_TIMEOUT)")

	// Read command flags
	backupReadCmd.Flags().String("output", "", "Output file path (if empty, writes to stdout)")
	backupReadCmd.Flags().Bool("save", false, "Save backup object to current working directory (same as --output <basename>)")
	backupReadCmd.Flags().String("prefix", "", "Prefix to search for when using --latest (e.g. backups/site-)")
	backupReadCmd.Flags().Bool("latest", false, "If set, resolve the most recent object matching --prefix when object argument is omitted")
	backupReadCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupReadCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupReadCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupReadCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupReadCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupReadCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")

	// List command flags
	backupListCmd.Flags().String("prefix", "", "Prefix to filter listed objects (e.g. backups/site-)")
	backupListCmd.Flags().Int("limit", 100, "Maximum number of objects to list")
	backupListCmd.Flags().Bool("json", false, "Output JSON")
	backupListCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupListCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupListCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupListCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupListCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupListCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")

	// Delete command flags
	backupDeleteCmd.Flags().Bool("dry-run", false, "Preview deletions without performing them")
	backupDeleteCmd.Flags().String("prefix", "", "Prefix to select objects to delete (e.g. backups/site-)")
	backupDeleteCmd.Flags().Int("limit", 100, "Maximum number of objects to consider when using --prefix")
	backupDeleteCmd.Flags().Bool("latest", false, "If set with --prefix, delete only the most recent object matching --prefix")
	backupDeleteCmd.Flags().Bool("delete-all", false, "Delete all backups (respects --prefix if provided)")
	backupDeleteCmd.Flags().String("delete-range", "", "Delete backups by numeric range (e.g., '1-10' for 1st through 10th most recent)")
	backupDeleteCmd.Flags().String("delete-range-by-date", "", "Delete backups by date range (YYYYMMDD-YYYYMMDD or YYYYMMDD:HHMMSS-YYYYMMDD:HHMMSS)")
	backupDeleteCmd.Flags().Bool("skip-confirmation", false, "Skip interactive confirmation prompt")
	backupDeleteCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupDeleteCmd.Flags().String("minio-access-key", "", "Minio access key (env: MINIO_ACCESS_KEY)")
	backupDeleteCmd.Flags().String("minio-secret-key", "", "Minio secret key (env: MINIO_SECRET_KEY)")
	backupDeleteCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupDeleteCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
	backupDeleteCmd.Flags().Duration("minio-http-timeout", getEnvDurationWithDefault("MINIO_HTTP_TIMEOUT", 0), "Minio HTTP client timeout (e.g., 0s for no timeout) (env: MINIO_HTTP_TIMEOUT)")

	// Monitor command flags
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

	// Conn command flags (test both Minio and AWS Glacier)
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

	// Sanitize command flags
	backupSanitizeCmd.Flags().String("input", "", "Path to input backup tarball (required)")
	backupSanitizeCmd.Flags().String("output", "", "Path to output sanitized tarball (required)")
	backupSanitizeCmd.Flags().String("extract-dir", "wp-content", "Comma-separated list of directories to extract from tarball (default: wp-content)")
	backupSanitizeCmd.Flags().String("extract-file", "*.sql", "Comma-separated list of file patterns to extract (default: *.sql)")
	backupSanitizeCmd.Flags().Bool("dry-run", false, "Preview what would be extracted without making changes")
	backupSanitizeCmd.MarkFlagRequired("input")
	backupSanitizeCmd.MarkFlagRequired("output")

	// Migrate AWS command flags
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

	// Estimate capacity command flags
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

// findEnvArg inspects argv for an explicit --env argument and returns
// the value if present. Supports `--env=path` and `--env path` forms.
func findEnvArg(argv []string) string {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "--env=") {
			return strings.TrimPrefix(a, "--env=")
		}
		if a == "--env" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func runBackupCreate(cmd *cobra.Command, args []string) error {
	// If user specified an env file via --env, load it now to override environment
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}
	serverRange, _ := cmd.Flags().GetString("server-range")

	// Validate Minio configuration
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}

	// Get AWS configuration (optional)
	awsConfig, err := getAWSConfig(cmd)
	if err != nil {
		return err
	}

	if serverRange != "" {
		return processBackupCreateForServerRange(cmd, serverRange, minioConfig, awsConfig)
	}

	if len(args) < 1 {
		return fmt.Errorf("hostname argument is required when --server-range is not used")
	}

	hostname := args[0]
	return createBackupForHost(cmd, hostname, minioConfig, awsConfig)
}

func processBackupCreateForServerRange(cmd *cobra.Command, serverRange string, minioConfig *backup.MinioConfig, awsConfig *backup.AWSConfig) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("error parsing server range: %w", err)
	}

	for i := start; i <= end; i++ {
		if exclusions[i] {
			fmt.Printf("Skipping excluded server: %s\n", fmt.Sprintf(pattern, i))
			continue
		}
		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("--- Processing server: %s ---\n", hostname)
		err := createBackupForHost(cmd, hostname, minioConfig, awsConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func createBackupForHost(cmd *cobra.Command, hostname string, minioConfig *backup.MinioConfig, awsConfig *backup.AWSConfig) error {

	// Determine if running locally
	localMode := mustGetBoolFlag(cmd, "local")

	var sshClient *auth.SSHClient
	if !localMode {
		var err error
		sshClient, err = createSSHClient(cmd, hostname)
		if err != nil {
			return err
		}
		defer sshClient.Close()
	}

	// Create backup manager with AWS config if available
	var backupManager *backup.BackupManager
	if awsConfig != nil {
		backupManager = backup.NewBackupManagerWithAWS(sshClient, minioConfig, awsConfig)
	} else {
		backupManager = backup.NewBackupManager(sshClient, minioConfig)
	}

	// Set verbosity level
	logLevel, _ := cmd.Flags().GetInt("log-level")
	vflag, _ := cmd.Flags().GetCount("vflag")
	verbosity := logLevel
	if vflag > 0 {
		verbosity = 1 + vflag // -v=2, -vv=3, -vvv=4, -vvvv=5
	}
	backupManager.SetVerbosity(verbosity)

	// Parse container-names (comma-delimited)
	var containerNames []string
	if v := mustGetStringFlag(cmd, "container-names"); v != "" {
		for _, p := range strings.Split(v, ",") {
			if s := strings.TrimSpace(p); s != "" {
				containerNames = append(containerNames, s)
			}
		}
	}

	// Get backup options from flags
	estimateMethod := mustGetStringFlag(cmd, "estimate-method")
	sampleSize, _ := cmd.Flags().GetInt64("sample-size")

	// Parse smart retention options
	var smartRetention *backup.SmartRetentionPolicy
	if mustGetBoolFlag(cmd, "smart-retention") {
		keepDaily, _ := cmd.Flags().GetInt("keep-daily")
		keepWeekly, _ := cmd.Flags().GetInt("keep-weekly")
		keepMonthly, _ := cmd.Flags().GetInt("keep-monthly")
		weeklyDay, _ := cmd.Flags().GetInt("weekly-day")
		monthlyDay, _ := cmd.Flags().GetInt("monthly-day")

		smartRetention = &backup.SmartRetentionPolicy{
			Enabled:     true,
			KeepDaily:   keepDaily,
			KeepWeekly:  keepWeekly,
			KeepMonthly: keepMonthly,
			WeeklyDay:   weeklyDay,
			MonthlyDay:  monthlyDay,
		}
	}

	options := &backup.BackupOptions{
		DryRun:               mustGetBoolFlag(cmd, "dry-run"),
		Delete:               mustGetBoolFlag(cmd, "delete"),
		ContainerName:        mustGetStringFlag(cmd, "container-name"),
		ContainerFile:        mustGetStringFlag(cmd, "container-file"),
		ContainerNames:       containerNames,
		Local:                localMode,
		ParentDir:            mustGetStringFlag(cmd, "container-parent-dir"),
		ConfigFile:           mustGetStringFlag(cmd, "config-file"),
		DatabaseType:         mustGetStringFlag(cmd, "database-type"),
		DatabaseExportDir:    mustGetStringFlag(cmd, "database-export-dir"),
		CustomAppDir:         mustGetStringFlag(cmd, "custom-app-dir"),
		DatabaseContainer:    mustGetStringFlag(cmd, "database-container"),
		DatabaseName:         mustGetStringFlag(cmd, "database-name"),
		DatabaseUser:         mustGetStringFlag(cmd, "database-user"),
		RespectCapacityLimit: mustGetBoolFlag(cmd, "respect-capacity-limit"),
		CapacityThreshold:    mustGetFloat64Flag(cmd, "capacity-threshold"),
		IncludeAWSGlacier:    mustGetBoolFlag(cmd, "include-aws-glacier"),
		EstimateMethod:       estimateMethod,
		SampleSize:           sampleSize,
		SmartRetention:       smartRetention,
	}

	fmt.Printf("Creating backups on %s...\n\n", hostname)
	err := backupManager.CreateBackups(options)
	if err != nil {
		return err
	}

	// Handle prune mode: clean up old backups
	prune := mustGetBoolFlag(cmd, "prune")
	if prune {
		remainder := 5
		if v, err := cmd.Flags().GetInt("remainder"); err == nil {
			remainder = v
		}
		if remainder < 0 {
			return fmt.Errorf("--remainder must be >= 0")
		}

		cleanAWS := mustGetBoolFlag(cmd, "clean-aws")

		// Display pruning strategy
		if smartRetention != nil && smartRetention.Enabled {
			fmt.Printf("\n--- Smart Retention Pruning (daily=%d, weekly=%d, monthly=%d) ---\n",
				smartRetention.KeepDaily, smartRetention.KeepWeekly, smartRetention.KeepMonthly)
			fmt.Printf("Weekly backups: every %s | Monthly backups: day %d of month\n",
				time.Weekday(smartRetention.WeeklyDay), smartRetention.MonthlyDay)
		} else {
			if cleanAWS && awsConfig != nil && awsConfig.Vault != "" {
				fmt.Printf("\n--- Pruning old backups from Minio and AWS Glacier (keeping %d most recent) ---\n", remainder)
			} else {
				fmt.Printf("\n--- Pruning old backups from Minio (keeping %d most recent) ---\n", remainder)
			}
		}

		// For each container that was backed up, clean up old backups
		containers, err := backupManager.GetContainersFromOptions(options)
		if err != nil {
			return fmt.Errorf("failed to get containers for cleanup: %w", err)
		}

		for _, container := range containers {
			siteName := filepath.Base(container.WorkingDir)
			// If the container has a configured bucket_path, it supersedes the
			// default backups/<siteName>/ prefix. Otherwise prefer global
			// MinioConfig.BucketPath. If neither is set, use the default.
			var prefix string
			if container.Config != nil && container.Config.BucketPath != "" {
				prefix = filepath.Clean(container.Config.BucketPath) + "/"
			} else if backupManager.GetBucketPath() != "" {
				prefix = filepath.Clean(backupManager.GetBucketPath()) + "/"
			} else {
				prefix = fmt.Sprintf("backups/%s/", siteName)
			}

			objs, err := backupManager.ListBackups(prefix, 0)
			if err != nil {
				fmt.Printf("Warning: failed to list backups for %s: %v\n", siteName, err)
				continue
			}

			// Use smart retention or simple retention based on configuration
			var toDelete []backup.ObjectInfo
			if smartRetention != nil && smartRetention.Enabled {
				toDelete = backupManager.SelectObjectsWithSmartRetention(objs, smartRetention)

				if len(toDelete) == 0 {
					fmt.Printf("Site %s: Found %d backup(s), all preserved by retention policy\n", siteName, len(objs))
					continue
				}

				fmt.Printf("Site %s: Found %d backup(s), preserving backups per policy, deleting %d older backup(s)\n",
					siteName, len(objs), len(toDelete))
			} else {
				if len(objs) <= remainder {
					fmt.Printf("Site %s: Found %d backup(s), keeping all\n", siteName, len(objs))
					continue
				}

				toDelete = backupManager.SelectObjectsForOverwrite(objs, remainder)
				if len(toDelete) == 0 {
					continue
				}

				fmt.Printf("Site %s: Found %d backup(s), keeping %d most recent, deleting %d older backup(s)\n",
					siteName, len(objs), remainder, len(toDelete))
			}
			var deleteKeys []string
			for _, o := range toDelete {
				deleteKeys = append(deleteKeys, o.Key)
			}

			// Delete from Minio
			if err := backupManager.DeleteObjects(deleteKeys); err != nil {
				fmt.Printf("Warning: failed to delete old Minio backups for %s: %v\n", siteName, err)
			} else {
				fmt.Printf("Successfully cleaned up old Minio backups for %s\n", siteName)
			}

			// If AWS cleanup is enabled and AWS is configured, also clean up AWS backups
			if cleanAWS && awsConfig != nil && awsConfig.Vault != "" {
				awsObjs, err := backupManager.ListAWSBackups(prefix, 0)
				if err != nil {
					fmt.Printf("Warning: failed to list AWS backups for %s: %v\n", siteName, err)
				} else if len(awsObjs) > remainder {
					awsToDelete := backupManager.SelectObjectsForOverwrite(awsObjs, remainder)
					if len(awsToDelete) > 0 {
						var awsDeleteKeys []string
						for _, o := range awsToDelete {
							awsDeleteKeys = append(awsDeleteKeys, o.Key)
						}
						if err := backupManager.DeleteAWSObjects(awsDeleteKeys); err != nil {
							fmt.Printf("Warning: failed to delete old AWS backups for %s: %v\n", siteName, err)
						} else {
							fmt.Printf("Successfully cleaned up old AWS backups for %s\n", siteName)
						}
					}
				}
			}
		}
	}

	return nil
}

func runTestMinio(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}
	// Validate Minio configuration
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}

	fmt.Println("Testing Minio connection...")
	fmt.Printf("Endpoint: %s\n", minioConfig.Endpoint)
	fmt.Printf("Bucket: %s\n", minioConfig.Bucket)
	fmt.Printf("Use SSL: %v\n\n", minioConfig.UseSSL)

	// Create a temporary backup manager without SSH client for testing
	backupManager := backup.NewBackupManager(nil, minioConfig)

	// Test connection and perform read/write test
	if err := backupManager.TestMinioConnection(); err != nil {
		return fmt.Errorf("Minio connection test failed: %w", err)
	}

	fmt.Println("✓ Minio connection test successful!")
	return nil
}

func runTestAWS(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}
	// Validate AWS configuration
	awsConfig, err := getAWSConfig(cmd)
	if err != nil {
		return err
	}

	if awsConfig == nil {
		return fmt.Errorf("AWS Glacier vault not configured (set AWS_VAULT environment variable or --aws-vault flag)")
	}

	fmt.Println("Testing AWS Glacier connection...")
	fmt.Printf("Vault: %s\n", awsConfig.Vault)
	fmt.Printf("Account ID: %s\n", awsConfig.AccountID)
	fmt.Printf("Region: %s\n\n", awsConfig.Region)

	// Create a temporary backup manager without SSH client for testing
	backupManager := backup.NewBackupManagerWithAWS(nil, nil, awsConfig)

	// Test connection and perform read/write test
	if err := backupManager.TestAWSConnection(); err != nil {
		return fmt.Errorf("AWS Glacier connection test failed: %w", err)
	}

	fmt.Println("✓ AWS Glacier connection test successful!")
	return nil
}

func runBackupRead(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}
	var objectName string
	if len(args) > 0 {
		objectName = args[0]
	}

	// Validate Minio configuration
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}

	backupManager := backup.NewBackupManager(nil, minioConfig)

	// If object name not provided, optionally resolve latest by prefix
	if objectName == "" {
		latest := mustGetBoolFlag(cmd, "latest")
		prefix := mustGetStringFlag(cmd, "prefix")
		if latest && prefix != "" {
			latestObj, err := backupManager.GetLatestObject(prefix)
			if err != nil {
				return fmt.Errorf("failed to resolve latest object for prefix '%s': %w", prefix, err)
			}
			objectName = latestObj
			fmt.Printf("Resolved latest object: %s\n", objectName)
		} else {
			return fmt.Errorf("object name argument is required unless --latest and --prefix are used")
		}
	}

	outputPath := mustGetStringFlag(cmd, "output")
	// If --save is set and no explicit output path provided, write to cwd using the object's basename
	if mustGetBoolFlag(cmd, "save") && outputPath == "" {
		if objectName == "" {
			return fmt.Errorf("cannot --save when object name is not resolved")
		}
		outputPath = filepath.Base(objectName)
	}

	return backupManager.ReadBackup(objectName, outputPath)
}

func runBackupList(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}
	// Validate Minio configuration
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}

	backupManager := backup.NewBackupManager(nil, minioConfig)

	prefix := mustGetStringFlag(cmd, "prefix")
	limit := 100
	if v, err := cmd.Flags().GetInt("limit"); err == nil {
		limit = v
	}

	objs, err := backupManager.ListBackups(prefix, limit)
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	if len(objs) == 0 {
		fmt.Println("No objects found")
		return nil
	}

	if jsonOut := mustGetBoolFlag(cmd, "json"); jsonOut {
		b, err := json.MarshalIndent(objs, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal objects to JSON: %w", err)
		}
		fmt.Println(string(b))
		return nil
	}

	for _, o := range objs {
		fmt.Printf("%s\t%d\t%s\n", o.Key, o.Size, o.LastModified.Format(time.RFC3339))
	}

	return nil
}

func runBackupDelete(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}

	// Validate Minio configuration
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}

	bm := backup.NewBackupManager(nil, minioConfig)

	var objectName string
	if len(args) > 0 {
		objectName = args[0]
	}

	prefix := mustGetStringFlag(cmd, "prefix")
	latest := mustGetBoolFlag(cmd, "latest")
	deleteAll := mustGetBoolFlag(cmd, "delete-all")
	deleteRange := mustGetStringFlag(cmd, "delete-range")
	deleteRangeByDate := mustGetStringFlag(cmd, "delete-range-by-date")
	skipConfirm := mustGetBoolFlag(cmd, "skip-confirmation")
	dryRun := mustGetBoolFlag(cmd, "dry-run")

	// Validate mutually exclusive flags
	flagCount := 0
	if objectName != "" {
		flagCount++
	}
	if latest {
		flagCount++
	}
	if deleteAll {
		flagCount++
	}
	if deleteRange != "" {
		flagCount++
	}
	if deleteRangeByDate != "" {
		flagCount++
	}
	if flagCount > 1 {
		return fmt.Errorf("only one of: object argument, --latest, --delete-all, --delete-range, or --delete-range-by-date can be specified")
	}

	// Resolve object(s) to delete
	var toDelete []string
	if objectName != "" {
		toDelete = append(toDelete, objectName)
	} else if prefix != "" || deleteAll || deleteRange != "" || deleteRangeByDate != "" {
		limit := 0 // Get all objects for these operations
		if v, err := cmd.Flags().GetInt("limit"); err == nil && !deleteAll && deleteRange == "" && deleteRangeByDate == "" {
			limit = v
		}
		objs, err := bm.ListBackups(prefix, limit)
		if err != nil {
			return fmt.Errorf("failed to list objects for prefix '%s': %w", prefix, err)
		}
		if len(objs) == 0 {
			fmt.Println("No objects found for prefix")
			return nil
		}

		if latest {
			// pick latest
			latestKey := objs[0].Key
			latestTime := objs[0].LastModified
			for _, o := range objs[1:] {
				if o.LastModified.After(latestTime) {
					latestKey = o.Key
					latestTime = o.LastModified
				}
			}
			toDelete = append(toDelete, latestKey)
		} else if deleteAll {
			for _, o := range objs {
				toDelete = append(toDelete, o.Key)
			}
		} else if deleteRange != "" {
			// Parse numeric range and select objects
			start, end, err := bm.ParseNumericRange(deleteRange)
			if err != nil {
				return fmt.Errorf("invalid --delete-range format: %w", err)
			}
			selected, err := bm.SelectObjectsByNumericRange(objs, start, end)
			if err != nil {
				return fmt.Errorf("failed to select objects by range: %w", err)
			}
			for _, o := range selected {
				toDelete = append(toDelete, o.Key)
			}
		} else if deleteRangeByDate != "" {
			// Parse date range and filter objects
			startTime, endTime, err := bm.ParseDateRange(deleteRangeByDate)
			if err != nil {
				return fmt.Errorf("invalid --delete-range-by-date format: %w", err)
			}
			filtered := bm.FilterObjectsByDateRange(objs, startTime, endTime)
			for _, o := range filtered {
				toDelete = append(toDelete, o.Key)
			}
		} else {
			for _, o := range objs {
				toDelete = append(toDelete, o.Key)
			}
		}
	} else {
		return fmt.Errorf("object name argument or --prefix is required")
	}

	// Confirmation
	// If dry-run requested, just preview and exit
	if dryRun {
		fmt.Printf("Dry run: %d object(s) would be deleted:\n", len(toDelete))
		for _, k := range toDelete {
			fmt.Println(" - ", k)
		}
		return nil
	}

	if !skipConfirm {
		fmt.Printf("About to delete %d object(s). Continue? [y/N]: ", len(toDelete))
		var resp string
		if _, err := fmt.Scanln(&resp); err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "y" && resp != "yes" {
			fmt.Println("Aborted by user")
			return nil
		}
	}

	// Perform deletion
	if err := bm.DeleteObjects(toDelete); err != nil {
		return fmt.Errorf("failed to delete objects: %w", err)
	}

	fmt.Printf("Deleted %d object(s)\n", len(toDelete))
	return nil
}

func getMinioConfig(cmd *cobra.Command) (*backup.MinioConfig, error) {
	endpoint := mustGetStringFlag(cmd, "minio-endpoint")
	accessKey := mustGetStringFlag(cmd, "minio-access-key")
	if accessKey == "" {
		accessKey = os.Getenv("MINIO_ACCESS_KEY")
	}
	secretKey := mustGetStringFlag(cmd, "minio-secret-key")
	if secretKey == "" {
		secretKey = os.Getenv("MINIO_SECRET_KEY")
	}
	bucket := mustGetStringFlag(cmd, "minio-bucket")
	useSSL := mustGetBoolFlag(cmd, "minio-ssl")
	bucketPath := mustGetStringFlag(cmd, "bucket-path")
	httpTimeout := mustGetDurationFlag(cmd, "minio-http-timeout")

	if endpoint == "" {
		return nil, fmt.Errorf("minio-endpoint is required (can be set via MINIO_ENDPOINT environment variable)")
	}
	if accessKey == "" {
		return nil, fmt.Errorf("minio-access-key is required (can be set via MINIO_ACCESS_KEY environment variable)")
	}
	if secretKey == "" {
		return nil, fmt.Errorf("minio-secret-key is required (can be set via MINIO_SECRET_KEY environment variable)")
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

func getAWSConfig(cmd *cobra.Command) (*backup.AWSConfig, error) {
	vault := mustGetStringFlag(cmd, "aws-vault")
	accountID := mustGetStringFlag(cmd, "aws-account-id")
	accessKey := mustGetStringFlag(cmd, "aws-access-key")
	if accessKey == "" {
		accessKey = os.Getenv("AWS_ACCESS_KEY")
	}
	secretKey := mustGetStringFlag(cmd, "aws-secret-access-key")
	if secretKey == "" {
		secretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	region := mustGetStringFlag(cmd, "aws-region")
	httpTimeout := mustGetDurationFlag(cmd, "aws-http-timeout")

	// AWS is optional, so only validate if vault is provided
	if vault == "" {
		return nil, nil
	}

	if accessKey == "" {
		return nil, fmt.Errorf("aws-access-key is required when aws-vault is set (can be set via AWS_ACCESS_KEY environment variable)")
	}
	if secretKey == "" {
		return nil, fmt.Errorf("aws-secret-access-key is required when aws-vault is set (can be set via AWS_SECRET_ACCESS_KEY environment variable)")
	}
	if region == "" {
		return nil, fmt.Errorf("aws-region is required when aws-vault is set (can be set via AWS_REGION environment variable)")
	}
	if accountID == "" {
		accountID = "-" // Default to current account
	}

	return &backup.AWSConfig{
		Vault:       vault,
		AccountID:   accountID,
		AccessKey:   accessKey,
		SecretKey:   secretKey,
		Region:      region,
		HTTPTimeout: httpTimeout,
	}, nil
}

func mustGetStringFlag(cmd *cobra.Command, name string) string {
	value, _ := cmd.Flags().GetString(name)
	return value
}

func mustGetBoolFlag(cmd *cobra.Command, name string) bool {
	value, _ := cmd.Flags().GetBool(name)
	return value
}

func mustGetFloat64Flag(cmd *cobra.Command, name string) float64 {
	value, _ := cmd.Flags().GetFloat64(name)
	return value
}

func mustGetDurationFlag(cmd *cobra.Command, name string) time.Duration {
	value, _ := cmd.Flags().GetDuration(name)
	return value
}

func runBackupMonitor(cmd *cobra.Command, args []string) error {
	// Parse flags
	storageServer, _ := cmd.Flags().GetString("storage-server")
	storagePath, _ := cmd.Flags().GetString("storage-path")
	threshold, _ := cmd.Flags().GetFloat64("threshold")
	migratePercent, _ := cmd.Flags().GetFloat64("migrate-percent")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	showMounts, _ := cmd.Flags().GetBool("show-mounts")
	forceDelete, _ := cmd.Flags().GetBool("force-delete")

	// Create SSH client if storage server specified
	var sshClient *auth.SSHClient
	var err error
	if storageServer != "" {
		sshClient, err = createSSHClient(cmd, storageServer)
		if err != nil {
			return fmt.Errorf("failed to connect to storage server %s: %w", storageServer, err)
		}
		defer sshClient.Close()
		fmt.Printf("✓ Connected to storage server: %s\n\n", storageServer)
	}

	// Check if user just wants to see mount points
	if showMounts {
		fmt.Println("===========================================")
		fmt.Println("Filesystem Mount Points")
		fmt.Println("===========================================")

		if sshClient != nil {
			// Remote mount points via SSH
			stdout, stderr, err := sshClient.ExecuteCommand("df -h")
			if err != nil {
				return fmt.Errorf("failed to list mount points on remote server: %w (stderr: %s)", err, stderr)
			}
			fmt.Println(stdout)
		} else {
			// Local mount points
			mounts, err := backup.ListMountPoints()
			if err != nil {
				return fmt.Errorf("failed to list mount points: %w", err)
			}
			fmt.Println(mounts)
		}

		fmt.Println()
		fmt.Println("💡 TIP: Look for your Minio data mount (e.g., /mnt/minio_nyc2)")
		fmt.Println("   Then use: --storage-path /mnt/minio_nyc2")
		return nil
	}

	// Validate storage server is provided
	if storageServer == "" {
		return fmt.Errorf("storage-server is required (use --storage-server or set STORAGE_SERVER_ADDR environment variable)")
	}

	// Get Minio configuration
	minioConfigPtr, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}
	minioConfig := *minioConfigPtr

	// Get AWS configuration (skip if dry-run for validation purposes)
	awsConfig, err := getAWSConfig(cmd)
	if err != nil && !dryRun {
		return err
	}
	if err != nil && dryRun {
		// Allow dry-run without full AWS config for preview purposes
		awsConfig = &backup.AWSConfig{
			Vault:     mustGetStringFlag(cmd, "aws-vault"),
			AccountID: mustGetStringFlag(cmd, "aws-account-id"),
		}
	}

	// Validate required configuration
	if minioConfig.Endpoint == "" {
		return fmt.Errorf("minio-endpoint is required")
	}
	if minioConfig.AccessKey == "" {
		return fmt.Errorf("minio-access-key is required")
	}
	if minioConfig.SecretKey == "" {
		return fmt.Errorf("minio-secret-key is required")
	}
	if awsConfig.Vault == "" {
		return fmt.Errorf("aws-vault is required for migration")
	}
	if !dryRun {
		if awsConfig.AccessKey == "" {
			return fmt.Errorf("aws-access-key is required for migration")
		}
		if awsConfig.SecretKey == "" {
			return fmt.Errorf("aws-secret-access-key is required for migration")
		}
	}

	// Create backup manager with SSH client for remote storage capacity checking
	manager := backup.NewBackupManagerWithAWS(sshClient, &minioConfig, awsConfig)

	// Set verbosity level
	logLevel, _ := cmd.Flags().GetInt("log-level")
	vflag, _ := cmd.Flags().GetCount("vflag")
	verbosity := logLevel
	if vflag > 0 {
		verbosity = 1 + vflag // -v=2, -vv=3, -vvv=4, -vvvv=5
	}
	manager.SetVerbosity(verbosity)

	// Run monitoring and migration
	fmt.Println("===========================================")
	fmt.Println("CIWG Backup Storage Capacity Monitor")
	fmt.Println("===========================================")
	if dryRun {
		fmt.Println("Mode:              🔍 DRY RUN (preview only)")
	} else {
		fmt.Println("Mode:              🚀 LIVE (will perform migrations)")
	}
	fmt.Printf("Storage Server:    %s\n", storageServer)
	fmt.Printf("Storage Path:      %s\n", storagePath)
	// Run monitoring and migration
	fmt.Println("===========================================")
	fmt.Println("CIWG Backup Storage Capacity Monitor")
	fmt.Println("===========================================")
	if dryRun {
		fmt.Println("Mode:              🔍 DRY RUN (preview only)")
	} else {
		fmt.Println("Mode:              🚀 LIVE (will perform migrations)")
	}
	fmt.Printf("Storage Path:      %s\n", storagePath)
	fmt.Printf("Threshold:         %.1f%%\n", threshold)
	fmt.Printf("Migrate Percent:   %.1f%%\n", migratePercent)
	fmt.Printf("Force Delete:      %v\n", forceDelete)
	fmt.Printf("Minio Bucket:      %s\n", minioConfig.Bucket)
	fmt.Printf("AWS Glacier Vault: %s\n", awsConfig.Vault)
	fmt.Println("===========================================")

	return manager.MonitorAndMigrateIfNeeded(storagePath, threshold, migratePercent, dryRun, forceDelete)
}

func runBackupConn(cmd *cobra.Command, args []string) error {
	// Load .env if specified
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}

	fmt.Println("===========================================")
	fmt.Println("Testing Backup System Connections")
	fmt.Println("===========================================")

	// Test Minio
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		fmt.Printf("❌ Minio Configuration Error: %v\n\n", err)
	} else {
		fmt.Println("📦 Testing Minio Connection...")
		fmt.Printf("   Endpoint: %s\n", minioConfig.Endpoint)
		fmt.Printf("   Bucket:   %s\n", minioConfig.Bucket)
		fmt.Printf("   Use SSL:  %v\n\n", minioConfig.UseSSL)

		backupManager := backup.NewBackupManager(nil, minioConfig)
		if err := backupManager.TestMinioConnection(); err != nil {
			fmt.Printf("   ❌ Minio test failed: %v\n\n", err)
		} else {
			fmt.Println("   ✓ Minio connection successful!")
		}
	}

	// Test AWS Glacier
	awsConfig, err := getAWSConfig(cmd)
	if err != nil {
		fmt.Printf("⚠️  AWS Glacier Configuration: %v\n", err)
		fmt.Println("   Skipping AWS Glacier test.")
	} else if awsConfig == nil {
		fmt.Println("⚠️  AWS Glacier not configured.")
		fmt.Println("   Skipping AWS Glacier test.")
	} else {
		fmt.Println("☁️  Testing AWS Glacier Connection...")
		fmt.Printf("   Vault:      %s\n", awsConfig.Vault)
		fmt.Printf("   Account ID: %s\n", awsConfig.AccountID)
		fmt.Printf("   Region:     %s\n\n", awsConfig.Region)

		backupManager := backup.NewBackupManagerWithAWS(nil, nil, awsConfig)
		if err := backupManager.TestAWSConnection(); err != nil {
			fmt.Printf("   ❌ AWS Glacier test failed: %v\n\n", err)
		} else {
			fmt.Println("   ✓ AWS Glacier connection successful!")
		}
	}

	fmt.Println("===========================================")
	fmt.Println("Connection Tests Complete")
	fmt.Println("===========================================")

	return nil
}

func runBackupSanitize(cmd *cobra.Command, args []string) error {
	inputPath := mustGetStringFlag(cmd, "input")
	outputPath := mustGetStringFlag(cmd, "output")
	extractDirStr := mustGetStringFlag(cmd, "extract-dir")
	extractFileStr := mustGetStringFlag(cmd, "extract-file")
	dryRun := mustGetBoolFlag(cmd, "dry-run")

	// Parse comma-separated lists
	var extractDirs []string
	for _, dir := range strings.Split(extractDirStr, ",") {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			extractDirs = append(extractDirs, trimmed)
		}
	}

	var extractFiles []string
	for _, file := range strings.Split(extractFileStr, ",") {
		if trimmed := strings.TrimSpace(file); trimmed != "" {
			extractFiles = append(extractFiles, trimmed)
		}
	}

	// Validate input
	if inputPath == "" {
		return fmt.Errorf("--input is required")
	}
	if outputPath == "" {
		return fmt.Errorf("--output is required")
	}

	// Check if input file exists
	if _, err := os.Stat(inputPath); os.IsNotExist(err) {
		return fmt.Errorf("input file does not exist: %s", inputPath)
	}

	fmt.Println("===========================================")
	fmt.Println("Backup Sanitization")
	fmt.Println("===========================================")
	if dryRun {
		fmt.Println("Mode: 🔍 DRY RUN (preview only)")
	} else {
		fmt.Println("Mode: 🚀 LIVE")
	}
	fmt.Printf("Input:         %s\n", inputPath)
	fmt.Printf("Output:        %s\n", outputPath)
	fmt.Printf("Extract Dirs:  %v\n", extractDirs)
	fmt.Printf("Extract Files: %v\n", extractFiles)
	fmt.Println("===========================================")

	// Create a backup manager (no SSH or Minio needed for sanitization)
	bm := backup.NewBackupManager(nil, nil)

	options := &backup.SanitizeOptions{
		InputPath:    inputPath,
		OutputPath:   outputPath,
		ExtractDirs:  extractDirs,
		ExtractFiles: extractFiles,
		DryRun:       dryRun,
	}

	if err := bm.SanitizeBackup(options); err != nil {
		return fmt.Errorf("sanitization failed: %w", err)
	}

	if dryRun {
		fmt.Println("\n✓ Dry run complete. No changes were made.")
	} else {
		fmt.Printf("\n✓ Sanitization complete! Output: %s\n", outputPath)
	}

	return nil
}

func runBackupMigrateAWS(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}

	// Parse flags
	dryRun := mustGetBoolFlag(cmd, "dry-run")
	prefix := mustGetStringFlag(cmd, "prefix")
	objectKey := mustGetStringFlag(cmd, "object")
	count, _ := cmd.Flags().GetInt("count")
	percent, _ := cmd.Flags().GetFloat64("percent")
	olderThan, _ := cmd.Flags().GetDuration("older-than")
	deleteAfter := mustGetBoolFlag(cmd, "delete-after")
	limit, _ := cmd.Flags().GetInt("limit")

	// Validate mutually exclusive flags
	strategyCount := 0
	if objectKey != "" {
		strategyCount++
	}
	if count > 0 {
		strategyCount++
	}
	if percent > 0 {
		strategyCount++
	}
	if olderThan > 0 {
		strategyCount++
	}

	if strategyCount == 0 {
		return fmt.Errorf("must specify one of: --object, --count, --percent, or --older-than")
	}
	if strategyCount > 1 {
		return fmt.Errorf("only one of --object, --count, --percent, or --older-than can be specified")
	}

	// Get Minio configuration
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}

	// Get AWS configuration
	awsConfig, err := getAWSConfig(cmd)
	if err != nil {
		return err
	}

	if awsConfig == nil {
		return fmt.Errorf("AWS Glacier vault not configured (set AWS_VAULT environment variable or --aws-vault flag)")
	}

	// Validate required AWS configuration
	if !dryRun {
		if awsConfig.AccessKey == "" {
			return fmt.Errorf("aws-access-key is required for migration")
		}
		if awsConfig.SecretKey == "" {
			return fmt.Errorf("aws-secret-access-key is required for migration")
		}
	}

	// Create backup manager
	manager := backup.NewBackupManagerWithAWS(nil, minioConfig, awsConfig)

	// Set verbosity level
	logLevel, _ := cmd.Flags().GetInt("log-level")
	vflag, _ := cmd.Flags().GetCount("vflag")
	verbosity := logLevel
	if vflag > 0 {
		verbosity = 1 + vflag // -v=2, -vv=3, -vvv=4, -vvvv=5
	}
	manager.SetVerbosity(verbosity)

	// Display configuration
	fmt.Println("===========================================")
	fmt.Println("AWS Glacier Manual Migration")
	fmt.Println("===========================================")
	if dryRun {
		fmt.Println("Mode:            🔍 DRY RUN (preview only)")
	} else {
		fmt.Println("Mode:            🚀 LIVE (will migrate)")
	}
	fmt.Printf("Minio Bucket:    %s\n", minioConfig.Bucket)
	fmt.Printf("AWS Vault:       %s\n", awsConfig.Vault)
	fmt.Printf("AWS Region:      %s\n", awsConfig.Region)
	if prefix != "" {
		fmt.Printf("Prefix Filter:   %s\n", prefix)
	}
	if objectKey != "" {
		fmt.Printf("Strategy:        Migrate specific object: %s\n", objectKey)
	} else if count > 0 {
		fmt.Printf("Strategy:        Migrate %d oldest backups\n", count)
	} else if percent > 0 {
		fmt.Printf("Strategy:        Migrate oldest %.1f%% of backups\n", percent)
	} else if olderThan > 0 {
		fmt.Printf("Strategy:        Migrate backups older than %s\n", olderThan)
	}
	if deleteAfter {
		fmt.Println("Delete After:    YES (will delete from Minio after successful migration)")
	} else {
		fmt.Println("Delete After:    NO (will keep in Minio)")
	}
	fmt.Println("===========================================")
	fmt.Println()

	// Select backups to migrate based on strategy
	var toMigrate []backup.ObjectInfo

	// Handle specific object migration
	if objectKey != "" {
		fmt.Printf("Getting object info for: %s\n", objectKey)

		// Get object info via StatObject
		objs, err := manager.ListBackups(objectKey, 1)
		if err != nil {
			return fmt.Errorf("failed to get object info: %w", err)
		}

		if len(objs) == 0 {
			return fmt.Errorf("object not found: %s", objectKey)
		}

		// Verify exact match (ListBackups may return prefix matches)
		if objs[0].Key != objectKey {
			return fmt.Errorf("object not found: %s (got prefix match: %s)", objectKey, objs[0].Key)
		}

		toMigrate = objs
		fmt.Printf("Found object: %s (%.2f MB, %s)\n\n",
			objs[0].Key,
			float64(objs[0].Size)/(1024*1024),
			objs[0].LastModified.Format("2006-01-02 15:04:05"))
	} else {
		// List backups from Minio
		fmt.Println("Fetching backups from Minio...")
		objs, err := manager.ListBackups(prefix, limit)
		if err != nil {
			return fmt.Errorf("failed to list backups: %w", err)
		}

		if len(objs) == 0 {
			fmt.Println("No backups found matching criteria.")
			return nil
		}

		fmt.Printf("Found %d backup(s) in Minio\n\n", len(objs))

		// Select based on strategy
		if count > 0 {
			// Migrate N oldest backups
			if count > len(objs) {
				count = len(objs)
			}
			toMigrate = objs[:count]
		} else if percent > 0 {
			// Migrate oldest N% of backups
			numToMigrate := int(float64(len(objs)) * percent / 100.0)
			if numToMigrate < 1 {
				numToMigrate = 1
			}
			if numToMigrate > len(objs) {
				numToMigrate = len(objs)
			}
			toMigrate = objs[:numToMigrate]
		} else if olderThan > 0 {
			// Migrate backups older than duration
			cutoffTime := time.Now().Add(-olderThan)
			for _, obj := range objs {
				if obj.LastModified.Before(cutoffTime) {
					toMigrate = append(toMigrate, obj)
				}
			}
		}
	}

	if len(toMigrate) == 0 {
		fmt.Println("No backups match the migration criteria.")
		return nil
	}

	// Display migration plan
	fmt.Printf("Selected %d backup(s) for migration:\n", len(toMigrate))
	fmt.Println("-------------------------------------------")
	var totalSize int64
	for i, obj := range toMigrate {
		fmt.Printf("%3d. %s (%.2f MB, %s)\n",
			i+1,
			obj.Key,
			float64(obj.Size)/(1024*1024),
			obj.LastModified.Format("2006-01-02 15:04:05"))
		totalSize += obj.Size
	}
	fmt.Println("-------------------------------------------")
	fmt.Printf("Total size to migrate: %.2f MB\n\n", float64(totalSize)/(1024*1024))

	if dryRun {
		fmt.Println("✓ Dry run complete. No backups were migrated.")
		return nil
	}

	// Perform migration
	fmt.Println("Starting migration...")
	var migratedCount, failedCount int
	var migratedSize int64

	for i, obj := range toMigrate {
		fmt.Printf("\n[%d/%d] Migrating: %s (%.2f MB)\n", i+1, len(toMigrate), obj.Key, float64(obj.Size)/(1024*1024))

		// Download from Minio
		reader, err := manager.DownloadBackup(obj.Key)
		if err != nil {
			fmt.Printf("   ❌ Failed to download from Minio: %v\n", err)
			failedCount++
			continue
		}

		// Upload to AWS Glacier
		err = manager.UploadToAWS(obj.Key, reader, obj.Size)
		if err != nil {
			fmt.Printf("   ❌ Failed to upload to AWS Glacier: %v\n", err)
			failedCount++
			continue
		}

		migratedCount++
		migratedSize += obj.Size

		// Delete from Minio if requested
		if deleteAfter {
			fmt.Printf("   Deleting from Minio...\n")
			if err := manager.DeleteObjects([]string{obj.Key}); err != nil {
				fmt.Printf("   ⚠️  Failed to delete from Minio: %v\n", err)
			} else {
				fmt.Printf("   ✓ Deleted from Minio\n")
			}
		}

		fmt.Printf("   ✓ Migration complete\n")
	}

	// Summary
	fmt.Println("\n===========================================")
	fmt.Println("Migration Summary")
	fmt.Println("===========================================")
	fmt.Printf("Total backups:     %d\n", len(toMigrate))
	fmt.Printf("Migrated:          %d (%.2f MB)\n", migratedCount, float64(migratedSize)/(1024*1024))
	fmt.Printf("Failed:            %d\n", failedCount)
	if deleteAfter {
		fmt.Printf("Deleted from Minio: %d\n", migratedCount)
	}
	fmt.Println("===========================================")

	if failedCount > 0 {
		return fmt.Errorf("%d backup(s) failed to migrate", failedCount)
	}

	return nil
}

func runBackupEstimateCapacity(cmd *cobra.Command, args []string) error {
	// Load .env if specified
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}

	// Parse flags
	serverRange := mustGetStringFlag(cmd, "server-range")
	estimateMethod := mustGetStringFlag(cmd, "estimate-method")
	sampleSize, _ := cmd.Flags().GetInt64("sample-size")
	fromBackup := mustGetStringFlag(cmd, "from-backup")
	avgSizeStr := mustGetStringFlag(cmd, "avg-compressed-size")
	siteCount, _ := cmd.Flags().GetInt("site-count")
	dailyRetention, _ := cmd.Flags().GetInt("daily-retention")
	weeklyRetention, _ := cmd.Flags().GetInt("weekly-retention")
	monthlyRetention, _ := cmd.Flags().GetInt("monthly-retention")
	estimateFocus := mustGetStringFlag(cmd, "estimate-focus")
	estimateType := mustGetStringFlag(cmd, "estimate-type")
	outputFormat := mustGetStringFlag(cmd, "output")
	growthRate, _ := cmd.Flags().GetFloat64("growth-rate")
	projectionMonths, _ := cmd.Flags().GetInt("projection-months")
	bufferPercent, _ := cmd.Flags().GetFloat64("buffer-percent")
	glacierPrice, _ := cmd.Flags().GetFloat64("aws-glacier-price")
	retrievalPrice, _ := cmd.Flags().GetFloat64("aws-retrieval-price")
	parentDir := mustGetStringFlag(cmd, "container-parent-dir")
	availableStorageStr := mustGetStringFlag(cmd, "available-storage")

	// Parse available storage if provided
	var availableStorageGB float64
	if availableStorageStr != "" {
		sizeBytes, err := parseSize(availableStorageStr)
		if err != nil {
			return fmt.Errorf("invalid --available-storage: %w", err)
		}
		availableStorageGB = float64(sizeBytes) / (1024 * 1024 * 1024)
	}

	// Validate estimate-focus
	if estimateFocus != "growth-modeling" && estimateFocus != "static-capacity" && estimateFocus != "all" {
		return fmt.Errorf("invalid --estimate-focus: %s (must be 'growth-modeling', 'static-capacity', or 'all')", estimateFocus)
	}

	// Validate estimate-type
	if estimateType != "cost" && estimateType != "size" && estimateType != "all" {
		return fmt.Errorf("invalid --estimate-type: %s (must be 'cost', 'size', or 'all')", estimateType)
	}

	// Validate output format
	if outputFormat != "stdout" && outputFormat != "json" && outputFormat != "csv" {
		return fmt.Errorf("invalid --output: %s (must be 'stdout', 'json', or 'csv')", outputFormat)
	}

	// Determine data source
	var hostname string
	if len(args) > 0 {
		hostname = args[0]
	}

	// Validate input methods
	inputCount := 0
	if hostname != "" || serverRange != "" {
		inputCount++
	}
	if fromBackup != "" {
		inputCount++
	}
	if avgSizeStr != "" {
		inputCount++
	}

	if inputCount == 0 {
		return fmt.Errorf("must specify one data source: hostname/--server-range, --from-backup, or --avg-compressed-size")
	}
	if inputCount > 1 {
		return fmt.Errorf("only one data source can be specified at a time")
	}

	// Validate manual input
	if avgSizeStr != "" && siteCount == 0 {
		return fmt.Errorf("--site-count is required when using --avg-compressed-size")
	}

	// Create capacity options
	capacityOpts := &backup.CapacityEstimateOptions{
		DailyRetention:      dailyRetention,
		WeeklyRetention:     weeklyRetention,
		MonthlyRetention:    monthlyRetention,
		GrowthRate:          growthRate,
		ProjectionMonths:    projectionMonths,
		BufferPercent:       bufferPercent,
		GlacierPricePerGB:   glacierPrice,
		RetrievalPricePerGB: retrievalPrice,
	}

	var estimate *backup.CapacityEstimate
	var err error

	// Process based on data source
	if avgSizeStr != "" {
		// Manual input mode
		avgSize, parseErr := parseSize(avgSizeStr)
		if parseErr != nil {
			return fmt.Errorf("invalid --avg-compressed-size format: %w", parseErr)
		}

		// Create a minimal backup manager (no SSH/Minio needed for manual calc)
		manager := backup.NewBackupManager(nil, nil)
		estimate, err = manager.EstimateCapacityFromManual(avgSize, siteCount, capacityOpts)
		if err != nil {
			return fmt.Errorf("capacity estimation failed: %w", err)
		}

	} else if fromBackup != "" {
		// From existing backup mode
		minioConfig, cfgErr := getMinioConfig(cmd)
		if cfgErr != nil {
			return fmt.Errorf("Minio configuration required for --from-backup: %w", cfgErr)
		}

		manager := backup.NewBackupManager(nil, minioConfig)
		estimate, err = manager.EstimateCapacityFromBackup(fromBackup, siteCount, capacityOpts)
		if err != nil {
			return fmt.Errorf("capacity estimation from backup failed: %w", err)
		}

	} else {
		// Live scanning mode (hostname or server-range)
		if hostname != "" {
			// Single server scan
			sshClient, sshErr := createSSHClient(cmd, hostname)
			if sshErr != nil {
				return fmt.Errorf("failed to connect to %s: %w", hostname, sshErr)
			}
			defer sshClient.Close()

			manager := backup.NewBackupManager(sshClient, nil)

			// Get containers
			containers, containerErr := manager.GetContainersFromOptions(&backup.BackupOptions{
				ParentDir: parentDir,
			})
			if containerErr != nil {
				return fmt.Errorf("failed to get containers: %w", containerErr)
			}

			estimate, err = manager.EstimateCapacityFromScan(containers, estimateMethod, sampleSize, capacityOpts)
			if err != nil {
				return fmt.Errorf("capacity estimation failed: %w", err)
			}

		} else {
			// Server range scan
			estimate, err = processCapacityEstimateForServerRange(cmd, serverRange, estimateMethod, sampleSize, parentDir, capacityOpts, outputFormat)
			if err != nil {
				return err
			}
		}
	}

	// Output results based on format
	switch outputFormat {
	case "json":
		return outputCapacityJSON(estimate)
	case "csv":
		return outputCapacityCSV(estimate, estimateFocus, estimateType)
	default:
		if err := outputCapacityStdout(estimate, estimateFocus, estimateType); err != nil {
			return err
		}
		// Output recommendations if available storage was specified
		outputCapacityRecommendations(estimate, availableStorageGB)
		return nil
	}
}

// processCapacityEstimateForServerRange handles server range processing
func processCapacityEstimateForServerRange(cmd *cobra.Command, serverRange, estimateMethod string, sampleSize int64, parentDir string, options *backup.CapacityEstimateOptions, outputFormat string) (*backup.CapacityEstimate, error) {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return nil, err
	}

	// Collect estimates from each server
	var serverEstimates []*backup.CapacityEstimate
	var allSites []backup.SiteEstimate
	totalServers := 0
	successfulServers := 0
	totalContainers := 0

	// Suppress progress output for JSON/CSV formats
	quiet := outputFormat == "json" || outputFormat == "csv"

	if !quiet {
		fmt.Printf("🌐 Scanning server range: %s\n\n", serverRange)
	}

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}
		totalServers++

		hostname := fmt.Sprintf(pattern, i)
		if !quiet {
			fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
			fmt.Printf("Server: %s\n", hostname)
			fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
		}

		sshClient, err := createSSHClient(cmd, hostname)
		if err != nil {
			if !quiet {
				fmt.Printf("⚠️  Failed to connect to %s: %v\n\n", hostname, err)
			}
			continue
		}

		manager := backup.NewBackupManager(sshClient, nil)
		containers, err := manager.GetContainersFromOptions(&backup.BackupOptions{
			ParentDir: parentDir,
		})

		if err != nil {
			if !quiet {
				fmt.Printf("⚠️  Failed to get containers from %s: %v\n\n", hostname, err)
			}
			sshClient.Close()
			continue
		}

		if len(containers) == 0 {
			if !quiet {
				fmt.Printf("ℹ️  No containers found on %s\n\n", hostname)
			}
			sshClient.Close()
			continue
		}

		if !quiet {
			fmt.Printf("Found %d container(s) on %s\n\n", len(containers), hostname)
		}

		// Scan this server's containers
		estimate, err := manager.EstimateCapacityFromScan(containers, estimateMethod, sampleSize, options)
		sshClient.Close()

		if err != nil {
			if !quiet {
				fmt.Printf("⚠️  Failed to estimate capacity for %s: %v\n\n", hostname, err)
			}
			continue
		}

		serverEstimates = append(serverEstimates, estimate)
		allSites = append(allSites, estimate.Sites...)
		totalContainers += len(containers)
		successfulServers++

		// Show server summary
		if !quiet {
			fmt.Printf("Server %s Summary:\n", hostname)
			fmt.Printf("  Sites: %d, Avg compressed: %.2f MB\n",
				len(estimate.Sites),
				float64(estimate.AvgCompressedSize)/(1024*1024))
			fmt.Printf("  Server total: %.2f GB compressed\n\n",
				float64(estimate.AvgCompressedSize*int64(len(estimate.Sites)))/(1024*1024*1024))
		}
	}

	if successfulServers == 0 {
		return nil, fmt.Errorf("failed to scan any servers (tried %d)", totalServers)
	}

	if !quiet {
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Printf("📊 FLEET-WIDE AGGREGATION\n")
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
		fmt.Printf("Successfully scanned: %d/%d servers, %d total containers\n\n", successfulServers, totalServers, totalContainers)
	}

	// Aggregate all server estimates into one combined result
	combinedEstimate := &backup.CapacityEstimate{
		EstimationMethod:    estimateMethod,
		SitesScanned:        len(allSites),
		DailyRetention:      options.DailyRetention,
		WeeklyRetention:     options.WeeklyRetention,
		MonthlyRetention:    options.MonthlyRetention,
		TotalBackupsPerSite: options.DailyRetention + options.WeeklyRetention + options.MonthlyRetention,
		BufferPercent:       options.BufferPercent,
		Sites:               allSites,
	}

	// Calculate combined totals
	var totalCompressed int64
	var totalUncompressed int64
	for _, site := range allSites {
		totalCompressed += site.CompressedSize
		totalUncompressed += site.UncompressedSize
	}

	if len(allSites) > 0 {
		combinedEstimate.AvgCompressedSize = totalCompressed / int64(len(allSites))
		combinedEstimate.AvgUncompressedSize = totalUncompressed / int64(len(allSites))
		if combinedEstimate.AvgUncompressedSize > 0 {
			combinedEstimate.AvgCompressionRatio = (1.0 - float64(combinedEstimate.AvgCompressedSize)/float64(combinedEstimate.AvgUncompressedSize)) * 100
		}
	}

	// Calculate per-site storage requirements
	combinedEstimate.PerSiteHotStorage = combinedEstimate.AvgCompressedSize * int64(options.DailyRetention)
	combinedEstimate.PerSiteColdStorage = combinedEstimate.AvgCompressedSize * int64(options.WeeklyRetention+options.MonthlyRetention)
	combinedEstimate.PerSiteTotalStorage = combinedEstimate.PerSiteHotStorage + combinedEstimate.PerSiteColdStorage

	// Calculate fleet-wide storage
	combinedEstimate.FleetHotStorage = combinedEstimate.PerSiteHotStorage * int64(len(allSites))
	combinedEstimate.FleetColdStorage = combinedEstimate.PerSiteColdStorage * int64(len(allSites))
	combinedEstimate.FleetTotalStorage = combinedEstimate.FleetHotStorage + combinedEstimate.FleetColdStorage

	// Add buffer
	combinedEstimate.FleetTotalWithBuffer = int64(float64(combinedEstimate.FleetTotalStorage) * (1.0 + options.BufferPercent/100.0))

	// Calculate growth projections if growth rate specified
	if options.GrowthRate > 0 && options.ProjectionMonths > 0 {
		combinedEstimate.GrowthProjections = calculateGrowthProjections(
			combinedEstimate.FleetHotStorage,
			combinedEstimate.FleetColdStorage,
			options.GrowthRate,
			options.ProjectionMonths,
			options.GlacierPricePerGB,
		)
	}

	// Calculate costs if price specified
	if options.GlacierPricePerGB > 0 {
		coldStorageGB := float64(combinedEstimate.FleetColdStorage) / (1024 * 1024 * 1024)
		combinedEstimate.MonthlyCost = coldStorageGB * options.GlacierPricePerGB

		if options.RetrievalPricePerGB > 0 {
			combinedEstimate.RetrievalCost10Pct = coldStorageGB * 0.10 * options.RetrievalPricePerGB
		}
	}

	return combinedEstimate, nil
}

// calculateGrowthProjections computes storage growth projections
func calculateGrowthProjections(hotStorage, coldStorage int64, growthRate float64, months int, glacierPrice float64) []backup.GrowthProjection {
	projections := make([]backup.GrowthProjection, 0, months)

	currentTotal := float64(hotStorage + coldStorage)
	growthMultiplier := 1.0 + (growthRate / 100.0)

	for month := 1; month <= months; month++ {
		currentTotal *= growthMultiplier
		totalGB := currentTotal / (1024 * 1024 * 1024)

		// Assume same hot/cold ratio
		ratio := float64(coldStorage) / float64(hotStorage+coldStorage)
		coldGB := totalGB * ratio
		hotGB := totalGB * (1.0 - ratio)

		projection := backup.GrowthProjection{
			Month:          month,
			TotalStorageGB: totalGB,
			HotStorageGB:   hotGB,
			ColdStorageGB:  coldGB,
		}

		if glacierPrice > 0 {
			projection.MonthlyCost = coldGB * glacierPrice
		}

		projections = append(projections, projection)
	}

	return projections
}

// parseSize parses size strings like "125MB", "1.5GB" into bytes
func parseSize(sizeStr string) (int64, error) {
	sizeStr = strings.TrimSpace(strings.ToUpper(sizeStr))

	multiplier := int64(1)
	if strings.HasSuffix(sizeStr, "GB") {
		multiplier = 1024 * 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "GB")
	} else if strings.HasSuffix(sizeStr, "MB") {
		multiplier = 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "MB")
	} else if strings.HasSuffix(sizeStr, "KB") {
		multiplier = 1024
		sizeStr = strings.TrimSuffix(sizeStr, "KB")
	} else if strings.HasSuffix(sizeStr, "B") {
		sizeStr = strings.TrimSuffix(sizeStr, "B")
	}

	value, err := strconv.ParseFloat(sizeStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size value: %s", sizeStr)
	}

	return int64(value * float64(multiplier)), nil
}

// outputCapacityJSON outputs estimate as JSON
func outputCapacityJSON(estimate *backup.CapacityEstimate) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(estimate)
}

// outputCapacityCSV outputs estimate as CSV
func outputCapacityCSV(estimate *backup.CapacityEstimate, focus, estimateType string) error {
	writer := csv.NewWriter(os.Stdout)
	defer writer.Flush()

	// Write header
	if err := writer.Write([]string{"Metric", "Value", "Unit"}); err != nil {
		return err
	}

	// Basic info
	writeCSVRow := func(metric, value, unit string) error {
		return writer.Write([]string{metric, value, unit})
	}

	writeCSVRow("Estimation Method", estimate.EstimationMethod, "")
	writeCSVRow("Sites Scanned", fmt.Sprintf("%d", estimate.SitesScanned), "sites")
	writeCSVRow("Daily Retention", fmt.Sprintf("%d", estimate.DailyRetention), "days")
	writeCSVRow("Weekly Retention", fmt.Sprintf("%d", estimate.WeeklyRetention), "weeks")
	writeCSVRow("Monthly Retention", fmt.Sprintf("%d", estimate.MonthlyRetention), "months")
	writeCSVRow("Total Backups Per Site", fmt.Sprintf("%d", estimate.TotalBackupsPerSite), "backups")

	if focus == "static-capacity" || focus == "all" {
		if estimateType == "size" || estimateType == "all" {
			// Storage sizes
			writeCSVRow("Avg Compressed Size", fmt.Sprintf("%.2f", float64(estimate.AvgCompressedSize)/(1024*1024)), "MB")
			writeCSVRow("Per-Site Hot Storage", fmt.Sprintf("%.2f", float64(estimate.PerSiteHotStorage)/(1024*1024)), "MB")
			writeCSVRow("Per-Site Cold Storage", fmt.Sprintf("%.2f", float64(estimate.PerSiteColdStorage)/(1024*1024)), "MB")
			writeCSVRow("Per-Site Total", fmt.Sprintf("%.2f", float64(estimate.PerSiteTotalStorage)/(1024*1024)), "MB")
			writeCSVRow("Fleet Hot Storage", fmt.Sprintf("%.2f", float64(estimate.FleetHotStorage)/(1024*1024*1024)), "GB")
			writeCSVRow("Fleet Cold Storage", fmt.Sprintf("%.2f", float64(estimate.FleetColdStorage)/(1024*1024*1024)), "GB")
			writeCSVRow("Fleet Total", fmt.Sprintf("%.2f", float64(estimate.FleetTotalStorage)/(1024*1024*1024)), "GB")
			writeCSVRow("Fleet Total With Buffer", fmt.Sprintf("%.2f", float64(estimate.FleetTotalWithBuffer)/(1024*1024*1024)), "GB")
		}

		if estimateType == "cost" || estimateType == "all" {
			writeCSVRow("Monthly Storage Cost", fmt.Sprintf("%.2f", estimate.MonthlyCost), "USD")
			writeCSVRow("Retrieval Cost (10%)", fmt.Sprintf("%.2f", estimate.RetrievalCost10Pct), "USD")
		}
	}

	// Growth projections
	if (focus == "growth-modeling" || focus == "all") && len(estimate.GrowthProjections) > 0 {
		writer.Write([]string{}) // Blank line
		writer.Write([]string{"Growth Projections", "", ""})
		writer.Write([]string{"Month", "Total Storage (GB)", "Monthly Cost (USD)"})

		for _, proj := range estimate.GrowthProjections {
			costStr := ""
			if proj.MonthlyCost > 0 {
				costStr = fmt.Sprintf("%.2f", proj.MonthlyCost)
			}
			writer.Write([]string{
				fmt.Sprintf("%d", proj.Month),
				fmt.Sprintf("%.2f", proj.TotalStorageGB),
				costStr,
			})
		}
	}

	return nil
}

// outputCapacityStdout outputs estimate to terminal
func outputCapacityStdout(estimate *backup.CapacityEstimate, focus, estimateType string) error {
	fmt.Println("===========================================")
	fmt.Println("Backup Capacity Estimation")
	fmt.Println("===========================================")
	fmt.Printf("Estimation Method:  %s\n", estimate.EstimationMethod)
	fmt.Printf("Sites Analyzed:     %d\n", estimate.SitesScanned)
	fmt.Println()

	fmt.Println("Retention Policy:")
	fmt.Printf("  Daily backups:    %d days\n", estimate.DailyRetention)
	fmt.Printf("  Weekly backups:   %d weeks\n", estimate.WeeklyRetention)
	fmt.Printf("  Monthly backups:  %d months\n", estimate.MonthlyRetention)
	fmt.Printf("  Total per site:   %d backups\n", estimate.TotalBackupsPerSite)
	fmt.Println()

	if focus == "static-capacity" || focus == "all" {
		if estimateType == "size" || estimateType == "all" {
			// Calculate total baseline (single backup of all sites)
			totalBaselineUncompressed := estimate.AvgUncompressedSize * int64(estimate.SitesScanned)
			totalBaselineCompressed := estimate.AvgCompressedSize * int64(estimate.SitesScanned)

			fmt.Println("Baseline Measurements (per site average):")
			if estimate.AvgUncompressedSize > 0 {
				fmt.Printf("  Avg uncompressed:  %.2f MB\n", float64(estimate.AvgUncompressedSize)/(1024*1024))
			}
			fmt.Printf("  Avg compressed:    %.2f MB\n", float64(estimate.AvgCompressedSize)/(1024*1024))
			if estimate.AvgCompressionRatio > 0 {
				fmt.Printf("  Compression ratio: %.1f%% saved\n", estimate.AvgCompressionRatio)
			}
			fmt.Println()

			fmt.Printf("Total Baseline Size (1 backup of all %d sites):\n", estimate.SitesScanned)
			if totalBaselineUncompressed > 0 {
				fmt.Printf("  Total uncompressed: %.2f GB\n", float64(totalBaselineUncompressed)/(1024*1024*1024))
			}
			fmt.Printf("  Total compressed:   %.2f GB\n", float64(totalBaselineCompressed)/(1024*1024*1024))
			fmt.Println()

			fmt.Println("Per-Site Storage Requirements:")
			fmt.Printf("  Hot storage (Minio):  %.2f GB (%d daily backups)\n",
				float64(estimate.PerSiteHotStorage)/(1024*1024*1024),
				estimate.DailyRetention)
			fmt.Printf("  Cold storage (AWS):   %.2f GB (%d weekly + %d monthly)\n",
				float64(estimate.PerSiteColdStorage)/(1024*1024*1024),
				estimate.WeeklyRetention,
				estimate.MonthlyRetention)
			fmt.Printf("  Total per site:       %.2f GB\n",
				float64(estimate.PerSiteTotalStorage)/(1024*1024*1024))
			fmt.Println()

			fmt.Printf("Fleet-Wide Storage (%d sites):\n", estimate.SitesScanned)
			fmt.Printf("  Hot storage (Minio):  %.2f GB\n", float64(estimate.FleetHotStorage)/(1024*1024*1024))
			fmt.Printf("  Cold storage (AWS):   %.2f GB\n", float64(estimate.FleetColdStorage)/(1024*1024*1024))
			fmt.Printf("  Total required:       %.2f GB\n", float64(estimate.FleetTotalStorage)/(1024*1024*1024))
			fmt.Printf("  With %.0f%% buffer:      %.2f GB\n",
				estimate.BufferPercent,
				float64(estimate.FleetTotalWithBuffer)/(1024*1024*1024))
			fmt.Println()
		}

		if estimateType == "cost" || estimateType == "all" {
			if estimate.MonthlyCost > 0 {
				fmt.Println("Cost Estimates (AWS Glacier):")
				fmt.Printf("  Monthly storage:      $%.2f\n", estimate.MonthlyCost)
				if estimate.RetrievalCost10Pct > 0 {
					fmt.Printf("  Retrieval (10%%/mo):  $%.2f\n", estimate.RetrievalCost10Pct)
				}
				fmt.Println()
			}
		}
	}

	if (focus == "growth-modeling" || focus == "all") && len(estimate.GrowthProjections) > 0 {
		fmt.Println("Growth Projections:")
		fmt.Println("  Month | Total Storage | Hot Storage | Cold Storage | Monthly Cost")
		fmt.Println("  ------|---------------|-------------|--------------|-------------")

		for _, proj := range estimate.GrowthProjections {
			costStr := "N/A"
			if proj.MonthlyCost > 0 {
				costStr = fmt.Sprintf("$%.2f", proj.MonthlyCost)
			}
			fmt.Printf("  %5d | %10.2f GB | %8.2f GB | %9.2f GB | %s\n",
				proj.Month,
				proj.TotalStorageGB,
				proj.HotStorageGB,
				proj.ColdStorageGB,
				costStr)
		}
		fmt.Println()
	}

	// Per-site breakdown if available and not too many
	if len(estimate.Sites) > 0 && len(estimate.Sites) <= 10 {
		fmt.Println("Per-Site Breakdown:")
		fmt.Println("  Site | Compressed | Hot Storage | Cold Storage | Total")
		fmt.Println("  -----|------------|-------------|--------------|-------")

		for _, site := range estimate.Sites {
			fmt.Printf("  %-25s | %7.2f MB | %8.2f MB | %9.2f MB | %.2f MB\n",
				site.SiteName,
				float64(site.CompressedSize)/(1024*1024),
				float64(site.HotStorageSize)/(1024*1024),
				float64(site.ColdStorageSize)/(1024*1024),
				float64(site.TotalStorageSize)/(1024*1024))
		}
		fmt.Println()
	} else if len(estimate.Sites) > 10 {
		fmt.Printf("ℹ️  %d sites analyzed (use --output json for full per-site breakdown)\n\n", len(estimate.Sites))
	}

	return nil
}

func outputCapacityRecommendations(estimate *backup.CapacityEstimate, availableStorageGB float64) {
	if availableStorageGB <= 0 {
		return
	}

	requiredHotStorageGB := float64(estimate.FleetHotStorage) / (1024 * 1024 * 1024)

	fmt.Println("===========================================")
	fmt.Println("Storage Analysis & Recommendations")
	fmt.Println("===========================================")
	fmt.Printf("Available Minio Storage: %.2f GB\n", availableStorageGB)
	fmt.Printf("Required Hot Storage:    %.2f GB (%d daily backups)\n",
		requiredHotStorageGB, estimate.DailyRetention)
	fmt.Println()

	// Calculate shortfall
	shortfall := requiredHotStorageGB - availableStorageGB
	utilizationPct := (requiredHotStorageGB / availableStorageGB) * 100

	if shortfall > 0 {
		// INSUFFICIENT CAPACITY
		fmt.Printf("⚠️  CRITICAL: Storage shortfall of %.2f GB (%.1fx over capacity)\n\n",
			shortfall, requiredHotStorageGB/availableStorageGB)

		fmt.Println("📋 Recommended Actions (choose one or combine):")
		fmt.Println()

		// Option 1: Reduce retention
		for _, days := range []int{7, 5, 3} {
			reducedHot := float64(estimate.AvgCompressedSize*int64(days)*int64(estimate.SitesScanned)) / (1024 * 1024 * 1024)
			if reducedHot <= availableStorageGB {
				fmt.Printf("1️⃣  REDUCE RETENTION to %d daily backups\n", days)
				fmt.Printf("   Required: %.2f GB (%.1f%% of available)\n", reducedHot, (reducedHot/availableStorageGB)*100)
				fmt.Printf("   Trade-off: Less recovery granularity\n")
				fmt.Printf("   Command: --daily-retention %d\n", days)
				fmt.Println()
				break
			}
		}

		// Option 2: Faster glacier migration
		for _, days := range []int{7, 5, 3, 2, 1} {
			if days < estimate.DailyRetention {
				reducedHot := float64(estimate.AvgCompressedSize*int64(days)*int64(estimate.SitesScanned)) / (1024 * 1024 * 1024)
				if reducedHot <= availableStorageGB {
					fmt.Printf("2️⃣  MIGRATE FASTER to Glacier (keep %d days hot, rest in Glacier)\n", days)
					fmt.Printf("   Required: %.2f GB (%.1f%% of available)\n", reducedHot, (reducedHot/availableStorageGB)*100)
					fmt.Printf("   Trade-off: Higher retrieval costs if needed\n")
					fmt.Printf("   Action: Run monitor more frequently, migrate after %d days\n", days)
					fmt.Println()
					break
				}
			}
		}

		// Option 3: Expand storage
		recommendedExpansion := requiredHotStorageGB * 1.2 // 20% buffer
		fmt.Printf("3️⃣  EXPAND STORAGE to %.0f GB minimum\n", recommendedExpansion)
		fmt.Printf("   Additional needed: %.0f GB\n", recommendedExpansion-availableStorageGB)
		fmt.Printf("   Benefit: Maintain current %d-day retention policy\n", estimate.DailyRetention)
		fmt.Printf("   For future growth: Consider %.0f GB\n", recommendedExpansion*1.5)
		fmt.Println()

		// Option 4: Reduce site count (less common)
		if estimate.SitesScanned > 100 {
			maxSites := int(availableStorageGB / (float64(estimate.AvgCompressedSize*int64(estimate.DailyRetention)) / (1024 * 1024 * 1024)))
			fmt.Printf("4️⃣  REDUCE ACTIVE SITES to ~%d sites\n", maxSites)
			fmt.Printf("   Archive/disable %d sites\n", estimate.SitesScanned-maxSites)
			fmt.Printf("   Trade-off: Service fewer sites\n")
			fmt.Println()
		}

	} else if utilizationPct > 80 {
		// WARNING: High utilization
		fmt.Printf("⚠️  WARNING: High storage utilization (%.1f%% of capacity)\n\n", utilizationPct)
		fmt.Println("📋 Recommendations:")
		fmt.Println()
		fmt.Printf("  • Monitor storage closely - only %.2f GB headroom\n", availableStorageGB-requiredHotStorageGB)
		fmt.Printf("  • Plan for expansion if growth rate >%.1f%% monthly\n", (20.0/utilizationPct)*100)
		fmt.Printf("  • Consider migrating to Glacier after %d days instead of %d\n",
			estimate.DailyRetention-3, estimate.DailyRetention)
		fmt.Println()

	} else {
		// SUFFICIENT CAPACITY
		fmt.Printf("✓ SUFFICIENT: Storage utilization at %.1f%% of capacity\n\n", utilizationPct)
		headroom := availableStorageGB - requiredHotStorageGB
		monthsOfGrowth := 0
		if estimate.GrowthProjections != nil && len(estimate.GrowthProjections) > 0 {
			for i, proj := range estimate.GrowthProjections {
				if proj.HotStorageGB > availableStorageGB {
					monthsOfGrowth = i
					break
				}
			}
		}

		fmt.Println("📊 Capacity Summary:")
		fmt.Printf("  • Headroom: %.2f GB (%.1f%% free)\n", headroom, ((availableStorageGB-requiredHotStorageGB)/availableStorageGB)*100)
		if monthsOfGrowth > 0 {
			fmt.Printf("  • Growth capacity: ~%d months at current rate\n", monthsOfGrowth)
			fmt.Printf("  • Review capacity in %d months\n", monthsOfGrowth-3)
		}
		fmt.Printf("  • Current policy sustainable: %d daily + %d weekly + %d monthly\n",
			estimate.DailyRetention, estimate.WeeklyRetention, estimate.MonthlyRetention)
		fmt.Println()
	}
}
