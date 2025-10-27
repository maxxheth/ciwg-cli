package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBackupCreate,
}

var backupTestMinioCmd = &cobra.Command{
	Use:   "test-minio",
	Short: "Test Minio connection and perform read/write test",
	Long:  `Test the connection to Minio storage and perform a basic read/write test to verify bucket access.`,
	RunE:  runTestMinio,
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
	backupCmd.AddCommand(backupReadCmd)
	backupCmd.AddCommand(backupListCmd)

	// Backup creation flags
	backupCreateCmd.Flags().Bool("dry-run", false, "Print actions without executing them")
	backupCreateCmd.Flags().Bool("delete", false, "Stop and remove containers, and delete associated directories after backup")
	backupCreateCmd.Flags().String("container-name", "", "Pipe-delimited container names or working directories to process (e.g. wp_foo|wp_bar|/srv/foo)")
	backupCreateCmd.Flags().String("container-names", "", "Comma-delimited container names to process (e.g. wp_foo,wp_bar)")
	backupCreateCmd.Flags().Bool("local", false, "Run backups locally using host's Docker instead of SSH")
	backupCreateCmd.Flags().String("container-file", "", "File with newline-delimited container names or working directories to process")
	backupCreateCmd.Flags().String("container-parent-dir", "/var/opt/sites", "Parent directory where site working directories live (default: /var/opt/sites)")
	backupCreateCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")
	backupCreateCmd.Flags().Bool("overwrite", false, "After creating backup, delete all old backups except the N most recent (configure N with --remainder)")
	backupCreateCmd.Flags().Int("remainder", 5, "Number of most recent backups to keep when using --overwrite (default: 5)")

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
	backupCreateCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	backupCreateCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	backupCreateCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupCreateCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")

	// SSH connection flags with environment variable support
	backupCreateCmd.Flags().StringP("user", "u", getEnvWithDefault("SSH_USER", ""), "SSH username (env: SSH_USER, default: current user)")
	backupCreateCmd.Flags().StringP("port", "p", getEnvWithDefault("SSH_PORT", "22"), "SSH port (env: SSH_PORT)")
	backupCreateCmd.Flags().StringP("key", "k", getEnvWithDefault("SSH_KEY", ""), "Path to SSH private key (env: SSH_KEY)")
	backupCreateCmd.Flags().BoolP("agent", "a", getEnvBoolWithDefault("SSH_AGENT", true), "Use SSH agent (env: SSH_AGENT)")
	backupCreateCmd.Flags().DurationP("timeout", "t", getEnvDurationWithDefault("SSH_TIMEOUT", 30*time.Second), "Connection timeout (env: SSH_TIMEOUT)")

	// Minio test command flags
	backupTestMinioCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupTestMinioCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	backupTestMinioCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	backupTestMinioCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupTestMinioCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")

	// Read command flags
	backupReadCmd.Flags().String("output", "", "Output file path (if empty, writes to stdout)")
	backupReadCmd.Flags().Bool("save", false, "Save backup object to current working directory (same as --output <basename>)")
	backupReadCmd.Flags().String("prefix", "", "Prefix to search for when using --latest (e.g. backups/site-)")
	backupReadCmd.Flags().Bool("latest", false, "If set, resolve the most recent object matching --prefix when object argument is omitted")
	backupReadCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupReadCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	backupReadCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	backupReadCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupReadCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")

	// List command flags
	backupListCmd.Flags().String("prefix", "", "Prefix to filter listed objects (e.g. backups/site-)")
	backupListCmd.Flags().Int("limit", 100, "Maximum number of objects to list")
	backupListCmd.Flags().Bool("json", false, "Output JSON")
	backupListCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupListCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	backupListCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	backupListCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupListCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")

	// Delete command registration and flags
	backupCmd.AddCommand(backupDeleteCmd)
	backupDeleteCmd.Flags().Bool("dry-run", false, "Preview deletions without performing them")
	backupDeleteCmd.Flags().String("prefix", "", "Prefix to select objects to delete (e.g. backups/site-)")
	backupDeleteCmd.Flags().Int("limit", 100, "Maximum number of objects to consider when using --prefix")
	backupDeleteCmd.Flags().Bool("latest", false, "If set with --prefix, delete only the most recent object matching --prefix")
	backupDeleteCmd.Flags().Bool("delete-all", false, "Delete all backups (respects --prefix if provided)")
	backupDeleteCmd.Flags().String("delete-range", "", "Delete backups by numeric range (e.g., '1-10' for 1st through 10th most recent)")
	backupDeleteCmd.Flags().String("delete-range-by-date", "", "Delete backups by date range (YYYYMMDD-YYYYMMDD or YYYYMMDD:HHMMSS-YYYYMMDD:HHMMSS)")
	backupDeleteCmd.Flags().Bool("skip-confirmation", false, "Skip interactive confirmation prompt")
	backupDeleteCmd.Flags().String("minio-endpoint", getEnvWithDefault("MINIO_ENDPOINT", ""), "Minio endpoint (env: MINIO_ENDPOINT)")
	backupDeleteCmd.Flags().String("minio-access-key", getEnvWithDefault("MINIO_ACCESS_KEY", ""), "Minio access key (env: MINIO_ACCESS_KEY)")
	backupDeleteCmd.Flags().String("minio-secret-key", getEnvWithDefault("MINIO_SECRET_KEY", ""), "Minio secret key (env: MINIO_SECRET_KEY)")
	backupDeleteCmd.Flags().String("minio-bucket", getEnvWithDefault("MINIO_BUCKET", "backups"), "Minio bucket name (env: MINIO_BUCKET)")
	backupDeleteCmd.Flags().Bool("minio-ssl", getEnvBoolWithDefault("MINIO_SSL", true), "Use SSL for Minio connection (env: MINIO_SSL)")
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

	if serverRange != "" {
		return processBackupCreateForServerRange(cmd, serverRange, minioConfig)
	}

	if len(args) < 1 {
		return fmt.Errorf("hostname argument is required when --server-range is not used")
	}

	hostname := args[0]
	return createBackupForHost(cmd, hostname, minioConfig)
}

func processBackupCreateForServerRange(cmd *cobra.Command, serverRange string, minioConfig *backup.MinioConfig) error {
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
		err := createBackupForHost(cmd, hostname, minioConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func createBackupForHost(cmd *cobra.Command, hostname string, minioConfig *backup.MinioConfig) error {

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

	backupManager := backup.NewBackupManager(sshClient, minioConfig)

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
	options := &backup.BackupOptions{
		DryRun:            mustGetBoolFlag(cmd, "dry-run"),
		Delete:            mustGetBoolFlag(cmd, "delete"),
		ContainerName:     mustGetStringFlag(cmd, "container-name"),
		ContainerFile:     mustGetStringFlag(cmd, "container-file"),
		ContainerNames:    containerNames,
		Local:             localMode,
		ParentDir:         mustGetStringFlag(cmd, "container-parent-dir"),
		ConfigFile:        mustGetStringFlag(cmd, "config-file"),
		DatabaseType:      mustGetStringFlag(cmd, "database-type"),
		DatabaseExportDir: mustGetStringFlag(cmd, "database-export-dir"),
		CustomAppDir:      mustGetStringFlag(cmd, "custom-app-dir"),
		DatabaseContainer: mustGetStringFlag(cmd, "database-container"),
		DatabaseName:      mustGetStringFlag(cmd, "database-name"),
		DatabaseUser:      mustGetStringFlag(cmd, "database-user"),
	}

	fmt.Printf("Creating backups on %s...\n\n", hostname)
	err := backupManager.CreateBackups(options)
	if err != nil {
		return err
	}

	// Handle overwrite mode: clean up old backups
	overwrite := mustGetBoolFlag(cmd, "overwrite")
	if overwrite {
		remainder := 5
		if v, err := cmd.Flags().GetInt("remainder"); err == nil {
			remainder = v
		}
		if remainder < 0 {
			return fmt.Errorf("--remainder must be >= 0")
		}

		fmt.Printf("\n--- Cleaning up old backups (keeping %d most recent) ---\n", remainder)

		// For each container that was backed up, clean up old backups
		containers, err := backupManager.GetContainersFromOptions(options)
		if err != nil {
			return fmt.Errorf("failed to get containers for cleanup: %w", err)
		}

		for _, container := range containers {
			siteName := filepath.Base(container.WorkingDir)
			// Backups are stored under backups/<siteName>/ so use that directory as the prefix
			prefix := fmt.Sprintf("backups/%s/", siteName)

			objs, err := backupManager.ListBackups(prefix, 0)
			if err != nil {
				fmt.Printf("Warning: failed to list backups for %s: %v\n", siteName, err)
				continue
			}

			if len(objs) <= remainder {
				fmt.Printf("Site %s: Found %d backup(s), keeping all\n", siteName, len(objs))
				continue
			}

			toDelete := backupManager.SelectObjectsForOverwrite(objs, remainder)
			if len(toDelete) == 0 {
				continue
			}

			fmt.Printf("Site %s: Found %d backup(s), keeping %d most recent, deleting %d older backup(s)\n",
				siteName, len(objs), remainder, len(toDelete))

			var deleteKeys []string
			for _, o := range toDelete {
				deleteKeys = append(deleteKeys, o.Key)
			}

			if err := backupManager.DeleteObjects(deleteKeys); err != nil {
				fmt.Printf("Warning: failed to delete old backups for %s: %v\n", siteName, err)
			} else {
				fmt.Printf("Successfully cleaned up old backups for %s\n", siteName)
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

	fmt.Println("\n✓ Minio connection test successful!")
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
	secretKey := mustGetStringFlag(cmd, "minio-secret-key")
	bucket := mustGetStringFlag(cmd, "minio-bucket")
	useSSL := mustGetBoolFlag(cmd, "minio-ssl")

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
		Endpoint:  endpoint,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    bucket,
		UseSSL:    useSSL,
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
