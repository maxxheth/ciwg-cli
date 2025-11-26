package dnsbackup

import (
	"fmt"
	"strings"

	dnsbackup "ciwg-cli/internal/dnsbackup"

	"github.com/spf13/cobra"
)

func runMonitor(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	minioConfig, err := getMinioConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	awsConfig, err := getAWSConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	manager := dnsbackup.NewBackupManagerWithAWS(minioConfig, awsConfig)

	percent := mustGetFloat64Flag(cmd, "migrate-percent")
	dryRun := mustGetBoolFlag(cmd, "dry-run")

	fmt.Printf("Monitoring DNS backup storage...\n")
	fmt.Printf("Migration threshold: %.1f%% of oldest backups\n\n", percent)

	// For DNS backups, we'll migrate based on count rather than storage capacity
	// since DNS records are typically small
	err = manager.MigrateToGlacier(percent, dryRun)
	if err != nil {
		return fmt.Errorf("failed to migrate backups: %w", err)
	}

	return nil
}

func runMigrateAWS(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	minioConfig, err := getMinioConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	awsConfig, err := getAWSConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	manager := dnsbackup.NewBackupManagerWithAWS(minioConfig, awsConfig)

	percent := mustGetFloat64Flag(cmd, "percent")
	dryRun := mustGetBoolFlag(cmd, "dry-run")

	if percent == 0 {
		percent = 10.0 // Default to 10%
	}

	err = manager.MigrateToGlacier(percent, dryRun)
	if err != nil {
		return fmt.Errorf("failed to migrate backups: %w", err)
	}

	if !dryRun {
		fmt.Println("\n✓ Migration completed successfully")
	}

	return nil
}

func runTestMinio(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	minioConfig, err := getMinioConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	manager := dnsbackup.NewBackupManager(minioConfig)

	fmt.Println("Testing Minio connection for DNS backups...")
	fmt.Println()

	err = manager.TestMinioConnection()
	if err != nil {
		return fmt.Errorf("Minio connection test failed: %w", err)
	}

	fmt.Println()
	fmt.Println("✓ All Minio tests passed successfully")
	return nil
}

func runTestAWS(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	awsConfig, err := getAWSConfigFromFlags(cmd)
	if err != nil {
		return err
	}

	manager := dnsbackup.NewBackupManagerWithAWS(nil, awsConfig)

	fmt.Println("Testing AWS Glacier connection for DNS backups...")
	fmt.Println()

	err = manager.TestAWSConnection()
	if err != nil {
		return fmt.Errorf("AWS Glacier connection test failed: %w", err)
	}

	fmt.Println()
	fmt.Println("✓ All AWS Glacier tests passed successfully")
	return nil
}

func runConn(cmd *cobra.Command, args []string) error {
	fmt.Println("Testing connections for DNS backup storage...")
	fmt.Println()

	// Test Minio
	fmt.Println("=" + strings.Repeat("=", 50))
	fmt.Println("Testing Minio Connection")
	fmt.Println("=" + strings.Repeat("=", 50))
	fmt.Println()

	if err := runTestMinio(cmd, args); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("=" + strings.Repeat("=", 50))
	fmt.Println("Testing AWS Glacier Connection")
	fmt.Println("=" + strings.Repeat("=", 50))
	fmt.Println()

	if err := runTestAWS(cmd, args); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("=" + strings.Repeat("=", 50))
	fmt.Println("✓ All connection tests passed")
	fmt.Println("=" + strings.Repeat("=", 50))

	return nil
}
