package dnsbackup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/glacier"
)

// MinioConfig contains configuration for Minio storage
type MinioConfig struct {
	Endpoint         string
	AccessKey        string
	SecretKey        string
	Bucket           string
	UseSSL           bool
	BucketPath       string // Optional path prefix within bucket
	HTTPTimeout      time.Duration
	AutoCreateBucket bool
}

// AWSConfig contains configuration for AWS Glacier storage
type AWSConfig struct {
	Vault       string
	AccountID   string
	AccessKey   string
	SecretKey   string
	Region      string
	HTTPTimeout time.Duration
}

const defaultCapacityThreshold = 95.0

// BackupManager handles DNS backup operations with Minio and AWS Glacier
type BackupManager struct {
	minioClient *minio.Client
	minioConfig *MinioConfig
	adminClient *madmin.AdminClient
	awsClient   *glacier.Client
	awsConfig   *AWSConfig
	verbosity   int // 0=quiet, 1=normal, 2=verbose, 3=debug, 4=trace

	respectCapacity   bool
	capacityThreshold float64
	capacityReported  bool
}

// DNSBackupInfo represents metadata about a DNS backup
type DNSBackupInfo struct {
	Key          string    `json:"key"`
	ZoneName     string    `json:"zone_name"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// NewBackupManager creates a new DNS backup manager with Minio configuration
func NewBackupManager(minioConfig *MinioConfig) *BackupManager {
	return &BackupManager{
		minioConfig:       minioConfig,
		verbosity:         1,
		capacityThreshold: defaultCapacityThreshold,
	}
}

// NewBackupManagerWithAWS creates a DNS backup manager with both Minio and AWS configurations
func NewBackupManagerWithAWS(minioConfig *MinioConfig, awsConfig *AWSConfig) *BackupManager {
	return &BackupManager{
		minioConfig:       minioConfig,
		awsConfig:         awsConfig,
		verbosity:         1,
		capacityThreshold: defaultCapacityThreshold,
	}
}

// SetVerbosity sets the logging verbosity level
func (bm *BackupManager) SetVerbosity(level int) {
	bm.verbosity = level
}

// SetCapacityGuard configures whether uploads should verify Minio capacity usage first.
func (bm *BackupManager) SetCapacityGuard(enabled bool, threshold float64) {
	bm.respectCapacity = enabled
	if threshold <= 0 {
		threshold = defaultCapacityThreshold
	}
	bm.capacityThreshold = threshold
}

// logVerbose logs a message if verbosity >= 2
func (bm *BackupManager) logVerbose(format string, args ...interface{}) {
	if bm.verbosity >= 2 {
		fmt.Printf("[VERBOSE] "+format+"\n", args...)
	}
}

// logDebug logs a message if verbosity >= 3
func (bm *BackupManager) logDebug(format string, args ...interface{}) {
	if bm.verbosity >= 3 {
		fmt.Printf("[DEBUG] "+format+"\n", args...)
	}
}

// logTrace logs a message if verbosity >= 4
func (bm *BackupManager) logTrace(format string, args ...interface{}) {
	if bm.verbosity >= 4 {
		fmt.Printf("[TRACE] "+format+"\n", args...)
	}
}

// initMinioClient initializes the Minio client
func (bm *BackupManager) initMinioClient() error {
	if bm.minioClient != nil {
		return nil
	}

	tr := &http.Transport{
		IdleConnTimeout:     5 * time.Minute,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
	}
	if bm.minioConfig.HTTPTimeout > 0 {
		tr.ResponseHeaderTimeout = bm.minioConfig.HTTPTimeout
	}

	client, err := minio.New(bm.minioConfig.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(bm.minioConfig.AccessKey, bm.minioConfig.SecretKey, ""),
		Secure:    bm.minioConfig.UseSSL,
		Transport: tr,
	})
	if err != nil {
		return fmt.Errorf("failed to create Minio client: %w", err)
	}

	bm.minioClient = client

	// Ensure bucket exists (optionally creating it)
	ctx := context.Background()
	exists, err := bm.minioClient.BucketExists(ctx, bm.minioConfig.Bucket)
	if err != nil {
		return fmt.Errorf("failed to check if bucket exists: %w", err)
	}

	if !exists {
		if !bm.minioConfig.AutoCreateBucket {
			return fmt.Errorf("bucket %s does not exist", bm.minioConfig.Bucket)
		}

		bm.logVerbose("bucket %s missing, attempting to create", bm.minioConfig.Bucket)
		if err := bm.minioClient.MakeBucket(ctx, bm.minioConfig.Bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("failed to create bucket %s: %w", bm.minioConfig.Bucket, err)
		}
		bm.logVerbose("bucket %s created", bm.minioConfig.Bucket)
	}

	return nil
}

func (bm *BackupManager) initMinioAdminClient() error {
	if bm.adminClient != nil {
		return nil
	}
	if bm.minioConfig == nil {
		return fmt.Errorf("minio configuration is not set")
	}
	client, err := madmin.New(bm.minioConfig.Endpoint, bm.minioConfig.AccessKey, bm.minioConfig.SecretKey, bm.minioConfig.UseSSL)
	if err != nil {
		return fmt.Errorf("failed to create Minio admin client: %w", err)
	}
	bm.adminClient = client
	return nil
}

func (bm *BackupManager) ensureCapacity() error {
	if !bm.respectCapacity {
		return nil
	}
	if bm.minioConfig == nil {
		return fmt.Errorf("capacity guard enabled but Minio configuration is missing")
	}
	threshold := bm.capacityThreshold
	if threshold <= 0 {
		threshold = defaultCapacityThreshold
	}
	if err := bm.initMinioAdminClient(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := bm.adminClient.StorageInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to query Minio storage info: %w", err)
	}
	var total, used uint64
	for _, disk := range info.Disks {
		total += disk.TotalSpace
		used += disk.UsedSpace
	}
	if total == 0 {
		return fmt.Errorf("Minio storage reported zero total capacity")
	}
	usage := (float64(used) / float64(total)) * 100
	if usage >= threshold {
		return fmt.Errorf("Minio storage usage %.1f%% exceeds %.1f%% threshold; run 'ciwg-cli dns-backup monitor' to migrate or delete old backups", usage, threshold)
	}
	if !bm.capacityReported {
		fmt.Printf("✓ Minio capacity check: %.1f%% used (threshold %.1f%%)\n", usage, threshold)
		bm.capacityReported = true
	}
	return nil
}

// initAWSClient initializes the AWS Glacier client
func (bm *BackupManager) initAWSClient() error {
	if bm.awsClient != nil {
		return nil
	}

	if bm.awsConfig == nil {
		return fmt.Errorf("AWS configuration is not set")
	}

	tr := &http.Transport{
		IdleConnTimeout:     5 * time.Minute,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
	}

	var httpClient *http.Client
	if bm.awsConfig.HTTPTimeout > 0 {
		httpClient = &http.Client{Timeout: bm.awsConfig.HTTPTimeout, Transport: tr}
	} else {
		httpClient = &http.Client{Transport: tr}
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(bm.awsConfig.Region),
		awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider(
			bm.awsConfig.AccessKey,
			bm.awsConfig.SecretKey,
			"",
		)),
		awsconfig.WithHTTPClient(httpClient),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	bm.awsClient = glacier.NewFromConfig(cfg)

	// Verify vault exists
	ctx := context.Background()
	accountID := bm.awsConfig.AccountID
	if accountID == "" {
		accountID = "-"
	}
	_, err = bm.awsClient.DescribeVault(ctx, &glacier.DescribeVaultInput{
		AccountId: aws.String(accountID),
		VaultName: aws.String(bm.awsConfig.Vault),
	})
	if err != nil {
		return fmt.Errorf("vault %s does not exist or is not accessible: %w", bm.awsConfig.Vault, err)
	}

	return nil
}

// TestMinioConnection tests the Minio connection
func (bm *BackupManager) TestMinioConnection() error {
	if err := bm.initMinioClient(); err != nil {
		return err
	}

	ctx := context.Background()

	fmt.Printf("1. Testing bucket existence...\n")
	exists, err := bm.minioClient.BucketExists(ctx, bm.minioConfig.Bucket)
	if err != nil {
		return fmt.Errorf("failed to check bucket existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("bucket '%s' does not exist", bm.minioConfig.Bucket)
	}
	fmt.Printf("   ✓ Bucket '%s' exists\n\n", bm.minioConfig.Bucket)

	fmt.Printf("2. Testing write operation...\n")
	testObjectName := fmt.Sprintf(".dns-backup-test-%d.txt", time.Now().Unix())
	if bm.minioConfig.BucketPath != "" {
		testObjectName = filepath.Join(bm.minioConfig.BucketPath, testObjectName)
	}

	testContent := []byte("DNS backup connection test")
	info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, testObjectName,
		strings.NewReader(string(testContent)), int64(len(testContent)), minio.PutObjectOptions{
			ContentType: "text/plain",
		})
	if err != nil {
		return fmt.Errorf("failed to write test object: %w", err)
	}
	fmt.Printf("   ✓ Successfully wrote test object '%s' (%d bytes)\n\n", testObjectName, info.Size)

	fmt.Printf("3. Testing read operation...\n")
	object, err := bm.minioClient.GetObject(ctx, bm.minioConfig.Bucket, testObjectName, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to read test object: %w", err)
	}
	defer object.Close()

	readContent, err := io.ReadAll(object)
	if err != nil {
		return fmt.Errorf("failed to read test object content: %w", err)
	}
	if string(readContent) != string(testContent) {
		return fmt.Errorf("content mismatch")
	}
	fmt.Printf("   ✓ Successfully read test object and verified content\n\n")

	fmt.Printf("4. Testing delete operation...\n")
	err = bm.minioClient.RemoveObject(ctx, bm.minioConfig.Bucket, testObjectName, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete test object: %w", err)
	}
	fmt.Printf("   ✓ Successfully deleted test object\n")

	return nil
}

// TestAWSConnection tests the AWS Glacier connection
func (bm *BackupManager) TestAWSConnection() error {
	if err := bm.initAWSClient(); err != nil {
		return err
	}

	ctx := context.Background()
	accountID := bm.awsConfig.AccountID
	if accountID == "" {
		accountID = "-"
	}

	fmt.Printf("1. Testing AWS Glacier vault existence...\n")
	describeOutput, err := bm.awsClient.DescribeVault(ctx, &glacier.DescribeVaultInput{
		AccountId: aws.String(accountID),
		VaultName: aws.String(bm.awsConfig.Vault),
	})
	if err != nil {
		return fmt.Errorf("failed to access vault '%s': %w", bm.awsConfig.Vault, err)
	}
	fmt.Printf("   ✓ AWS Glacier Vault '%s' exists and is accessible\n", *describeOutput.VaultName)
	fmt.Printf("   Vault ARN: %s\n", *describeOutput.VaultARN)
	fmt.Printf("   Number of archives: %d\n\n", describeOutput.NumberOfArchives)

	fmt.Printf("2. Testing write operation...\n")
	testContent := []byte("DNS backup AWS Glacier test")
	testDescription := fmt.Sprintf("DNS backup test archive created at %d", time.Now().Unix())

	uploadOutput, err := bm.awsClient.UploadArchive(ctx, &glacier.UploadArchiveInput{
		AccountId:          aws.String(accountID),
		VaultName:          aws.String(bm.awsConfig.Vault),
		ArchiveDescription: aws.String(testDescription),
		Body:               bytes.NewReader(testContent),
	})
	if err != nil {
		return fmt.Errorf("failed to upload test archive: %w", err)
	}
	archiveID := *uploadOutput.ArchiveId
	fmt.Printf("   ✓ Successfully uploaded test archive (%d bytes)\n", len(testContent))
	fmt.Printf("   Archive ID: %s\n\n", archiveID[:40]+"...")

	fmt.Printf("3. Testing delete operation...\n")
	_, err = bm.awsClient.DeleteArchive(ctx, &glacier.DeleteArchiveInput{
		AccountId: aws.String(accountID),
		VaultName: aws.String(bm.awsConfig.Vault),
		ArchiveId: aws.String(archiveID),
	})
	if err != nil {
		return fmt.Errorf("failed to delete test archive: %w", err)
	}
	fmt.Printf("   ✓ Successfully deleted test archive\n")

	return nil
}

// UploadSnapshot uploads a DNS snapshot to Minio
func (bm *BackupManager) UploadSnapshot(snapshot *ZoneSnapshot, format string) (string, error) {
	if err := bm.initMinioClient(); err != nil {
		return "", err
	}
	if err := bm.ensureCapacity(); err != nil {
		return "", err
	}

	if err := snapshot.Validate(); err != nil {
		return "", err
	}

	// Encode snapshot
	content, err := EncodeSnapshot(snapshot, format, true)
	if err != nil {
		return "", fmt.Errorf("failed to encode snapshot: %w", err)
	}

	// Generate object key
	timestamp := snapshot.Exported.Format("20060102-150405")
	ext := ".json"
	if format == "yaml" || format == "yml" {
		ext = ".yaml"
	}
	objectName := fmt.Sprintf("dns-backups/%s-%s%s", snapshot.ZoneName, timestamp, ext)

	if bm.minioConfig.BucketPath != "" {
		objectName = filepath.Join(bm.minioConfig.BucketPath, objectName)
	}

	// Upload to Minio
	ctx := context.Background()
	_, err = bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName,
		bytes.NewReader(content), int64(len(content)), minio.PutObjectOptions{
			ContentType: "application/" + format,
		})
	if err != nil {
		return "", fmt.Errorf("failed to upload to Minio: %w", err)
	}

	fmt.Printf("✓ Uploaded DNS backup to Minio: %s (%d bytes)\n", objectName, len(content))
	return objectName, nil
}

// ListBackups lists DNS backups from Minio
func (bm *BackupManager) ListBackups(prefix string, limit int) ([]DNSBackupInfo, error) {
	if err := bm.initMinioClient(); err != nil {
		return nil, err
	}

	ctx := context.Background()

	// Add dns-backups prefix if not already present
	if prefix != "" && !strings.HasPrefix(prefix, "dns-backups/") {
		prefix = "dns-backups/" + prefix
	} else if prefix == "" {
		prefix = "dns-backups/"
	}

	if bm.minioConfig.BucketPath != "" {
		prefix = filepath.Join(bm.minioConfig.BucketPath, prefix)
	}

	opts := minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}

	var backups []DNSBackupInfo
	objectCh := bm.minioClient.ListObjects(ctx, bm.minioConfig.Bucket, opts)

	for obj := range objectCh {
		if obj.Err != nil {
			return nil, fmt.Errorf("error listing objects: %w", obj.Err)
		}

		backups = append(backups, DNSBackupInfo{
			Key:          obj.Key,
			ZoneName:     extractZoneName(obj.Key),
			Size:         obj.Size,
			LastModified: obj.LastModified,
		})

		if limit > 0 && len(backups) >= limit {
			break
		}
	}

	// Sort by LastModified descending (most recent first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].LastModified.After(backups[j].LastModified)
	})

	return backups, nil
}

// DownloadSnapshot downloads a DNS snapshot from Minio
func (bm *BackupManager) DownloadSnapshot(objectKey string) (*ZoneSnapshot, error) {
	if err := bm.initMinioClient(); err != nil {
		return nil, err
	}

	ctx := context.Background()
	object, err := bm.minioClient.GetObject(ctx, bm.minioConfig.Bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to download from Minio: %w", err)
	}
	defer object.Close()

	data, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("failed to read object content: %w", err)
	}

	// Detect format from file extension
	format := "json"
	if strings.HasSuffix(objectKey, ".yaml") || strings.HasSuffix(objectKey, ".yml") {
		format = "yaml"
	}

	snapshot, err := decodeSnapshot(data, format)
	if err != nil {
		return nil, fmt.Errorf("failed to decode snapshot: %w", err)
	}

	return snapshot, nil
}

// DeleteBackups deletes multiple DNS backups from Minio
func (bm *BackupManager) DeleteBackups(objectKeys []string, dryRun bool) error {
	if err := bm.initMinioClient(); err != nil {
		return err
	}

	if len(objectKeys) == 0 {
		return nil
	}

	ctx := context.Background()

	for _, key := range objectKeys {
		if dryRun {
			fmt.Printf("[DRY RUN] Would delete: %s\n", key)
			continue
		}

		err := bm.minioClient.RemoveObject(ctx, bm.minioConfig.Bucket, key, minio.RemoveObjectOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete %s: %w", key, err)
		}
		fmt.Printf("✓ Deleted: %s\n", key)
	}

	return nil
}

// MigrateToGlacier migrates DNS backups from Minio to AWS Glacier
func (bm *BackupManager) MigrateToGlacier(percent float64, dryRun bool) error {
	if err := bm.initMinioClient(); err != nil {
		return err
	}

	if !dryRun {
		if err := bm.initAWSClient(); err != nil {
			return err
		}
	}

	// List all DNS backups
	backups, err := bm.ListBackups("", 0)
	if err != nil {
		return err
	}

	if len(backups) == 0 {
		fmt.Println("No DNS backups found to migrate")
		return nil
	}

	// Sort by date (oldest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].LastModified.Before(backups[j].LastModified)
	})

	// Calculate number to migrate
	numToMigrate := int(math.Ceil(float64(len(backups)) * percent / 100.0))
	if numToMigrate == 0 {
		fmt.Println("No backups to migrate based on percentage")
		return nil
	}

	fmt.Printf("Migrating %d oldest DNS backups (%.1f%%) to AWS Glacier...\n", numToMigrate, percent)

	ctx := context.Background()
	for i := 0; i < numToMigrate; i++ {
		backup := backups[i]

		if dryRun {
			fmt.Printf("[DRY RUN] Would migrate: %s (%.2f MB, %s)\n",
				backup.Key,
				float64(backup.Size)/(1024*1024),
				backup.LastModified.Format(time.RFC3339))
			continue
		}

		fmt.Printf("Migrating %d/%d: %s (%.2f MB)\n",
			i+1, numToMigrate, backup.Key, float64(backup.Size)/(1024*1024))

		// Download from Minio
		object, err := bm.minioClient.GetObject(ctx, bm.minioConfig.Bucket, backup.Key, minio.GetObjectOptions{})
		if err != nil {
			fmt.Printf("  ⚠ Failed to download: %v\n", err)
			continue
		}

		// Read into memory
		data, err := io.ReadAll(object)
		object.Close()
		if err != nil {
			fmt.Printf("  ⚠ Failed to read: %v\n", err)
			continue
		}

		// Upload to Glacier
		accountID := bm.awsConfig.AccountID
		if accountID == "" {
			accountID = "-"
		}

		_, err = bm.awsClient.UploadArchive(ctx, &glacier.UploadArchiveInput{
			AccountId:          aws.String(accountID),
			VaultName:          aws.String(bm.awsConfig.Vault),
			ArchiveDescription: aws.String(fmt.Sprintf("DNS Backup: %s", backup.Key)),
			Body:               bytes.NewReader(data),
		})
		if err != nil {
			fmt.Printf("  ⚠ Failed to upload to Glacier: %v\n", err)
			continue
		}

		// Delete from Minio after successful migration
		err = bm.minioClient.RemoveObject(ctx, bm.minioConfig.Bucket, backup.Key, minio.RemoveObjectOptions{})
		if err != nil {
			fmt.Printf("  ⚠ Failed to delete from Minio: %v\n", err)
			continue
		}

		fmt.Printf("  ✓ Migrated and removed from Minio\n")
	}

	return nil
}

// UploadToGlacier uploads a DNS snapshot directly to AWS Glacier
func (bm *BackupManager) UploadToGlacier(snapshot *ZoneSnapshot, format string) error {
	if err := bm.initAWSClient(); err != nil {
		return err
	}

	// Encode snapshot
	content, err := EncodeSnapshot(snapshot, format, true)
	if err != nil {
		return fmt.Errorf("failed to encode snapshot: %w", err)
	}

	// Generate description
	timestamp := snapshot.Exported.Format("20060102-150405")
	description := fmt.Sprintf("DNS Backup: %s (%s)", snapshot.ZoneName, timestamp)

	ctx := context.Background()
	accountID := bm.awsConfig.AccountID
	if accountID == "" {
		accountID = "-"
	}

	fmt.Printf("Uploading DNS backup to AWS Glacier...\n")
	uploadResult, err := bm.awsClient.UploadArchive(ctx, &glacier.UploadArchiveInput{
		AccountId:          aws.String(accountID),
		VaultName:          aws.String(bm.awsConfig.Vault),
		ArchiveDescription: aws.String(description),
		Body:               bytes.NewReader(content),
	})
	if err != nil {
		return fmt.Errorf("failed to upload to AWS Glacier: %w", err)
	}

	fmt.Printf("✓ Uploaded to AWS Glacier (%d bytes)\n", len(content))
	if uploadResult.ArchiveId != nil {
		fmt.Printf("  Archive ID: %s...\n", (*uploadResult.ArchiveId)[:40])
	}

	return nil
}

// Expected format: dns-backups/{zone-name}-YYYYMMDD-HHMMSS.{ext}
// The timestamp is always exactly 8 digits, hyphen, 6 digits
func extractZoneName(key string) string {
	// Extract filename from path
	filename := filepath.Base(key)

	// Remove extension
	name := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Look for the timestamp pattern: -YYYYMMDD-HHMMSS at the end
	// This pattern is exactly 16 characters: -8digits-6digits
	if len(name) > 16 {
		// Check if the last 16 characters match the timestamp pattern
		possibleTimestamp := name[len(name)-16:]
		// Pattern: -YYYYMMDD-HHMMSS
		if len(possibleTimestamp) == 16 &&
			possibleTimestamp[0] == '-' &&
			possibleTimestamp[9] == '-' &&
			isDigits(possibleTimestamp[1:9]) &&
			isDigits(possibleTimestamp[10:16]) {
			// Valid timestamp found, return everything before it
			return name[:len(name)-16]
		}
	}

	// Fallback: use the old logic if timestamp pattern not found
	parts := strings.Split(name, "-")
	if len(parts) >= 3 {
		return strings.Join(parts[:len(parts)-2], "-")
	}

	return name
}

// isDigits checks if a string contains only digits
func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
