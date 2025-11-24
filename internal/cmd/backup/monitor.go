package backup

import (
	"fmt"

	"github.com/spf13/cobra"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/backup"
)

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
	fmt.Printf("Threshold:         %.1f%%\n", threshold)
	fmt.Printf("Migrate Percent:   %.1f%%\n", migratePercent)
	fmt.Printf("Force Delete:      %v\n", forceDelete)
	fmt.Printf("Minio Bucket:      %s\n", minioConfig.Bucket)
	fmt.Printf("AWS Glacier Vault: %s\n", awsConfig.Vault)
	fmt.Println("===========================================")

	return manager.MonitorAndMigrateIfNeeded(storagePath, threshold, migratePercent, dryRun, forceDelete)
}
