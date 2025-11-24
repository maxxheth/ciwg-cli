package backup

import (
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
