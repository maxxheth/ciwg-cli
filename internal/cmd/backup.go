package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

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

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.AddCommand(backupCreateCmd)

	// Backup creation flags
	backupCreateCmd.Flags().Bool("dry-run", false, "Print actions without executing them")
	backupCreateCmd.Flags().Bool("delete", false, "Stop and remove containers, and delete associated directories after backup")
	backupCreateCmd.Flags().String("container-name", "", "Pipe-delimited container names or working directories to process (e.g. wp_foo|wp_bar|/srv/foo)")
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
}

func runBackupCreate(cmd *cobra.Command, args []string) error {
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
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	backupManager := backup.NewBackupManager(sshClient, minioConfig)

	// Get backup options from flags
	options := &backup.BackupOptions{
		DryRun:        mustGetBoolFlag(cmd, "dry-run"),
		Delete:        mustGetBoolFlag(cmd, "delete"),
		ContainerName: mustGetStringFlag(cmd, "container-name"),
		ContainerFile: mustGetStringFlag(cmd, "container-file"),
	}

	fmt.Printf("Creating backups on %s...\n\n", hostname)
	return backupManager.CreateBackups(options)
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
