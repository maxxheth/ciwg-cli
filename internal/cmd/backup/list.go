package backup

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"ciwg-cli/internal/backup"
)

func runBackupList(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
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

	prefix := mustGetStringFlag(cmd, "prefix")
	limit := mustGetIntFlag(cmd, "limit")
	if limit == 0 {
		limit = 100 // default value
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
