package cmd

import (
	"encoding/json"
	"fmt"
	"os"
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

func init() {
	// Load .env early so getEnvWithDefault calls used during flag setup
	// will see values from a local .env file in development.
	_ = godotenv.Load()

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
	backupCreateCmd.Flags().String("server-range", "", "Server range pattern (e.g., 'wp%d.example.com:0-41')")

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
		DryRun:         mustGetBoolFlag(cmd, "dry-run"),
		Delete:         mustGetBoolFlag(cmd, "delete"),
		ContainerName:  mustGetStringFlag(cmd, "container-name"),
		ContainerFile:  mustGetStringFlag(cmd, "container-file"),
		ContainerNames: containerNames,
		Local:          localMode,
	}

	fmt.Printf("Creating backups on %s...\n\n", hostname)
	return backupManager.CreateBackups(options)
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
