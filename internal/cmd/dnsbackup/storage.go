package dnsbackup

import (
	"encoding/json"
	"fmt"

	dnsbackup "ciwg-cli/internal/dnsbackup"

	"github.com/spf13/cobra"
)

func runList(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	minioConfig, err := getMinioConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	manager := dnsbackup.NewBackupManager(minioConfig)

	prefix := mustGetStringFlag(cmd, "prefix")
	limit := mustGetIntFlag(cmd, "limit")

	backups, err := manager.ListBackups(prefix, limit)
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	if len(backups) == 0 {
		fmt.Println("No DNS backups found")
		return nil
	}

	if mustGetBoolFlag(cmd, "json") {
		data, err := json.MarshalIndent(backups, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal to JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("Found %d DNS backup(s):\n\n", len(backups))
	for i, backup := range backups {
		fmt.Printf("%d. %s\n", i+1, backup.Key)
		fmt.Printf("   Zone: %s\n", backup.ZoneName)
		fmt.Printf("   Size: %.2f KB\n", float64(backup.Size)/1024)
		fmt.Printf("   Modified: %s\n", backup.LastModified.Format("2006-01-02 15:04:05 MST"))
		fmt.Println()
	}

	return nil
}

func runRead(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	minioConfig, err := getMinioConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	manager := dnsbackup.NewBackupManager(minioConfig)

	var objectKey string
	if len(args) > 0 {
		objectKey = args[0]
	} else if mustGetBoolFlag(cmd, "latest") {
		prefix := mustGetStringFlag(cmd, "prefix")
		backups, err := manager.ListBackups(prefix, 1)
		if err != nil {
			return fmt.Errorf("failed to list backups: %w", err)
		}
		if len(backups) == 0 {
			return fmt.Errorf("no backups found")
		}
		objectKey = backups[0].Key
	} else {
		return fmt.Errorf("object key required or use --latest")
	}

	snapshot, err := manager.DownloadSnapshot(objectKey)
	if err != nil {
		return fmt.Errorf("failed to download snapshot: %w", err)
	}

	outputPath := mustGetStringFlag(cmd, "output")
	format := mustGetStringFlag(cmd, "format")
	if format == "" {
		format = "json"
	}

	if outputPath != "" {
		err = dnsbackup.SaveSnapshot(snapshot, outputPath, format, true)
		if err != nil {
			return fmt.Errorf("failed to save snapshot: %w", err)
		}
		fmt.Printf("✓ Snapshot saved to: %s\n", outputPath)
	} else {
		content, err := dnsbackup.EncodeSnapshot(snapshot, format, true)
		if err != nil {
			return fmt.Errorf("failed to encode snapshot: %w", err)
		}
		fmt.Println(string(content))
	}

	return nil
}

func runDelete(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	minioConfig, err := getMinioConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	manager := dnsbackup.NewBackupManager(minioConfig)

	dryRun := mustGetBoolFlag(cmd, "dry-run")
	deleteAll := mustGetBoolFlag(cmd, "delete-all")
	prefix := mustGetStringFlag(cmd, "prefix")

	var objectKeys []string

	if deleteAll {
		backups, err := manager.ListBackups(prefix, 0)
		if err != nil {
			return fmt.Errorf("failed to list backups: %w", err)
		}

		for _, backup := range backups {
			objectKeys = append(objectKeys, backup.Key)
		}

		if len(objectKeys) == 0 {
			fmt.Println("No backups found to delete")
			return nil
		}

		fmt.Printf("Will delete %d backup(s)\n", len(objectKeys))
	} else if len(args) > 0 {
		objectKeys = args
	} else {
		return fmt.Errorf("object key required or use --delete-all")
	}

	if dryRun {
		fmt.Println("\n[DRY RUN] Would delete:")
		for _, key := range objectKeys {
			fmt.Printf("  - %s\n", key)
		}
		return nil
	}

	err = manager.DeleteBackups(objectKeys, dryRun)
	if err != nil {
		return fmt.Errorf("failed to delete backups: %w", err)
	}

	fmt.Printf("\n✓ Deleted %d backup(s)\n", len(objectKeys))
	return nil
}
