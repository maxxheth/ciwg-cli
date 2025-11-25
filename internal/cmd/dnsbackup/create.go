package dnsbackup

import (
	"context"
	"fmt"
	"os"

	dnsbackup "ciwg-cli/internal/dnsbackup"

	"github.com/spf13/cobra"
)

func runCreate(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	zone := args[0]
	token, err := requireToken(mustGetStringFlag(cmd, "token"))
	if err != nil {
		return err
	}

	// Create Cloudflare client
	client, err := dnsbackup.NewClient(token)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), mustGetDurationFlag(cmd, "timeout"))
	defer cancel()

	// Export DNS records
	fmt.Printf("Exporting DNS records for zone '%s'...\n", zone)
	snapshot, err := client.Export(ctx, zone)
	if err != nil {
		return fmt.Errorf("failed to export DNS records: %w", err)
	}

	fmt.Printf("✓ Exported %d DNS records\n", len(snapshot.Records))

	// Upload to Minio if configured
	if mustGetBoolFlag(cmd, "upload-minio") {
		minioConfig, err := getMinioConfigFromFlags(cmd)
		if err != nil {
			return err
		}

		manager := dnsbackup.NewBackupManager(minioConfig)
		format := mustGetStringFlag(cmd, "format")
		if format == "" {
			format = "json"
		}

		objectKey, err := manager.UploadSnapshot(snapshot, format)
		if err != nil {
			return fmt.Errorf("failed to upload to Minio: %w", err)
		}

		fmt.Printf("✓ Uploaded to Minio: %s\n", objectKey)

		// Also upload to AWS Glacier if requested
		if mustGetBoolFlag(cmd, "upload-glacier") {
			awsConfig, err := getAWSConfigFromFlags(cmd)
			if err != nil {
				return err
			}

			managerWithAWS := dnsbackup.NewBackupManagerWithAWS(minioConfig, awsConfig)

			// Upload directly to Glacier
			err = managerWithAWS.UploadToGlacier(snapshot, format)
			if err != nil {
				return fmt.Errorf("failed to upload to Glacier: %w", err)
			}
		}
	}

	return nil
}

func getMinioConfigFromFlags(cmd *cobra.Command) (*dnsbackup.MinioConfig, error) {
	endpoint := mustGetStringFlag(cmd, "minio-endpoint")
	if endpoint == "" {
		return nil, fmt.Errorf("minio-endpoint is required (use --minio-endpoint or set MINIO_ENDPOINT)")
	}

	accessKey := mustGetStringFlag(cmd, "minio-access-key")
	if accessKey == "" {
		return nil, fmt.Errorf("minio-access-key is required (use --minio-access-key or set MINIO_ACCESS_KEY)")
	}

	secretKey := mustGetStringFlag(cmd, "minio-secret-key")
	if secretKey == "" {
		return nil, fmt.Errorf("minio-secret-key is required (use --minio-secret-key or set MINIO_SECRET_KEY)")
	}

	return &dnsbackup.MinioConfig{
		Endpoint:         endpoint,
		AccessKey:        accessKey,
		SecretKey:        secretKey,
		Bucket:           mustGetStringFlag(cmd, "minio-bucket"),
		UseSSL:           mustGetBoolFlag(cmd, "minio-ssl"),
		BucketPath:       mustGetStringFlag(cmd, "bucket-path"),
		HTTPTimeout:      mustGetDurationFlag(cmd, "minio-http-timeout"),
		AutoCreateBucket: os.Getenv("MINIO_DNS_BUCKET") != "",
	}, nil
}

func getAWSConfigFromFlags(cmd *cobra.Command) (*dnsbackup.AWSConfig, error) {
	vault := mustGetStringFlag(cmd, "aws-vault")
	if vault == "" {
		return nil, fmt.Errorf("aws-vault is required (use --aws-vault or set AWS_VAULT)")
	}

	accessKey := mustGetStringFlag(cmd, "aws-access-key")
	if accessKey == "" {
		return nil, fmt.Errorf("aws-access-key is required (use --aws-access-key or set AWS_ACCESS_KEY)")
	}

	secretKey := mustGetStringFlag(cmd, "aws-secret-access-key")
	if secretKey == "" {
		return nil, fmt.Errorf("aws-secret-access-key is required (use --aws-secret-access-key or set AWS_SECRET_ACCESS_KEY)")
	}

	return &dnsbackup.AWSConfig{
		Vault:       vault,
		AccountID:   mustGetStringFlag(cmd, "aws-account-id"),
		AccessKey:   accessKey,
		SecretKey:   secretKey,
		Region:      mustGetStringFlag(cmd, "aws-region"),
		HTTPTimeout: mustGetDurationFlag(cmd, "aws-http-timeout"),
	}, nil
}
