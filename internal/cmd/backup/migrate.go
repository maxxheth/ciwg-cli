package backup

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"ciwg-cli/internal/backup"
)

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
	count := mustGetIntFlag(cmd, "count")
	percent := mustGetFloat64Flag(cmd, "percent")
	olderThan := mustGetDurationFlag(cmd, "older-than")
	deleteAfter := mustGetBoolFlag(cmd, "delete-after")
	limit := mustGetIntFlag(cmd, "limit")

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
