package dnsbackup

import (
	"context"
	"fmt"
	"os"
	"strings"

	dnsbackup "ciwg-cli/internal/dnsbackup"

	"github.com/spf13/cobra"
)

func runCreate(cmd *cobra.Command, args []string) error {
	if err := loadEnvFromFlag(cmd); err != nil {
		return err
	}

	token, err := requireToken(mustGetStringFlag(cmd, "token"))
	if err != nil {
		return err
	}

	// Create Cloudflare client
	client, err := dnsbackup.NewClient(token)
	if err != nil {
		return err
	}

	zones, err := resolveZones(cmd, args, client)
	if err != nil {
		return err
	}
	if len(zones) == 0 {
		return fmt.Errorf("no zones resolved")
	}

	if !mustGetBoolFlag(cmd, "upload-minio") {
		fmt.Fprintln(cmd.ErrOrStderr(), "upload-minio flag disabled; nothing to do")
		return nil
	}

	minioConfig, err := getMinioConfigFromFlags(cmd)
	if err != nil {
		return err
	}
	manager := dnsbackup.NewBackupManager(minioConfig)

	var glacierManager *dnsbackup.BackupManager
	if mustGetBoolFlag(cmd, "upload-glacier") {
		awsConfig, err := getAWSConfigFromFlags(cmd)
		if err != nil {
			return err
		}
		glacierManager = dnsbackup.NewBackupManagerWithAWS(minioConfig, awsConfig)
	}

	format := strings.ToLower(mustGetStringFlag(cmd, "format"))
	if format == "" {
		format = "json"
	}
	timeout := mustGetDurationFlag(cmd, "timeout")

	for _, target := range zones {
		fmt.Fprintf(cmd.ErrOrStderr(), "Exporting DNS records for zone '%s'...\n", target.ZoneName)
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		snapshot, err := client.Export(ctx, target.ZoneName)
		cancel()
		if err != nil {
			return fmt.Errorf("failed to export DNS records for %s: %w", target.ZoneName, err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "[%s] Exported %d DNS records\n", target.ZoneName, len(snapshot.Records))

		objectKey, err := manager.UploadSnapshot(snapshot, format)
		if err != nil {
			return fmt.Errorf("failed to upload %s to Minio: %w", target.ZoneName, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "[%s] Uploaded to Minio: %s\n", target.ZoneName, objectKey)

		if glacierManager != nil {
			if err := glacierManager.UploadToGlacier(snapshot, format); err != nil {
				return fmt.Errorf("failed to upload %s to Glacier: %w", target.ZoneName, err)
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
