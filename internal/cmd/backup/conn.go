package backup

import (
	"fmt"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"ciwg-cli/internal/backup"
)

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

	fmt.Println("✓ Minio connection test successful!")
	return nil
}

func runTestAWS(cmd *cobra.Command, args []string) error {
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}
	// Validate AWS configuration
	awsConfig, err := getAWSConfig(cmd)
	if err != nil {
		return err
	}

	if awsConfig == nil {
		return fmt.Errorf("AWS Glacier vault not configured (set AWS_VAULT environment variable or --aws-vault flag)")
	}

	fmt.Println("Testing AWS Glacier connection...")
	fmt.Printf("Vault: %s\n", awsConfig.Vault)
	fmt.Printf("Account ID: %s\n", awsConfig.AccountID)
	fmt.Printf("Region: %s\n\n", awsConfig.Region)

	// Create a temporary backup manager without SSH client for testing
	backupManager := backup.NewBackupManagerWithAWS(nil, nil, awsConfig)

	// Test connection and perform read/write test
	if err := backupManager.TestAWSConnection(); err != nil {
		return fmt.Errorf("AWS Glacier connection test failed: %w", err)
	}

	fmt.Println("✓ AWS Glacier connection test successful!")
	return nil
}

func runBackupConn(cmd *cobra.Command, args []string) error {
	// Load .env if specified
	if envPath := mustGetStringFlag(cmd, "env"); envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return fmt.Errorf("failed to load env file '%s': %w", envPath, err)
		}
	}

	fmt.Println("===========================================")
	fmt.Println("Testing Backup System Connections")
	fmt.Println("===========================================")

	// Test Minio
	minioConfig, err := getMinioConfig(cmd)
	if err != nil {
		fmt.Printf("❌ Minio Configuration Error: %v\n\n", err)
	} else {
		fmt.Println("📦 Testing Minio Connection...")
		fmt.Printf("   Endpoint: %s\n", minioConfig.Endpoint)
		fmt.Printf("   Bucket:   %s\n", minioConfig.Bucket)
		fmt.Printf("   Use SSL:  %v\n\n", minioConfig.UseSSL)

		backupManager := backup.NewBackupManager(nil, minioConfig)
		if err := backupManager.TestMinioConnection(); err != nil {
			fmt.Printf("   ❌ Minio test failed: %v\n\n", err)
		} else {
			fmt.Println("   ✓ Minio connection successful!")
		}
	}

	// Test AWS Glacier
	awsConfig, err := getAWSConfig(cmd)
	if err != nil {
		fmt.Printf("⚠️  AWS Glacier Configuration: %v\n", err)
		fmt.Println("   Skipping AWS Glacier test.")
	} else if awsConfig == nil {
		fmt.Println("⚠️  AWS Glacier not configured.")
		fmt.Println("   Skipping AWS Glacier test.")
	} else {
		fmt.Println("☁️  Testing AWS Glacier Connection...")
		fmt.Printf("   Vault:      %s\n", awsConfig.Vault)
		fmt.Printf("   Account ID: %s\n", awsConfig.AccountID)
		fmt.Printf("   Region:     %s\n\n", awsConfig.Region)

		backupManager := backup.NewBackupManagerWithAWS(nil, nil, awsConfig)
		if err := backupManager.TestAWSConnection(); err != nil {
			fmt.Printf("   ❌ AWS Glacier test failed: %v\n\n", err)
		} else {
			fmt.Println("   ✓ AWS Glacier connection successful!")
		}
	}

	fmt.Println("===========================================")
	fmt.Println("Connection Tests Complete")
	fmt.Println("===========================================")

	return nil
}
