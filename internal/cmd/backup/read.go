package backup

import (
	"fmt"
	"path/filepath"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"ciwg-cli/internal/backup"
)

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

	// Get storage configuration
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		return err
	}

	s3Config, err := getS3Config(cmd)
	if err != nil {
		return err
	}

	// Ensure at least one storage backend is configured
	if minioConfig == nil && s3Config == nil {
		return fmt.Errorf("either MinIO or S3 must be configured (use --minio-endpoint or --s3-bucket)")
	}

	// Create backup manager
	var backupManager *backup.BackupManager
	if s3Config != nil {
		backupManager = backup.NewBackupManagerWithS3(nil, s3Config)
	} else {
		backupManager = backup.NewBackupManager(nil, minioConfig)
	}

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
