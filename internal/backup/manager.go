package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"net"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/glacier"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"ciwg-cli/internal/auth"
)

// ProgressReader wraps an io.Reader and reports progress
type ProgressReader struct {
	reader      io.Reader
	total       int64
	read        int64
	lastReport  time.Time
	reportEvery time.Duration
	label       string
}

// NewProgressReader creates a progress tracking reader
func NewProgressReader(r io.Reader, total int64, label string) *ProgressReader {
	return &ProgressReader{
		reader:      r,
		total:       total,
		lastReport:  time.Now(),
		reportEvery: 2 * time.Second, // Report every 2 seconds
		label:       label,
	}
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.read += int64(n)

	// Report progress periodically or on completion/error
	now := time.Now()
	if now.Sub(pr.lastReport) >= pr.reportEvery || err == io.EOF || err != nil {
		pr.report()
		pr.lastReport = now
	}

	return n, err
}

func (pr *ProgressReader) report() {
	if pr.total > 0 {
		percent := float64(pr.read) / float64(pr.total) * 100
		mbRead := float64(pr.read) / (1024 * 1024)
		mbTotal := float64(pr.total) / (1024 * 1024)
		fmt.Printf("   %s: %.2f%% (%.2f / %.2f MB)\n", pr.label, percent, mbRead, mbTotal)
	} else {
		// Unknown total size, just show bytes transferred
		mbRead := float64(pr.read) / (1024 * 1024)
		fmt.Printf("   %s: %.2f MB transferred\n", pr.label, mbRead)
	}
}

// countingWriter counts bytes written but discards the data
type countingWriter struct {
	written int64
}

func (w *countingWriter) Write(p []byte) (n int, err error) {
	n = len(p)
	w.written += int64(n)
	return n, nil
}

type MinioConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
	// BucketPath is an optional path prefix within the bucket (e.g., "production/backups")
	BucketPath string
	// HTTPTimeout is an optional overall timeout for the HTTP client used by the Minio SDK.
	// Zero means no timeout (requests can run indefinitely).
	HTTPTimeout time.Duration
}

type AWSConfig struct {
	Vault     string // Glacier uses vaults instead of buckets
	AccountID string // AWS account ID (can be "-" for current account)
	AccessKey string
	SecretKey string
	Region    string
	// HTTPTimeout is an optional overall timeout for the AWS HTTP client.
	// Zero means no timeout (requests can run indefinitely).
	HTTPTimeout time.Duration
}

type BackupOptions struct {
	DryRun        bool
	Delete        bool
	ContainerName string
	ContainerFile string
	// ContainerNames is a comma-delimited list provided via CLI and parsed into a slice
	ContainerNames []string
	// Local indicates to run docker and tar commands locally instead of over SSH
	Local bool
	// ParentDir is the parent directory where site working directories are located (e.g. /var/opt/sites)
	ParentDir string
	// ConfigFile is the path to a YAML config file for custom backup configurations
	ConfigFile string
	// DatabaseType specifies the database type for custom containers (postgres, mysql, etc.)
	DatabaseType string
	// DatabaseExportDir is where database exports should be saved before backup
	DatabaseExportDir string
	// CustomAppDir is the application directory for custom containers
	CustomAppDir string
	// DatabaseContainer is the name of a separate database container
	DatabaseContainer string
	// DatabaseName for custom database exports
	DatabaseName string
	// DatabaseUser for custom database exports
	DatabaseUser string
	// RespectCapacityLimit checks storage capacity before creating backups
	RespectCapacityLimit bool
	// CapacityThreshold is the percentage threshold for capacity checks (default 95.0)
	CapacityThreshold float64
	// IncludeAWSGlacier enables uploading backups to AWS Glacier in addition to Minio
	IncludeAWSGlacier bool
	// EstimateMethod specifies compression estimation for dry-run: "heuristic", "sample", or "accurate"
	EstimateMethod string
	// SampleSize specifies the number of bytes to sample for "sample" estimation method
	SampleSize int64
	// SmartRetention enables date-aware retention policy (preserves weekly/monthly backups)
	SmartRetention *SmartRetentionPolicy
}

// SmartRetentionPolicy defines intelligent backup retention based on backup dates
// Allows preserving weekly and monthly backups from a single daily backup job
type SmartRetentionPolicy struct {
	Enabled     bool // Enable smart retention (vs simple "keep N most recent")
	KeepDaily   int  // Number of daily backups to keep (default: 14)
	KeepWeekly  int  // Number of weekly backups to keep (default: 26)
	KeepMonthly int  // Number of monthly backups to keep (default: 6)
	WeeklyDay   int  // Day of week for weekly backups, 0=Sunday (default: 0)
	MonthlyDay  int  // Day of month for monthly backups (default: 1)
}

// SanitizeOptions contains options for sanitizing backup tarballs
type SanitizeOptions struct {
	InputPath    string   // Path to input tarball
	OutputPath   string   // Path to output sanitized tarball
	ExtractDirs  []string // Directories to extract from tarball
	ExtractFiles []string // File patterns to extract (e.g., *.sql)
	DryRun       bool     // Preview mode without making changes
}

// StorageCapacity represents disk usage statistics
type StorageCapacity struct {
	Total       uint64  // Total disk space in bytes
	Used        uint64  // Used disk space in bytes
	Available   uint64  // Available disk space in bytes
	UsedPercent float64 // Usage percentage (0-100)
	Path        string  // Mount point or path checked
}

type ContainerInfo struct {
	Name       string
	WorkingDir string
	// Type indicates the container type: wordpress, custom, postgres, mysql, etc.
	Type string
	// Config holds custom configuration from YAML file
	Config *ContainerConfig
}

// DefaultLicenseKeysToRemove is the default list of WordPress option names
// that contain license keys or sensitive licensing information.
// These are removed during backup sanitization to create client-safe backups.
var DefaultLicenseKeysToRemove = []string{
	"license_number",
	"_elementor_pro_license_data",
	"_elementor_pro_license_data_fallback",
	"_elementor_pro_license_v2_data_fallback",
	"_elementor_pro_license_v2_data",
	"_transient_timeout_rg_gforms_license",
	"_transient_rg_gforms_license",
	"_transient_timeout_uael_license_status",
	"_transient_timeout_astra-addon_license_status",
}

// CapacityEstimateOptions contains parameters for capacity estimation
type CapacityEstimateOptions struct {
	// Retention policy
	DailyRetention   int
	WeeklyRetention  int
	MonthlyRetention int

	// Growth modeling
	GrowthRate       float64 // Monthly growth percentage
	ProjectionMonths int
	BufferPercent    float64

	// Cost parameters
	GlacierPricePerGB   float64 // Per GB per month
	RetrievalPricePerGB float64

	// Baseline data (one of these will be set)
	AvgCompressedSize int64 // Manual input in bytes
	SiteCount         int
}

// SiteEstimate represents capacity estimates for a single site
type SiteEstimate struct {
	SiteName         string  `json:"site_name"`
	UncompressedSize int64   `json:"uncompressed_size"`
	CompressedSize   int64   `json:"compressed_size"`
	CompressionRatio float64 `json:"compression_ratio"`
	HotStorageSize   int64   `json:"hot_storage_size"`  // Daily backups in Minio
	ColdStorageSize  int64   `json:"cold_storage_size"` // Weekly+Monthly in Glacier
	TotalStorageSize int64   `json:"total_storage_size"`
}

// CapacityEstimate represents the complete capacity estimation results
type CapacityEstimate struct {
	// Input parameters
	EstimationMethod string `json:"estimation_method"`
	SitesScanned     int    `json:"sites_scanned"`

	// Retention policy
	DailyRetention      int `json:"daily_retention"`
	WeeklyRetention     int `json:"weekly_retention"`
	MonthlyRetention    int `json:"monthly_retention"`
	TotalBackupsPerSite int `json:"total_backups_per_site"`

	// Baseline measurements
	AvgUncompressedSize int64   `json:"avg_uncompressed_size"`
	AvgCompressedSize   int64   `json:"avg_compressed_size"`
	AvgCompressionRatio float64 `json:"avg_compression_ratio"`

	// Per-site storage requirements
	PerSiteHotStorage   int64 `json:"per_site_hot_storage"`
	PerSiteColdStorage  int64 `json:"per_site_cold_storage"`
	PerSiteTotalStorage int64 `json:"per_site_total_storage"`

	// Fleet-wide storage requirements
	FleetHotStorage   int64 `json:"fleet_hot_storage"`
	FleetColdStorage  int64 `json:"fleet_cold_storage"`
	FleetTotalStorage int64 `json:"fleet_total_storage"`

	// With buffer
	FleetTotalWithBuffer int64   `json:"fleet_total_with_buffer"`
	BufferPercent        float64 `json:"buffer_percent"`

	// Growth projections (if enabled)
	GrowthProjections []GrowthProjection `json:"growth_projections,omitempty"`

	// Cost estimates (if enabled)
	MonthlyCost        float64 `json:"monthly_cost,omitempty"`
	RetrievalCost10Pct float64 `json:"retrieval_cost_10pct,omitempty"`

	// Per-site details
	Sites []SiteEstimate `json:"sites,omitempty"`
}

// GrowthProjection represents storage projections at a specific month
type GrowthProjection struct {
	Month          int     `json:"month"`
	TotalStorageGB float64 `json:"total_storage_gb"`
	HotStorageGB   float64 `json:"hot_storage_gb"`
	ColdStorageGB  float64 `json:"cold_storage_gb"`
	MonthlyCost    float64 `json:"monthly_cost,omitempty"`
}

type BackupManager struct {
	sshClient   *auth.SSHClient
	minioClient *minio.Client
	minioConfig *MinioConfig
	awsClient   *glacier.Client
	awsConfig   *AWSConfig
	verbosity   int // 0=quiet, 1=normal, 2=verbose, 3=debug, 4=trace
}

// ObjectInfo is a lightweight representation of an object in Minio
type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

func NewBackupManager(sshClient *auth.SSHClient, minioConfig *MinioConfig) *BackupManager {
	return &BackupManager{
		sshClient:   sshClient,
		minioConfig: minioConfig,
		verbosity:   1, // Default to normal verbosity
	}
}

// NewBackupManagerWithAWS creates a BackupManager with both Minio and AWS configurations
func NewBackupManagerWithAWS(sshClient *auth.SSHClient, minioConfig *MinioConfig, awsConfig *AWSConfig) *BackupManager {
	return &BackupManager{
		sshClient:   sshClient,
		minioConfig: minioConfig,
		awsConfig:   awsConfig,
		verbosity:   1, // Default to normal verbosity
	}
}

// SetVerbosity sets the verbosity level (0=quiet, 1=normal, 2=verbose, 3=debug, 4=trace)
func (bm *BackupManager) SetVerbosity(level int) {
	bm.verbosity = level
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

// GetBucketPath returns the configured bucket path prefix
func (bm *BackupManager) GetBucketPath() string {
	if bm.minioConfig == nil {
		return ""
	}
	return bm.minioConfig.BucketPath
}

// executeCommand runs a shell command either over SSH (when sshClient is present)
// or locally (when sshClient is nil). It returns stdout, stderr and any error.
func (bm *BackupManager) executeCommand(cmd string) (string, string, error) {
	if bm.sshClient == nil {
		c := exec.Command("bash", "-lc", cmd)
		var out bytes.Buffer
		var stderr bytes.Buffer
		c.Stdout = &out
		c.Stderr = &stderr
		err := c.Run()
		return out.String(), stderr.String(), err
	}
	return bm.sshClient.ExecuteCommand(cmd)
}

func (bm *BackupManager) initMinioClient() error {
	if bm.minioClient != nil {
		return nil
	}

	// Create custom transport for Minio client using configured timeouts.
	dialer := &net.Dialer{
		Timeout:   60 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   5 * time.Minute,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       5 * time.Minute,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		// ResponseHeaderTimeout controls how long to wait for a server's
		// response headers after writing the request. When non-zero, it will
		// help prevent long stalls waiting for headers; leave zero for no timeout.
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

	// Ensure bucket exists
	ctx := context.Background()
	exists, err := bm.minioClient.BucketExists(ctx, bm.minioConfig.Bucket)
	if err != nil {
		return fmt.Errorf("failed to check if bucket exists: %w", err)
	}

	if !exists {
		return fmt.Errorf("bucket %s does not exist", bm.minioConfig.Bucket)
	}

	return nil
}

func (bm *BackupManager) TestMinioConnection() error {
	if err := bm.initMinioClient(); err != nil {
		return err
	}

	ctx := context.Background()

	// Step 1: Test bucket existence
	fmt.Printf("1. Testing bucket existence...\n")
	exists, err := bm.minioClient.BucketExists(ctx, bm.minioConfig.Bucket)
	if err != nil {
		return fmt.Errorf("failed to check bucket existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("bucket '%s' does not exist", bm.minioConfig.Bucket)
	}
	fmt.Printf("   ✓ Bucket '%s' exists\n\n", bm.minioConfig.Bucket)

	// Step 2: Test write operation
	fmt.Printf("2. Testing write operation...\n")
	testObjectName := fmt.Sprintf(".connection-test-%d.txt", time.Now().Unix())

	// Apply bucket path prefix if configured
	if bm.minioConfig.BucketPath != "" {
		testObjectName = filepath.Join(bm.minioConfig.BucketPath, testObjectName)
	}

	testContent := []byte("This is a connection test file created by ciwg-cli")

	info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, testObjectName,
		strings.NewReader(string(testContent)), int64(len(testContent)), minio.PutObjectOptions{
			ContentType: "text/plain",
		})
	if err != nil {
		return fmt.Errorf("failed to write test object: %w", err)
	}
	fmt.Printf("   ✓ Successfully wrote test object '%s' (%d bytes)\n\n", testObjectName, info.Size)

	// Step 3: Test read operation
	fmt.Printf("3. Testing read operation...\n")
	object, err := bm.minioClient.GetObject(ctx, bm.minioConfig.Bucket, testObjectName, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to read test object: %w", err)
	}
	defer object.Close()

	readContent := make([]byte, len(testContent))
	n, err := object.Read(readContent)
	if err != nil && err.Error() != "EOF" {
		return fmt.Errorf("failed to read test object content: %w", err)
	}
	if n != len(testContent) {
		return fmt.Errorf("read size mismatch: expected %d, got %d", len(testContent), n)
	}
	if string(readContent) != string(testContent) {
		return fmt.Errorf("content mismatch: read content doesn't match written content")
	}
	fmt.Printf("   ✓ Successfully read test object and verified content\n\n")

	// Step 4: Test delete operation
	fmt.Printf("4. Testing delete operation...\n")
	err = bm.minioClient.RemoveObject(ctx, bm.minioConfig.Bucket, testObjectName, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete test object: %w", err)
	}
	fmt.Printf("   ✓ Successfully deleted test object\n")

	return nil
}

// initAWSClient initializes the AWS Glacier client if not already initialized
func (bm *BackupManager) initAWSClient() error {
	if bm.awsClient != nil {
		bm.logTrace("AWS client already initialized")
		return nil
	}

	bm.logDebug("Initializing AWS Glacier client")

	if bm.awsConfig == nil {
		bm.logDebug("AWS configuration is nil")
		return fmt.Errorf("AWS configuration is not set")
	}

	bm.logVerbose("AWS Config: Region=%s, Vault=%s, AccountID=%s",
		bm.awsConfig.Region, bm.awsConfig.Vault, bm.awsConfig.AccountID)

	// Create AWS config with static credentials
	// Build a custom HTTP client for AWS SDK to handle long uploads and avoid timing out
	bm.logTrace("Creating custom HTTP transport for AWS SDK")
	dialer := &net.Dialer{Timeout: 60 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   5 * time.Minute,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       5 * time.Minute,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
	}
	var httpClient *http.Client
	if bm.awsConfig.HTTPTimeout > 0 {
		bm.logDebug("Using AWS HTTP timeout: %s", bm.awsConfig.HTTPTimeout)
		httpClient = &http.Client{Timeout: bm.awsConfig.HTTPTimeout, Transport: tr}
	} else {
		bm.logDebug("Using no AWS HTTP timeout (requests can run indefinitely)")
		httpClient = &http.Client{Transport: tr}
	}

	bm.logTrace("Loading AWS default config")
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
		bm.logDebug("Failed to load AWS config: %v", err)
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	bm.logTrace("Creating Glacier client from config")
	bm.awsClient = glacier.NewFromConfig(cfg)

	// Verify vault exists
	bm.logVerbose("Verifying vault '%s' exists", bm.awsConfig.Vault)
	ctx := context.Background()
	accountID := bm.awsConfig.AccountID
	if accountID == "" {
		accountID = "-" // Use "-" to indicate current account
		bm.logTrace("Using '-' for current account")
	}
	_, err = bm.awsClient.DescribeVault(ctx, &glacier.DescribeVaultInput{
		AccountId: aws.String(accountID),
		VaultName: aws.String(bm.awsConfig.Vault),
	})
	if err != nil {
		bm.logDebug("DescribeVault failed: %v", err)
		return fmt.Errorf("vault %s does not exist or is not accessible: %w", bm.awsConfig.Vault, err)
	}

	bm.logDebug("AWS Glacier client initialized successfully")
	return nil
}

// TestAWSConnection tests the AWS Glacier connection with write/read/delete operations
func (bm *BackupManager) TestAWSConnection() error {
	if err := bm.initAWSClient(); err != nil {
		return err
	}

	ctx := context.Background()
	accountID := bm.awsConfig.AccountID
	if accountID == "" {
		accountID = "-"
	}

	// Step 1: Test vault existence
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
	fmt.Printf("   Number of archives: %d\n", describeOutput.NumberOfArchives)
	fmt.Printf("   Size: %d bytes\n\n", describeOutput.SizeInBytes)

	// Step 2: Test write operation (upload archive)
	fmt.Printf("2. Testing write operation...\n")
	testContent := []byte("This is an AWS Glacier connection test file created by ciwg-cli")
	testDescription := fmt.Sprintf("Connection test archive created at %d", time.Now().Unix())

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
	fmt.Printf("   Archive ID: %s\n\n", archiveID)

	// Step 3: Note about retrieval
	fmt.Printf("3. Archive retrieval test skipped\n")
	fmt.Printf("   Note: Glacier archive retrieval requires initiating a job and waiting 3-5 hours.\n")
	fmt.Printf("   This is not practical for a connection test.\n\n")

	// Step 4: Test delete operation
	fmt.Printf("4. Testing delete operation...\n")
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

// computeTreeHash calculates the AWS Glacier tree hash for data
// The tree hash is computed by splitting the data into 1MB chunks,
// hashing each chunk, then building a binary tree of hashes
func computeTreeHash(data []byte) string {
	const chunkSize = 1024 * 1024 // 1 MB

	// Calculate hashes for all 1MB chunks
	var hashes [][]byte
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[i:end]
		hash := sha256.Sum256(chunk)
		hashes = append(hashes, hash[:])
	}

	// Build the hash tree by repeatedly hashing pairs until we have one hash
	for len(hashes) > 1 {
		var newHashes [][]byte
		for i := 0; i < len(hashes); i += 2 {
			if i+1 < len(hashes) {
				// Hash the concatenation of two hashes
				combined := append(hashes[i], hashes[i+1]...)
				hash := sha256.Sum256(combined)
				newHashes = append(newHashes, hash[:])
			} else {
				// Odd one out, just carry it forward
				newHashes = append(newHashes, hashes[i])
			}
		}
		hashes = newHashes
	}

	// Return the final hash as hex string
	if len(hashes) == 0 {
		// Empty data case
		hash := sha256.Sum256([]byte{})
		return hex.EncodeToString(hash[:])
	}
	return hex.EncodeToString(hashes[0])
}

// UploadToAWS uploads data from a reader to AWS Glacier
// For streaming data, we need to buffer it first because Glacier requires
// calculating a tree-hash checksum which needs seekable data
func (bm *BackupManager) UploadToAWS(objectName string, reader io.Reader, size int64) error {
	bm.logDebug("UploadToAWS called with objectName=%s, size=%d", objectName, size)

	if err := bm.initAWSClient(); err != nil {
		bm.logDebug("Failed to initialize AWS client: %v", err)
		return err
	}
	bm.logTrace("AWS client initialized successfully")

	ctx := context.Background()
	accountID := bm.awsConfig.AccountID
	if accountID == "" {
		accountID = "-"
	}
	bm.logDebug("Using AWS account ID: %s", accountID)

	// Glacier uses archive description instead of object key
	archiveDescription := fmt.Sprintf("Backup: %s", objectName)
	bm.logVerbose("Archive description: %s", archiveDescription)

	// Create a temporary file to buffer the data
	// This is necessary because Glacier needs to calculate tree-hash which requires seekable data
	fmt.Printf("      [AWS] Creating temporary buffer file...\n")
	bufferStartTime := time.Now()
	// Attempt to create a temp file and if we fail with ENOSPC, cleanup
	// existing `glacier-*` temp files and retry once.
	tmpDir := os.TempDir()
	bm.logTrace("Temp directory: %s", tmpDir)
	tmpFile, err := os.CreateTemp(tmpDir, "glacier-upload-*.tmp")
	if err != nil {
		bm.logDebug("Failed to create temp file: %v", err)
		if errors.Is(err, syscall.ENOSPC) {
			fmt.Printf("      [AWS] Disk full - attempting to clean up other glacier temp files in %s\n", tmpDir)
			bm.logVerbose("ENOSPC detected, attempting cleanup")
			if deleted, derr := cleanupGlacierTempFiles(tmpDir); derr != nil {
				fmt.Printf("      [AWS] Failed to cleanup temp files: %v\n", derr)
				bm.logDebug("Cleanup failed: %v", derr)
			} else {
				fmt.Printf("      [AWS] Removed %d old glacier temp file(s)\n", deleted)
				bm.logVerbose("Cleanup removed %d files", deleted)
			}
			// Try to create the temp file again
			tmpFile, err = os.CreateTemp(tmpDir, "glacier-upload-*.tmp")
			if err != nil {
				bm.logDebug("Failed to create temp file after cleanup: %v", err)
				return fmt.Errorf("failed to create temporary file after cleanup: %w", err)
			}
			bm.logVerbose("Successfully created temp file after cleanup: %s", tmpFile.Name())
		} else {
			return fmt.Errorf("failed to create temporary file: %w", err)
		}
	}
	bm.logVerbose("Created temporary buffer file: %s", tmpFile.Name())
	// Ensure the file is closed and removed. We remove explicitly after upload
	// completes successfully to free space immediately.
	defer func() {
		tmpFile.Close()
		// best-effort remove
		err := os.Remove(tmpFile.Name())

		if err == nil {
			fmt.Printf("      [AWS] Removed temporary buffer file %s\n", tmpFile.Name())
		} else {
			fmt.Printf("      [AWS] Failed to remove temporary buffer file %s: %v\n", tmpFile.Name(), err)
		}
	}()

	// Copy data from reader to temp file and calculate checksums
	fmt.Printf("      [AWS] Buffering stream to temporary file...\n")
	bm.logTrace("Starting io.Copy from reader to temp file")
	written, err := io.Copy(tmpFile, reader)
	bufferEndTime := time.Now()
	bufferDuration := bufferEndTime.Sub(bufferStartTime)
	bm.logDebug("io.Copy completed: written=%d bytes, duration=%s, err=%v", written, bufferDuration, err)
	if err != nil {
		// If copying fails due to ENOSPC, attempt to clean up other glacier temp files
		if errors.Is(err, syscall.ENOSPC) {
			fmt.Printf("      [AWS] Buffering failed: disk full (ENOSPC). Attempting to cleanup other glacier temp files...\n")
			bm.logVerbose("ENOSPC during buffering, attempting cleanup")
			if deleted, derr := cleanupGlacierTempFiles(tmpDir); derr != nil {
				fmt.Printf("      [AWS] Failed to cleanup temp files: %v\n", derr)
				bm.logDebug("Cleanup failed: %v", derr)
			} else {
				fmt.Printf("      [AWS] Removed %d old glacier temp file(s)\n", deleted)
				bm.logVerbose("Cleanup removed %d files", deleted)
			}
			// Can't reliably resume the copy for non-seekable readers, so return an error
			return fmt.Errorf("failed to buffer data to temporary file (disk full): %w", err)
		}
		return fmt.Errorf("failed to buffer data to temporary file: %w", err)
	}
	fmt.Printf("      [AWS] Buffered %d bytes (%.2f MB) in %s (%.2f MB/s)\n",
		written,
		float64(written)/(1024*1024),
		bufferDuration,
		float64(written)/(1024*1024)/bufferDuration.Seconds())

	// Read the file content for checksum calculation
	fmt.Printf("      [AWS] Reading buffered data for checksum calculation...\n")
	checksumStartTime := time.Now()
	bm.logTrace("Seeking to beginning of temp file for checksum")
	if _, err := tmpFile.Seek(0, 0); err != nil {
		bm.logDebug("Seek failed: %v", err)
		return fmt.Errorf("failed to seek temporary file for checksum: %w", err)
	}

	bm.logTrace("Reading all data from temp file")
	data, err := io.ReadAll(tmpFile)
	if err != nil {
		bm.logDebug("ReadAll failed: %v", err)
		return fmt.Errorf("failed to read temporary file for checksum: %w", err)
	}
	bm.logDebug("Read %d bytes from temp file", len(data))

	// Calculate the required checksums
	fmt.Printf("      [AWS] Calculating tree hash and linear hash...\n")
	bm.logTrace("Computing tree hash")
	treeHash := computeTreeHash(data)
	bm.logTrace("Computing linear SHA256 hash")
	linearHash := sha256.Sum256(data)
	linearHashHex := hex.EncodeToString(linearHash[:])
	checksumEndTime := time.Now()
	checksumDuration := checksumEndTime.Sub(checksumStartTime)
	fmt.Printf("      [AWS] Checksums calculated in %s\n", checksumDuration)
	fmt.Printf("      [AWS] Tree hash: %s\n", treeHash[:16]+"...")
	fmt.Printf("      [AWS] Linear hash: %s\n", linearHashHex[:16]+"...")
	bm.logDebug("Full tree hash: %s", treeHash)
	bm.logDebug("Full linear hash: %s", linearHashHex)

	// Get the file size for Content-Length
	fileSize := int64(len(data))
	bm.logDebug("File size for upload: %d bytes", fileSize)

	// Seek back to beginning for upload
	bm.logTrace("Seeking back to beginning for upload")
	if _, err := tmpFile.Seek(0, 0); err != nil {
		bm.logDebug("Seek failed: %v", err)
		return fmt.Errorf("failed to seek temporary file for upload: %w", err)
	}

	fmt.Printf("      [AWS] Initiating upload to Glacier vault '%s'...\n", bm.awsConfig.Vault)
	fmt.Printf("      [AWS] Archive: %s\n", archiveDescription)
	fmt.Printf("      [AWS] Size: %.2f MB\n", float64(fileSize)/(1024*1024))
	bm.logVerbose("Vault: %s, Region: %s, Account: %s", bm.awsConfig.Vault, bm.awsConfig.Region, accountID)

	// Set the payload hash in the context for the AWS signer to use in signature calculation
	bm.logTrace("Setting payload hash in context")
	ctx = v4.SetPayloadHash(ctx, linearHashHex)

	// Capture values for closure to avoid variable capture issues
	contentHash := linearHashHex
	contentLength := fileSize

	// Upload with explicitly calculated checksums
	// We need to add the x-amz-content-sha256 header explicitly via middleware
	uploadStartTime := time.Now()
	bm.logTrace("Calling UploadArchive API")
	bm.logDebug("UploadArchive parameters: vault=%s, account=%s, description=%s, checksum=%s, size=%d",
		bm.awsConfig.Vault, accountID, archiveDescription, treeHash, fileSize)
	uploadResult, err := bm.awsClient.UploadArchive(ctx, &glacier.UploadArchiveInput{
		AccountId:          aws.String(accountID),
		VaultName:          aws.String(bm.awsConfig.Vault),
		ArchiveDescription: aws.String(archiveDescription),
		Body:               tmpFile,
		Checksum:           aws.String(treeHash),
	}, func(o *glacier.Options) {
		// Add middleware to set x-amz-content-sha256 header and Content-Length
		// This is required by Glacier and must match the hash used in signature calculation
		bm.logTrace("Configuring Glacier upload options with middleware")
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			return stack.Build.Add(middleware.BuildMiddlewareFunc(
				"AddContentSHA256Header",
				func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (
					middleware.BuildOutput, middleware.Metadata, error,
				) {
					req, ok := in.Request.(*smithyhttp.Request)
					if ok {
						bm.logTrace("Setting x-amz-content-sha256: %s", contentHash)
						bm.logTrace("Setting Content-Length: %d", contentLength)
						req.Header.Set("x-amz-content-sha256", contentHash)
						req.Header.Set("Content-Length", fmt.Sprintf("%d", contentLength))
					}
					return next.HandleBuild(ctx, in)
				},
			), middleware.Before)
		})
	})
	uploadEndTime := time.Now()
	uploadDuration := uploadEndTime.Sub(uploadStartTime)
	bm.logDebug("UploadArchive API completed: duration=%s, err=%v", uploadDuration, err)
	if err != nil {
		fmt.Printf("      [AWS] Upload failed after %s: %v\n", uploadDuration, err)
		bm.logVerbose("Full error: %+v", err)
		return fmt.Errorf("failed to upload to AWS Glacier: %w", err)
	}

	// Upload success - remove the temp buffer file immediately
	bm.logTrace("Attempting to remove temp file: %s", tmpFile.Name())
	if err := os.Remove(tmpFile.Name()); err == nil {
		fmt.Printf("      [AWS] Removed temporary buffer file %s\n", tmpFile.Name())
		bm.logDebug("Successfully removed temp file")
	} else {
		fmt.Printf("      [AWS] Warning: failed to delete temp file %s: %v\n", tmpFile.Name(), err)
		bm.logDebug("Failed to remove temp file: %v", err)
	}
	uploadMBps := float64(fileSize) / (1024 * 1024) / uploadDuration.Seconds()
	fmt.Printf("      [AWS] Upload completed in %s (%.2f MB/s)\n", uploadDuration, uploadMBps)
	if uploadResult.ArchiveId != nil {
		fmt.Printf("      [AWS] Archive ID: %s...\n", (*uploadResult.ArchiveId)[:40])
		bm.logVerbose("Full Archive ID: %s", *uploadResult.ArchiveId)
	} else {
		bm.logDebug("Warning: ArchiveId is nil in upload result")
	}

	bm.logDebug("UploadToAWS completed successfully")
	return nil
}

// ListAWSBackups lists archives in the AWS Glacier vault
// Note: Glacier does not support direct listing of archives. This function initiates
// an inventory retrieval job. The actual inventory takes 3-5 hours to complete.
// For immediate listing, you must retrieve a previously completed inventory job.
func (bm *BackupManager) ListAWSBackups(prefix string, limit int) ([]ObjectInfo, error) {
	if err := bm.initAWSClient(); err != nil {
		return nil, err
	}

	fmt.Println("Warning: AWS Glacier does not support immediate archive listing.")
	fmt.Println("Archive inventory requires initiating a job that takes 3-5 hours to complete.")
	fmt.Println("To list archives, you must:")
	fmt.Println("  1. Initiate an inventory job using AWS Glacier API")
	fmt.Println("  2. Wait 3-5 hours for the job to complete")
	fmt.Println("  3. Retrieve the job output to get the archive list")
	fmt.Println("\nFor now, this function returns an empty list.")

	// Return empty list - actual implementation would require job management
	return []ObjectInfo{}, nil
}

// cleanupGlacierTempFiles deletes old temporary files used by glacier uploads
// in the provided tmpDir. It returns the number of files deleted and any error
// encountered while reading or deleting files.
func cleanupGlacierTempFiles(tmpDir string) (int, error) {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read temp dir %s: %w", tmpDir, err)
	}
	deleted := 0
	for _, e := range entries {
		// We only target regular files
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "glacier-upload-") || strings.HasPrefix(name, "glacier-migrate-") {
			path := filepath.Join(tmpDir, name)
			if err := os.Remove(path); err != nil {
				// Best effort: log and continue
				fmt.Printf("      [AWS] Warning: failed to remove old temp file %s: %v\n", path, err)
				continue
			}
			deleted++
		}
	}
	return deleted, nil
}

// DeleteAWSObjects deletes multiple archives from AWS Glacier
// Note: In Glacier, 'keys' should be archive IDs, not object keys
func (bm *BackupManager) DeleteAWSObjects(archiveIDs []string) error {
	if err := bm.initAWSClient(); err != nil {
		return err
	}

	if len(archiveIDs) == 0 {
		return nil
	}

	ctx := context.Background()
	accountID := bm.awsConfig.AccountID
	if accountID == "" {
		accountID = "-"
	}

	// Glacier doesn't have batch delete - must delete one at a time
	for _, archiveID := range archiveIDs {
		_, err := bm.awsClient.DeleteArchive(ctx, &glacier.DeleteArchiveInput{
			AccountId: aws.String(accountID),
			VaultName: aws.String(bm.awsConfig.Vault),
			ArchiveId: aws.String(archiveID),
		})
		if err != nil {
			return fmt.Errorf("failed to delete archive %s: %w", archiveID, err)
		}
	}

	return nil
}

// GetStorageCapacity checks the disk usage of the Minio storage path
func (bm *BackupManager) GetStorageCapacity(path string) (*StorageCapacity, error) {
	if path == "" {
		path = "/" // Default to root filesystem
	}

	// Check if we have an SSH client (remote check)
	if bm.sshClient != nil {
		return bm.getRemoteStorageCapacity(path)
	}

	// Local check using syscall
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return nil, fmt.Errorf("failed to get filesystem stats for %s: %w", path, err)
	}

	// Calculate capacity
	total := stat.Blocks * uint64(stat.Bsize)
	available := stat.Bavail * uint64(stat.Bsize)
	used := total - available
	usedPercent := (float64(used) / float64(total)) * 100

	capacity := &StorageCapacity{
		Total:       total,
		Used:        used,
		Available:   available,
		UsedPercent: usedPercent,
		Path:        path,
	}

	// Warning if checking root filesystem - likely wrong for dedicated Minio mounts
	if path == "/" {
		fmt.Println("\n⚠️  WARNING: Checking root filesystem capacity (/).")
		fmt.Println("   If Minio data is on a separate mount (e.g., /mnt/minio_nyc2),")
		fmt.Println("   use --storage-path flag to specify the correct mount point.")
		fmt.Println("   Example: --storage-path /mnt/minio_nyc2")
		fmt.Println("\n   To see all mount points, run: df -h | grep -E 'Filesystem|minio'")
		fmt.Println()
	}

	return capacity, nil
}

// getRemoteStorageCapacity checks disk usage on a remote server via SSH
func (bm *BackupManager) getRemoteStorageCapacity(path string) (*StorageCapacity, error) {
	// Use df command to get disk usage for the path
	cmd := fmt.Sprintf("df -B1 %s | tail -n 1", path)
	stdout, stderr, err := bm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to execute df command on remote server: %w (stderr: %s)", err, stderr)
	}

	// Parse df output: Filesystem 1B-blocks Used Available Use% Mounted
	// Example: /dev/sda 532575944704 532575166464 0 100% /mnt/minio_nyc2
	fields := strings.Fields(strings.TrimSpace(stdout))
	if len(fields) < 6 {
		return nil, fmt.Errorf("unexpected df output format: %s", stdout)
	}

	// Parse the numeric fields
	total, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse total size: %w", err)
	}

	used, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse used size: %w", err)
	}

	available, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse available size: %w", err)
	}

	// Parse percentage (remove % sign)
	percentStr := strings.TrimSuffix(fields[4], "%")
	usedPercent, err := strconv.ParseFloat(percentStr, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse usage percentage: %w", err)
	}

	mountPoint := fields[5]

	return &StorageCapacity{
		Total:       total,
		Used:        used,
		Available:   available,
		UsedPercent: usedPercent,
		Path:        mountPoint,
	}, nil
}

// ListMountPoints returns information about filesystem mount points (helper for debugging)
func ListMountPoints() (string, error) {
	cmd := exec.Command("df", "-h")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to list mount points: %w", err)
	}
	return string(output), nil
}

// MigrateOldestBackupsToGlacier migrates the oldest N% of backups from Minio to Glacier
func (bm *BackupManager) MigrateOldestBackupsToGlacier(percent float64, dryRun bool) error {
	if err := bm.initMinioClient(); err != nil {
		return fmt.Errorf("failed to initialize Minio client: %w", err)
	}

	if dryRun {
		fmt.Println("🔍 DRY RUN MODE: No backups will be migrated or deleted")
		fmt.Println()
	}

	if !dryRun {
		if err := bm.initAWSClient(); err != nil {
			return fmt.Errorf("failed to initialize AWS Glacier client: %w", err)
		}

		// Clean up any stale glacier temp files from previous runs before starting
		tmpDir := os.TempDir()
		if deleted, err := cleanupGlacierTempFiles(tmpDir); err != nil {
			fmt.Printf("⚠️  Warning: Failed to cleanup old glacier temp files: %v\n", err)
		} else if deleted > 0 {
			fmt.Printf("🧹 Cleaned up %d stale glacier temp file(s) from previous runs\n", deleted)
		}
	}

	ctx := context.Background()

	// List all backups from Minio
	type BackupInfo struct {
		Name         string
		LastModified time.Time
		Size         int64
	}

	var backups []BackupInfo
	objectCh := bm.minioClient.ListObjects(ctx, bm.minioConfig.Bucket, minio.ListObjectsOptions{
		Recursive: true,
	})

	for object := range objectCh {
		if object.Err != nil {
			return fmt.Errorf("error listing objects: %w", object.Err)
		}
		backups = append(backups, BackupInfo{
			Name:         object.Key,
			LastModified: object.LastModified,
			Size:         object.Size,
		})
	}

	if len(backups) == 0 {
		fmt.Println("No backups found in Minio to migrate.")
		return nil
	}

	// Sort backups by date (oldest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].LastModified.Before(backups[j].LastModified)
	})

	// Calculate how many backups to migrate
	numToMigrate := int(math.Ceil(float64(len(backups)) * percent / 100.0))
	if numToMigrate == 0 {
		fmt.Println("No backups to migrate based on the specified percentage.")
		return nil
	}

	fmt.Printf("Migrating %d oldest backups (%.1f%%) from Minio to AWS Glacier...\n", numToMigrate, percent)
	if dryRun {
		fmt.Println("\n📋 MIGRATION PLAN (no changes will be made):")
		fmt.Println()
	}

	// Migrate each backup
	migratedCount := 0
	var totalFreed int64

	for i := 0; i < numToMigrate; i++ {
		backup := backups[i]

		// Format timestamps in both international (ISO 8601) and US formats
		intlDate := backup.LastModified.Format("2006-01-02 15:04:05 MST")  // International: YYYY-MM-DD
		usDate := backup.LastModified.Format("01/02/2006 03:04:05 PM MST") // US: MM/DD/YYYY

		if dryRun {
			fmt.Printf("\n[%d/%d] WOULD MIGRATE: %s\n", i+1, numToMigrate, backup.Name)
			fmt.Printf("  📦 Size:           %.2f MB (%.2f GB)\n",
				float64(backup.Size)/(1024*1024),
				float64(backup.Size)/(1024*1024*1024))
			fmt.Printf("  📅 Modified (Intl): %s\n", intlDate)
			fmt.Printf("  📅 Modified (US):   %s\n", usDate)
			fmt.Printf("  📤 Would upload to: AWS Glacier vault '%s'\n", bm.awsConfig.Vault)
			fmt.Printf("  🗑️  Would delete from: Minio bucket '%s'\n", bm.minioConfig.Bucket)
			totalFreed += backup.Size
			migratedCount++
			continue
		}

		fmt.Printf("Migrating backup %d/%d: %s (%.2f MB)\n",
			i+1, numToMigrate, backup.Name,
			float64(backup.Size)/(1024*1024))
		fmt.Printf("  📅 Modified (Intl): %s\n", intlDate)
		fmt.Printf("  📅 Modified (US):   %s\n", usDate)

		// Download from Minio
		object, err := bm.minioClient.GetObject(ctx, bm.minioConfig.Bucket, backup.Name, minio.GetObjectOptions{})
		if err != nil {
			fmt.Printf("  ⚠ Failed to download %s from Minio: %v\n", backup.Name, err)
			continue
		}

		// Buffer to temporary file (memory-efficient and provides seekable handle for AWS SDK)
		// This mimics the robust logic from UploadToAWS and allows the SDK to calculate
		// both x-amz-content-sha256 (linear hash) and x-amz-sha256-tree-hash (tree hash)
		tmpFile, err := os.CreateTemp("", "glacier-migrate-*.tmp")
		if err != nil {
			fmt.Printf("  ⚠ Failed to create temporary file: %v\n", err)
			object.Close()
			continue
		}
		// Ensure we close and remove the temp file when done
		defer os.Remove(tmpFile.Name())
		defer tmpFile.Close()

		// Copy data from Minio stream to the temporary file
		if _, err := io.Copy(tmpFile, object); err != nil {
			fmt.Printf("  ⚠ Failed to buffer data to temporary file: %v\n", err)
			object.Close()
			continue
		}
		object.Close() // Done with the Minio stream

		// Read the temp file into memory for checksum calculation
		// Seek to the beginning first
		if _, err := tmpFile.Seek(0, 0); err != nil {
			fmt.Printf("  ⚠ Failed to seek temporary file for checksum: %v\n", err)
			continue
		}

		data, err := io.ReadAll(tmpFile)
		if err != nil {
			fmt.Printf("  ⚠ Failed to read temporary file for checksum: %v\n", err)
			continue
		}

		// Calculate the AWS Glacier tree hash checksum (required for upload)
		fmt.Printf("  ℹ️  Calculating tree hash for %s...\n", backup.Name)
		treeHash := computeTreeHash(data)

		// Calculate the linear hash (x-amz-content-sha256)
		linearHash := sha256.Sum256(data)
		linearHashHex := hex.EncodeToString(linearHash[:])

		// Get the file size for Content-Length
		fileSize := int64(len(data))

		// Debug: Show actual file size
		fmt.Printf("  ℹ️  File size: %d bytes (%.2f MB)\n", fileSize, float64(fileSize)/(1024*1024))

		// Skip empty files
		if fileSize == 0 {
			fmt.Printf("  ⚠ Skipping empty file: %s\n", backup.Name)
			continue
		}

		// Seek back to beginning for the upload
		if _, err := tmpFile.Seek(0, 0); err != nil {
			fmt.Printf("  ⚠ Failed to seek temporary file for upload: %v\n", err)
			continue
		}

		accountID := bm.awsConfig.AccountID
		if accountID == "" || accountID == "-" {
			accountID = "-"
		}

		// Set the payload hash in the context for the AWS signer to use in signature calculation
		ctx = v4.SetPayloadHash(ctx, linearHashHex)

		// Capture values for closure to avoid variable capture issues
		contentHash := linearHashHex
		contentLength := fileSize

		// Upload with explicitly calculated checksums
		// We need to add the x-amz-content-sha256 header explicitly via middleware
		uploadResult, err := bm.awsClient.UploadArchive(ctx, &glacier.UploadArchiveInput{
			VaultName:          aws.String(bm.awsConfig.Vault),
			AccountId:          aws.String(accountID),
			ArchiveDescription: aws.String(fmt.Sprintf("Migrated from Minio: %s", backup.Name)),
			Body:               tmpFile,
			Checksum:           aws.String(treeHash),
		}, func(o *glacier.Options) {
			// Add middleware to set x-amz-content-sha256 header and Content-Length
			// This is required by Glacier and must match the hash used in signature calculation
			o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
				return stack.Build.Add(middleware.BuildMiddlewareFunc(
					"AddContentSHA256Header",
					func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (
						middleware.BuildOutput, middleware.Metadata, error,
					) {
						req, ok := in.Request.(*smithyhttp.Request)
						if ok {
							req.Header.Set("x-amz-content-sha256", contentHash)
							req.Header.Set("Content-Length", fmt.Sprintf("%d", contentLength))
						}
						return next.HandleBuild(ctx, in)
					},
				), middleware.Before)
			})
		})
		if err != nil {
			fmt.Printf("  ⚠ Failed to upload %s to Glacier: %v\n", backup.Name, err)
			continue
		}

		fmt.Printf("  ✓ Uploaded to Glacier (Archive ID: %s...)\n", (*uploadResult.ArchiveId)[:40])

		// Delete from Minio
		err = bm.minioClient.RemoveObject(ctx, bm.minioConfig.Bucket, backup.Name, minio.RemoveObjectOptions{})
		if err != nil {
			fmt.Printf("  ⚠ Failed to delete %s from Minio after migration: %v\n", backup.Name, err)
			// Continue anyway - backup is already in Glacier
		} else {
			fmt.Printf("  ✓ Deleted from Minio\n")
			totalFreed += backup.Size
			migratedCount++
			fmt.Printf("  Progress: %d/%d migrated, freed: %.2f MB\n", migratedCount, numToMigrate, float64(totalFreed)/(1024*1024))
		}
	}

	if dryRun {
		fmt.Println()
		fmt.Println(strings.Repeat("=", 70))
		fmt.Println("📊 DRY RUN SUMMARY")
		fmt.Println(strings.Repeat("=", 70))
		fmt.Printf("Would migrate:     %d backups\n", migratedCount)
		fmt.Printf("Would free:        %.2f MB (%.2f GB)\n",
			float64(totalFreed)/(1024*1024),
			float64(totalFreed)/(1024*1024*1024))
		fmt.Printf("Source:            Minio bucket '%s'\n", bm.minioConfig.Bucket)
		fmt.Printf("Destination:       AWS Glacier vault '%s'\n", bm.awsConfig.Vault)
		fmt.Println()
		fmt.Println("ℹ️  No changes were made. Run without --dry-run to perform migration.")
		fmt.Println(strings.Repeat("=", 70))
	} else {
		fmt.Printf("\n✓ Migration complete: %d/%d backups migrated, %.2f MB (%.2f GB) freed\n",
			migratedCount, numToMigrate,
			float64(totalFreed)/(1024*1024),
			float64(totalFreed)/(1024*1024*1024))
	}

	return nil
}

// MonitorAndMigrateIfNeeded checks storage capacity and migrates backups if threshold exceeded
func (bm *BackupManager) MonitorAndMigrateIfNeeded(storagePath string, threshold float64, migratePercent float64, dryRun bool) error {
	fmt.Printf("Monitoring storage capacity at %s (threshold: %.1f%%)\n", storagePath, threshold)
	if dryRun {
		fmt.Println("🔍 DRY RUN MODE: No actual migrations will be performed")
	}

	maxIterations := 10 // Prevent infinite loops
	iteration := 0

	for iteration < maxIterations {
		iteration++

		capacity, err := bm.GetStorageCapacity(storagePath)
		if err != nil {
			return fmt.Errorf("failed to get storage capacity: %w", err)
		}

		fmt.Printf("\nIteration %d - Storage Status:\n", iteration)
		fmt.Printf("  Total:     %.2f GB\n", float64(capacity.Total)/(1024*1024*1024))
		fmt.Printf("  Used:      %.2f GB (%.1f%%)\n",
			float64(capacity.Used)/(1024*1024*1024), capacity.UsedPercent)
		fmt.Printf("  Available: %.2f GB\n", float64(capacity.Available)/(1024*1024*1024))

		if capacity.UsedPercent <= threshold {
			fmt.Printf("\n✓ Storage usage (%.1f%%) is within threshold (%.1f%%)\n",
				capacity.UsedPercent, threshold)
			if dryRun {
				fmt.Println("ℹ️  No migration needed in this dry run")
			}
			return nil
		}

		fmt.Printf("\n⚠ Storage usage (%.1f%%) exceeds threshold (%.1f%%)\n",
			capacity.UsedPercent, threshold)
		if dryRun {
			fmt.Printf("  Would start migration of %.1f%% oldest backups to AWS Glacier...\n", migratePercent)
		} else {
			fmt.Printf("  Starting migration of %.1f%% oldest backups to AWS Glacier...\n", migratePercent)
		}

		err = bm.MigrateOldestBackupsToGlacier(migratePercent, dryRun)
		if err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}

		if dryRun {
			// In dry run mode, don't iterate - just show what would happen once
			fmt.Println("\nℹ️  Dry run complete. Only one iteration performed for preview.")
			return nil
		}

		// Wait a moment for filesystem to update
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("storage capacity still exceeds threshold after %d iterations", maxIterations)
}

func (bm *BackupManager) CreateBackups(options *BackupOptions) error {
	// Check capacity if RespectCapacityLimit is enabled
	if options.RespectCapacityLimit {
		// Default threshold to 95% if not specified
		threshold := options.CapacityThreshold
		if threshold <= 0 {
			threshold = 95.0
		}

		storagePath := "/" // Default to root filesystem
		if bm.minioConfig.Endpoint != "" {
			// Try to infer storage path from Minio endpoint if it's a local path
			// In most cases, Minio will be on the same server
			storagePath = "/"
		}

		capacity, err := bm.GetStorageCapacity(storagePath)
		if err != nil {
			return fmt.Errorf("failed to check storage capacity: %w", err)
		}

		if capacity.UsedPercent > threshold {
			return fmt.Errorf("storage capacity exceeds %.1f%% (current: %.1f%%). Cannot create backup. Please run 'backup monitor' to free up space", threshold, capacity.UsedPercent)
		}

		fmt.Printf("✓ Storage capacity check passed: %.1f%% used (threshold: %.1f%%)\n", capacity.UsedPercent, threshold)
	}

	if err := bm.initMinioClient(); err != nil {
		return err
	}

	containers, err := bm.getContainers(options)
	if err != nil {
		return err
	}

	if len(containers) == 0 {
		fmt.Println("No containers found to process.")
		return nil
	}

	total := len(containers)
	processed := 0
	successCount := 0
	failedCount := 0
	var totalCompressed int64
	var totalUncompressed int64
	awsUploads := 0

	for idx, container := range containers {
		processed++
		fmt.Printf("\n--- [%d/%d] Processing container: %s ---\n", idx+1, total, container.Name)
		compressedSize, awsUploaded, err := bm.processContainer(container, options)
		if err != nil {
			fmt.Printf("Error processing container %s: %v\n", container.Name, err)
			failedCount++
			continue
		}
		successCount++
		totalCompressed += compressedSize
		if awsUploaded {
			awsUploads++
		}
		// Attempt to calculate uncompressed size for the container if available
		if container.WorkingDir != "" {
			if size, err := bm.getDirectorySize(container.WorkingDir, options.ParentDir); err == nil {
				totalUncompressed += size
			}
		}
		// Show interim aggregated progress
		fmt.Printf("Progress: %d/%d processed, %d succeeded, %d failed\n", processed, total, successCount, failedCount)
		fmt.Printf("Aggregate compressed: %.2f MB, Aggregate uncompressed: %.2f MB\n",
			float64(totalCompressed)/(1024*1024),
			float64(totalUncompressed)/(1024*1024))
		if totalUncompressed > 0 {
			ratio := (1.0 - float64(totalCompressed)/float64(totalUncompressed)) * 100
			fmt.Printf("Overall compression: %.1f%% space saved\n", ratio)
		}
		if awsUploads > 0 {
			fmt.Printf("AWS Glacier uploads: %d\n", awsUploads)
		}
	}

	return nil
}

// GetContainersFromOptions returns the list of containers that would be processed
// based on the provided options. This is useful for determining which backups to clean up.
func (bm *BackupManager) GetContainersFromOptions(options *BackupOptions) ([]ContainerInfo, error) {
	return bm.getContainers(options)
}

func (bm *BackupManager) getContainers(options *BackupOptions) ([]ContainerInfo, error) {
	var containerInputs []string

	// If config file is provided, load it and return containers from config
	if options.ConfigFile != "" {
		return bm.getContainersFromConfig(options.ConfigFile)
	}

	// Read from file if specified
	if options.ContainerFile != "" {
		content, err := bm.readRemoteFile(options.ContainerFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read container file: %w", err)
		}

		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				containerInputs = append(containerInputs, line)
			}
		}
	}

	// Add from container-name flag
	if options.ContainerName != "" {
		names := strings.Split(options.ContainerName, "|")
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" {
				containerInputs = append(containerInputs, name)
			}
		}
	}

	// Add from container-names flag (already parsed into a slice)
	if len(options.ContainerNames) > 0 {
		for _, name := range options.ContainerNames {
			name = strings.TrimSpace(name)
			if name != "" {
				containerInputs = append(containerInputs, name)
			}
		}
	}

	// If no inputs, get all wp_ containers
	if len(containerInputs) == 0 {
		containers, err := bm.getWPContainers()
		if err != nil {
			return nil, err
		}
		return containers, nil
	}

	// Process inputs
	var containers []ContainerInfo
	for _, input := range containerInputs {
		container, err := bm.resolveContainer(input)
		if err != nil {
			fmt.Printf("Warning: %v. Skipping...\n", err)
			continue
		}
		containers = append(containers, container)
	}

	return containers, nil
}

func (bm *BackupManager) getWPContainers() ([]ContainerInfo, error) {
	// Get all wp_ containers
	cmd := `docker ps --format '{{.Names}}' | grep '^wp_'`
	output, stderr, err := bm.executeCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to list wp containers: %w (stderr: %s)", err, stderr)
	}

	var containers []ContainerInfo
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		workingDir, err := bm.getContainerWorkingDir(line)
		if err != nil {
			fmt.Printf("Warning: failed to get working dir for %s: %v\n", line, err)
			continue
		}

		containers = append(containers, ContainerInfo{
			Name:       line,
			WorkingDir: workingDir,
		})
	}

	return containers, nil
}

func (bm *BackupManager) resolveContainer(input string) (ContainerInfo, error) {
	// If it's an absolute path, treat as working directory
	if strings.HasPrefix(input, "/") {
		containerName, err := bm.findContainerByWorkingDir(input)
		if err != nil {
			return ContainerInfo{}, fmt.Errorf("no running container found for directory '%s'", input)
		}
		return ContainerInfo{Name: containerName, WorkingDir: input}, nil
	}

	// Try as container name first
	workingDir, err := bm.getContainerWorkingDir(input)
	if err == nil {
		return ContainerInfo{Name: input, WorkingDir: workingDir}, nil
	}

	// Try as directory under /var/opt
	candidateDir := "/var/opt/" + input
	containerName, err := bm.findContainerByWorkingDir(candidateDir)
	if err == nil {
		return ContainerInfo{Name: containerName, WorkingDir: candidateDir}, nil
	}

	return ContainerInfo{}, fmt.Errorf("no running container or directory found for '%s'", input)
}

func (bm *BackupManager) getContainerWorkingDir(containerName string) (string, error) {
	cmd := fmt.Sprintf(`docker inspect "%s" | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"'`, containerName)
	output, stderr, err := bm.executeCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container: %w (stderr: %s)", err, stderr)
	}

	workingDir := strings.TrimSpace(output)
	if workingDir == "null" || workingDir == "" {
		return "", fmt.Errorf("no working directory found")
	}

	return workingDir, nil
}

func (bm *BackupManager) findContainerByWorkingDir(workingDir string) (string, error) {
	cmd := `docker ps --format '{{.Names}}'`
	output, stderr, err := bm.executeCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %w (stderr: %s)", err, stderr)
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		containerName := strings.TrimSpace(line)
		if containerName == "" {
			continue
		}

		containerWorkingDir, err := bm.getContainerWorkingDir(containerName)
		if err != nil {
			continue
		}

		if containerWorkingDir == workingDir {
			return containerName, nil
		}
	}

	return "", fmt.Errorf("container not found")
}

func (bm *BackupManager) processContainer(container ContainerInfo, options *BackupOptions) (int64, bool, error) {
	fmt.Printf("Processing container: %s (type: %s)\n", container.Name, container.Type)
	fmt.Printf("Working directory: %s\n", container.WorkingDir)

	timestamp := time.Now().Format("20060102-150405")

	// Use custom label if provided, otherwise use working dir basename
	label := filepath.Base(container.WorkingDir)
	if container.Config != nil && container.Config.Label != "" {
		label = container.Config.Label
	}
	backupName := fmt.Sprintf("%s-%s.tgz", label, timestamp)

	if options.DryRun {
		fmt.Printf("[DRY RUN] Would process container %s\n", container.Name)
		if container.Type == "wordpress" || container.Type == "" {
			fmt.Printf("[DRY RUN] Would clean old SQL files in %s\n", container.Name)
			fmt.Printf("[DRY RUN] Would export WordPress DB in %s\n", container.Name)
		} else if container.Config != nil && container.Config.Database.Type != "" {
			fmt.Printf("[DRY RUN] Would export %s database\n", container.Config.Database.Type)
		}
		fmt.Printf("[DRY RUN] Would create and stream tarball %s to Minio\n", backupName)

		// Estimate compressed size if method specified
		var estimatedCompressed int64
		if options.EstimateMethod != "" {
			fmt.Printf("\n[DRY RUN] Estimating compressed size using '%s' method...\n", options.EstimateMethod)
			startTime := time.Now()

			compressedSize, uncompressedSize, err := bm.EstimateCompressedSize(
				container.WorkingDir,
				options.ParentDir,
				options.EstimateMethod,
				options.SampleSize,
			)

			duration := time.Since(startTime)

			if err != nil {
				fmt.Printf("[DRY RUN] ⚠️  Estimation failed: %v\n", err)
			} else {
				estimatedCompressed = compressedSize
				compressedMB := float64(compressedSize) / (1024 * 1024)
				uncompressedMB := float64(uncompressedSize) / (1024 * 1024)
				ratio := 0.0
				if uncompressedSize > 0 {
					ratio = (1.0 - float64(compressedSize)/float64(uncompressedSize)) * 100
				}

				fmt.Printf("[DRY RUN] 📊 Estimation complete (took %s):\n", duration.Round(time.Millisecond))
				fmt.Printf("[DRY RUN]    Uncompressed: %.2f MB (%d bytes)\n", uncompressedMB, uncompressedSize)
				fmt.Printf("[DRY RUN]    Estimated compressed: %.2f MB (%d bytes)\n", compressedMB, compressedSize)
				fmt.Printf("[DRY RUN]    Compression ratio: %.1f%% space saved\n", ratio)

				// Show accuracy note based on method
				switch options.EstimateMethod {
				case "heuristic":
					fmt.Printf("[DRY RUN]    Accuracy: ~80%% (instant file-type analysis)\n")
				case "sample":
					sampleMB := float64(options.SampleSize) / (1024 * 1024)
					fmt.Printf("[DRY RUN]    Accuracy: ~90%% (%.0f MB sample compressed)\n", sampleMB)
				case "accurate":
					fmt.Printf("[DRY RUN]    Accuracy: 100%% (full compression simulation)\n")
				}
			}
			fmt.Println()
		}

		if options.Delete {
			fmt.Printf("[DRY RUN] Would stop and remove container %s\n", container.Name)
			fmt.Printf("[DRY RUN] Would remove directory %s\n", container.WorkingDir)
		}
		fmt.Printf("Done with %s\n\n", container.Name)
		return estimatedCompressed, false, nil
	}

	// Run pre-backup commands if specified
	if container.Config != nil && len(container.Config.PreBackupCommands) > 0 {
		fmt.Printf("Running pre-backup commands...\n")
		for _, cmd := range container.Config.PreBackupCommands {
			fmt.Printf("  Running: %s\n", cmd)
			if _, stderr, err := bm.executeCommand(cmd); err != nil {
				return 0, false, fmt.Errorf("pre-backup command failed: %w (stderr: %s)", err, stderr)
			}
		}
	}

	// Handle database export based on container type
	if container.Type == "wordpress" || container.Type == "" {
		// WordPress-specific backup logic
		if err := bm.exportWordPressDatabase(container); err != nil {
			return 0, false, err
		}
	} else if container.Config != nil && container.Config.Database.Type != "" {
		// Custom database export
		if err := bm.exportDatabase(container, options); err != nil {
			return 0, false, err
		}
	}

	// Create and stream tarball to Minio
	siteName := filepath.Base(container.WorkingDir)
	fmt.Printf("\n📦 Creating tarball for %s...\n", siteName)

	// Determine backup directory - use custom app dir if specified
	backupDir := container.WorkingDir
	if container.Config != nil && container.Config.Paths.AppDir != "" {
		backupDir = container.Config.Paths.AppDir
	}

	var containerBucketPath string
	if container.Config != nil {
		containerBucketPath = container.Config.BucketPath
	}

	fmt.Printf("   Source: %s\n", backupDir)
	fmt.Printf("   Target: %s\n", backupName)

	// Get uncompressed directory size for compression ratio calculation
	fmt.Printf("   Calculating source size...\n")
	uncompressedSize, err := bm.getDirectorySize(backupDir, options.ParentDir)
	if err != nil {
		fmt.Printf("   ⚠️  Warning: Could not determine source size: %v\n", err)
		uncompressedSize = 0 // Continue anyway
	} else {
		uncompressedMB := float64(uncompressedSize) / (1024 * 1024)
		fmt.Printf("   Uncompressed: %.2f MB\n", uncompressedMB)
	}

	fmt.Printf("   Compressing and streaming...\n")

	compressedSize, awsUploaded, err := bm.streamBackupToMinio(backupDir, backupName, options.ParentDir, containerBucketPath, uncompressedSize, options.IncludeAWSGlacier)
	if err != nil {
		return 0, false, fmt.Errorf("failed to stream backup to Minio: %w", err)
	}

	// Calculate and display compression ratio
	if uncompressedSize > 0 && compressedSize > 0 {
		compressionRatio := (1.0 - float64(compressedSize)/float64(uncompressedSize)) * 100
		fmt.Printf("   💾 Compression: %.1f%% space saved\n", compressionRatio)
	}

	// Run post-backup commands if specified
	if container.Config != nil && len(container.Config.PostBackupCommands) > 0 {
		fmt.Printf("Running post-backup commands...\n")
		for _, cmd := range container.Config.PostBackupCommands {
			fmt.Printf("  Running: %s\n", cmd)
			if _, stderr, err := bm.executeCommand(cmd); err != nil {
				fmt.Printf("Warning: post-backup command failed: %v (stderr: %s)\n", err, stderr)
			}
		}
	}

	if options.Delete {
		fmt.Printf("Stopping and removing container %s...\n", container.Name)
		stopCmd := fmt.Sprintf(`docker stop "%s" 2>/dev/null || true`, container.Name)
		bm.executeCommand(stopCmd)

		removeCmd := fmt.Sprintf(`docker rm "%s" 2>/dev/null || true`, container.Name)
		bm.executeCommand(removeCmd)

		fmt.Printf("Removing directory %s...\n", container.WorkingDir)
		rmCmd := fmt.Sprintf(`rm -rf "%s"`, container.WorkingDir)
		if _, stderr, err := bm.executeCommand(rmCmd); err != nil {
			fmt.Printf("Warning: failed to remove directory: %v (stderr: %s)\n", err, stderr)
		}
	}

	fmt.Printf("Done with %s\n\n", container.Name)
	return compressedSize, awsUploaded, nil
}

// exportWordPressDatabase handles WordPress-specific database export
func (bm *BackupManager) exportWordPressDatabase(container ContainerInfo) error {
	// Clean all SQL files
	fmt.Printf("Cleaning all SQL files in %s...\n", container.Name)
	cleanCmd := fmt.Sprintf(`docker exec -u 0 "%s" find /var/www/html -name "*.sql" -type f -exec rm -f {} \;`, container.Name)
	if _, stderr, err := bm.executeCommand(cleanCmd); err != nil {
		fmt.Printf("Warning: failed to clean old SQL files: %v (stderr: %s)\n", err, stderr)
	}

	// Export database
	fmt.Printf("Removing existing SQL files in %s/www/wp-content...\n", container.WorkingDir)
	hostWPContent := filepath.Join(container.WorkingDir, "www", "wp-content")
	cleanHostCmd := fmt.Sprintf(`if [ -d "%s" ]; then find "%s" -name "*.sql" -type f -exec rm -f {} +; fi`, hostWPContent, hostWPContent)
	if _, stderr, err := bm.executeCommand(cleanHostCmd); err != nil {
		fmt.Printf("Warning: failed to remove existing SQL files from host wp-content: %v (stderr: %s)\n", err, stderr)
	}

	fmt.Printf("Exporting DB in %s...\n", container.Name)
	exportCmd := fmt.Sprintf(`docker exec -u 0 "%s" sh -c 'wp --allow-root db export && mv *.sql /var/www/html/wp-content/'`, container.Name)
	if _, stderr, err := bm.executeCommand(exportCmd); err != nil {
		return fmt.Errorf("failed to export database: %w (stderr: %s)", err, stderr)
	}

	return nil
}

// getDirectorySize returns the total size of a directory in bytes
func (bm *BackupManager) getDirectorySize(dirPath string, parentDir string) (int64, error) {
	// Try the primary path first
	var duCmd string
	duCmd = fmt.Sprintf(`du -sb "%s" 2>/dev/null | awk '{print $1}'`, dirPath)

	stdout, stderr, err := bm.executeCommand(duCmd)

	// If the primary path failed and we have a parentDir, try the fallback
	if (err != nil || strings.TrimSpace(stdout) == "") && parentDir != "" {
		altPath := filepath.Join(parentDir, filepath.Base(dirPath))
		duCmd = fmt.Sprintf(`du -sb "%s" 2>/dev/null | awk '{print $1}'`, altPath)
		stdout, stderr, err = bm.executeCommand(duCmd)
	}

	if err != nil {
		return 0, fmt.Errorf("failed to get directory size: %w (stderr: %s)", err, stderr)
	}

	trimmedOutput := strings.TrimSpace(stdout)
	if trimmedOutput == "" {
		return 0, fmt.Errorf("directory not found or empty output from du command")
	}

	size, err := strconv.ParseInt(trimmedOutput, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse directory size '%s': %w", trimmedOutput, err)
	}

	return size, nil
}

func (bm *BackupManager) streamBackupToMinio(workingDir, backupName, parentDir, containerBucketPath string, uncompressedSize int64, includeAWSGlacier bool) (int64, bool, error) {
	// Build a tar command that attempts the provided workingDir first and
	// falls back to parentDir/<basename> if the first path doesn't exist.
	// This works for both local and remote execution because we run the
	// command under a shell (bash -lc).
	var tarCmd string
	if parentDir != "" {
		alt := filepath.Join(parentDir, filepath.Base(workingDir))
		// Use a shell conditional so remote execution can choose the right path.
		tarCmd = fmt.Sprintf(`if [ -d "%s" ]; then tar -czf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s"; elif [ -d "%s" ]; then tar -czf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s"; else echo "tar: no such directory: %s" >&2; exit 2; fi`, workingDir, workingDir, alt, alt, workingDir)
	} else {
		tarCmd = fmt.Sprintf(`tar -czf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s"`, workingDir)
	}

	// Track whether an AWS upload completed successfully
	awsUploaded := false

	// If running locally (no ssh client) run tar locally and stream stdout to Minio
	if bm.sshClient == nil {
		cmd := exec.Command("bash", "-lc", tarCmd)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return 0, false, fmt.Errorf("failed to create stdout pipe for local tar: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return 0, false, fmt.Errorf("failed to start local tar command: %w", err)
		}

		ctx := context.Background()
		// Store backups in a directory named after the site (basename of workingDir)
		siteName := filepath.Base(workingDir)

		// If a container-specific bucket path is configured, it supersedes the
		// default `backups/<siteName>/...` structure. In that case place the
		// backup directly under the configured prefix. Otherwise if a global
		// MinioConfig.BucketPath is set use that. If neither is set, fall back
		// to the default backups/<siteName>/<backupName> layout.
		var objectName string
		if containerBucketPath != "" {
			objectName = filepath.Join(containerBucketPath, backupName)
		} else if bm.minioConfig != nil && bm.minioConfig.BucketPath != "" {
			objectName = filepath.Join(bm.minioConfig.BucketPath, backupName)
		} else {
			objectName = fmt.Sprintf("backups/%s/%s", siteName, backupName)
		}

		// If AWS is configured and includeAWSGlacier flag is set, upload to AWS first using TeeReader to capture data
		var reader io.Reader = stdout
		if includeAWSGlacier && bm.awsConfig != nil && bm.awsConfig.Vault != "" {
			if err := bm.initAWSClient(); err != nil {
				fmt.Printf("Warning: failed to initialize AWS client, skipping AWS upload: %v\n", err)
			} else {
				// Create a pipe to capture the tar output for AWS
				pr, pw := io.Pipe()

				// Use TeeReader to duplicate the stream
				reader = io.TeeReader(stdout, pw)

				// Upload to AWS in a goroutine
				awsErrChan := make(chan error, 1)
				go func() {
					defer pw.Close()
					awsStartTime := time.Now()
					fmt.Printf("   ☁️  Streaming to AWS Glacier...\n")
					fmt.Printf("      [AWS] Starting upload at %s\n", awsStartTime.Format("15:04:05"))
					err := bm.UploadToAWS(objectName, pr, -1)
					awsEndTime := time.Now()
					awsDuration := awsEndTime.Sub(awsStartTime)
					if err != nil {
						fmt.Printf("      [AWS] Failed after %s: %v\n", awsDuration, err)
						awsErrChan <- fmt.Errorf("AWS upload failed: %w", err)
					} else {
						fmt.Printf("      [AWS] Completed in %s\n", awsDuration)
						awsErrChan <- nil
					}
				}()

				// Continue with Minio upload using the TeeReader
				fmt.Printf("   📦 Streaming to Minio...\n")
				info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, reader, -1, minio.PutObjectOptions{
					ContentType: "application/gzip",
				})
				if err != nil {
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
					return 0, false, fmt.Errorf("failed to upload to Minio: %w", err)
				}

				// Wait for AWS upload to complete
				awsErr := <-awsErrChan
				awsUploaded := false
				if awsErr != nil {
					fmt.Printf("⚠️  Warning: %v\n", awsErr)
				} else {
					fmt.Printf("   ✓ AWS Glacier upload complete\n")
				}

				if err := cmd.Wait(); err != nil {
					// Treat tar exit code 1 for "file changed as we read it" as a non-fatal warning
					var exitErr *exec.ExitError
					if errors.As(err, &exitErr) {
						if exitErr.ExitCode() == 1 && strings.Contains(stderr.String(), "file changed as we read it") {
							fmt.Printf("⚠️  Warning: tar reported non-fatal issue: %s\n", strings.TrimSpace(stderr.String()))
						} else {
							return 0, false, fmt.Errorf("local tar command failed: %w (stderr: %s)", err, stderr.String())
						}
					} else {
						return 0, false, fmt.Errorf("local tar command failed: %w (stderr: %s)", err, stderr.String())
					}
				}

				sizeMB := float64(info.Size) / (1024 * 1024)
				fmt.Printf("✓ Successfully uploaded to Minio: %s (%.2f MB)\n", objectName, sizeMB)
				return info.Size, awsUploaded, nil
			}
		}

		// Standard Minio-only upload (no AWS configured or AWS init failed)
		info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, reader, -1, minio.PutObjectOptions{
			ContentType: "application/gzip",
		})
		if err != nil {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return 0, false, fmt.Errorf("failed to upload to Minio: %w", err)
		}

		if err := cmd.Wait(); err != nil {
			// Treat tar exit code 1 for "file changed as we read it" as a non-fatal warning
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if exitErr.ExitCode() == 1 && strings.Contains(stderr.String(), "file changed as we read it") {
					fmt.Printf("⚠️  Warning: tar reported non-fatal issue: %s\n", strings.TrimSpace(stderr.String()))
				} else {
					return 0, false, fmt.Errorf("local tar command failed: %w (stderr: %s)", err, stderr.String())
				}
			} else {
				return 0, false, fmt.Errorf("local tar command failed: %w (stderr: %s)", err, stderr.String())
			}
		}

		sizeMB := float64(info.Size) / (1024 * 1024)
		fmt.Printf("✓ Successfully uploaded to Minio: %s (%.2f MB)\n", objectName, sizeMB)
		return info.Size, awsUploaded, nil
	}

	// Remote (ssh) path - run the tarCmd under bash -lc on the remote side
	session, err := bm.sshClient.GetSession()
	if err != nil {
		return 0, false, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Get stdout pipe
	stdout, err := session.StdoutPipe()
	if err != nil {
		return 0, false, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	remoteCmd := fmt.Sprintf("bash -lc %q", tarCmd)

	// Prepare to capture remote stderr so we can detect benign tar warnings
	remoteStderrPipe, err := session.StderrPipe()
	if err != nil {
		return 0, false, fmt.Errorf("failed to get stderr pipe from SSH session: %w", err)
	}
	var remoteStderr bytes.Buffer
	go func() {
		_, _ = io.Copy(&remoteStderr, remoteStderrPipe)
	}()

	// Start the tar command
	if err := session.Start(remoteCmd); err != nil {
		return 0, false, fmt.Errorf("failed to start tar command: %w", err)
	}

	// Stream directly to Minio
	ctx := context.Background()
	// Store backups in a directory named after the site (basename of workingDir)
	siteName := filepath.Base(workingDir)

	// Build objectName with same supersede semantics as local branch
	var objectName string
	if containerBucketPath != "" {
		objectName = filepath.Join(containerBucketPath, backupName)
	} else if bm.minioConfig != nil && bm.minioConfig.BucketPath != "" {
		objectName = filepath.Join(bm.minioConfig.BucketPath, backupName)
	} else {
		objectName = fmt.Sprintf("backups/%s/%s", siteName, backupName)
	}

	// If AWS is configured and includeAWSGlacier flag is set, upload to AWS first using TeeReader
	var reader io.Reader = stdout
	if includeAWSGlacier && bm.awsConfig != nil && bm.awsConfig.Vault != "" {
		if err := bm.initAWSClient(); err != nil {
			fmt.Printf("Warning: failed to initialize AWS client, skipping AWS upload: %v\n", err)
		} else {
			// Create a pipe to capture the tar output for AWS
			pr, pw := io.Pipe()

			// Use TeeReader to duplicate the stream
			reader = io.TeeReader(stdout, pw)

			// Upload to AWS in a goroutine
			awsErrChan := make(chan error, 1)
			go func() {
				defer pw.Close()
				awsStartTime := time.Now()
				fmt.Printf("   ☁️  Streaming to AWS Glacier...\n")
				fmt.Printf("      [AWS] Starting upload at %s\n", awsStartTime.Format("15:04:05"))
				err := bm.UploadToAWS(objectName, pr, -1)
				awsEndTime := time.Now()
				awsDuration := awsEndTime.Sub(awsStartTime)
				if err != nil {
					fmt.Printf("      [AWS] Failed after %s: %v\n", awsDuration, err)
					awsErrChan <- fmt.Errorf("AWS upload failed: %w", err)
				} else {
					fmt.Printf("      [AWS] Completed in %s\n", awsDuration)
					awsErrChan <- nil
				}
			}()

			// Continue with Minio upload using the TeeReader
			fmt.Printf("   📦 Streaming to Minio...\n")
			info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, reader, -1, minio.PutObjectOptions{
				ContentType: "application/gzip",
			})
			if err != nil {
				session.Signal("KILL") // Kill the session if upload fails
				return 0, false, fmt.Errorf("failed to upload to Minio: %w", err)
			}

			// Wait for AWS upload to complete
			awsErr := <-awsErrChan
			if awsErr != nil {
				fmt.Printf("⚠️  Warning: %v\n", awsErr)
			} else {
				fmt.Printf("   ✓ AWS Glacier upload complete\n")
				awsUploaded = true
			}

			// Wait for command to complete
			if err := session.Wait(); err != nil {
				// If remote tar printed "file changed as we read it" consider it a warning
				if strings.Contains(remoteStderr.String(), "file changed as we read it") {
					fmt.Printf("⚠️  Warning: remote tar reported non-fatal issue: %s\n", strings.TrimSpace(remoteStderr.String()))
				} else {
					return 0, false, fmt.Errorf("tar command failed: %w (remote stderr: %s)", err, remoteStderr.String())
				}
			}

			sizeMB := float64(info.Size) / (1024 * 1024)
			fmt.Printf("✓ Successfully uploaded to Minio: %s (%.2f MB)\n", objectName, sizeMB)
			return info.Size, awsUploaded, nil
		}
	}

	// Standard Minio-only upload (no AWS configured or AWS init failed)
	info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, reader, -1, minio.PutObjectOptions{
		ContentType: "application/gzip",
	})
	if err != nil {
		session.Signal("KILL") // Kill the session if upload fails
		return 0, false, fmt.Errorf("failed to upload to Minio: %w", err)
	}

	// Wait for command to complete
	if err := session.Wait(); err != nil {
		// If remote tar printed "file changed as we read it" consider it a warning
		if strings.Contains(remoteStderr.String(), "file changed as we read it") {
			fmt.Printf("⚠️  Warning: remote tar reported non-fatal issue: %s\n", strings.TrimSpace(remoteStderr.String()))
		} else {
			return 0, false, fmt.Errorf("tar command failed: %w (remote stderr: %s)", err, remoteStderr.String())
		}
	}

	sizeMB := float64(info.Size) / (1024 * 1024)
	fmt.Printf("✓ Successfully uploaded to Minio: %s (%.2f MB)\n", objectName, sizeMB)
	return info.Size, awsUploaded, nil
}

func (bm *BackupManager) readRemoteFile(filePath string) ([]byte, error) {
	// If running locally, read the file from disk directly
	if bm.sshClient == nil {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read file: %w", err)
		}
		return data, nil
	}

	cmd := fmt.Sprintf("cat %s", filePath)
	output, stderr, err := bm.executeCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w (stderr: %s)", err, stderr)
	}
	return []byte(output), nil
}

// ReadBackup downloads or streams a Minio object. If outputPath is empty it writes to stdout.
func (bm *BackupManager) ReadBackup(objectName, outputPath string) error {
	if err := bm.initMinioClient(); err != nil {
		return err
	}

	ctx := context.Background()

	obj, err := bm.minioClient.GetObject(ctx, bm.minioConfig.Bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to get object '%s': %w", objectName, err)
	}
	defer obj.Close()

	if outputPath == "" {
		// Stream to stdout
		if _, err := io.Copy(os.Stdout, obj); err != nil {
			return fmt.Errorf("failed to stream object to stdout: %w", err)
		}
		return nil
	}

	// Ensure parent directory exists
	if dir := filepath.Dir(outputPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, obj); err != nil {
		return fmt.Errorf("failed to write object to file: %w", err)
	}

	fmt.Printf("Successfully downloaded %s to %s\n", objectName, outputPath)
	return nil
}

// DownloadBackup downloads a backup object from Minio and returns a reader.
// The caller is responsible for closing the returned ReadCloser.
func (bm *BackupManager) DownloadBackup(objectName string) (io.ReadCloser, error) {
	if err := bm.initMinioClient(); err != nil {
		return nil, err
	}

	bm.logDebug("DownloadBackup called for object: %s", objectName)

	ctx := context.Background()
	obj, err := bm.minioClient.GetObject(ctx, bm.minioConfig.Bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		bm.logDebug("Failed to get object from Minio: %v", err)
		return nil, fmt.Errorf("failed to get object '%s': %w", objectName, err)
	}

	bm.logVerbose("Successfully opened stream for object: %s", objectName)
	return obj, nil
}

// ListBackups lists objects in the configured bucket filtered by prefix.
// It returns up to 'limit' objects ordered by whatever the Minio server yields (client-side sorting is not performed).
func (bm *BackupManager) ListBackups(prefix string, limit int) ([]ObjectInfo, error) {
	if err := bm.initMinioClient(); err != nil {
		return nil, err
	}

	ctx := context.Background()
	opts := minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}

	var results []ObjectInfo
	ch := bm.minioClient.ListObjects(ctx, bm.minioConfig.Bucket, opts)
	for obj := range ch {
		if obj.Err != nil {
			return nil, fmt.Errorf("error listing object: %w", obj.Err)
		}
		results = append(results, ObjectInfo{
			Key:          obj.Key,
			Size:         obj.Size,
			LastModified: obj.LastModified,
		})
		if limit > 0 && len(results) >= limit {
			break
		}
	}

	return results, nil
}

// GetLatestObject returns the key of the most recently modified object with the given prefix.
func (bm *BackupManager) GetLatestObject(prefix string) (string, error) {
	objs, err := bm.ListBackups(prefix, 0)
	if err != nil {
		return "", err
	}
	if len(objs) == 0 {
		return "", fmt.Errorf("no objects found for prefix '%s'", prefix)
	}

	// Find the latest by LastModified
	latest := objs[0]
	for _, o := range objs[1:] {
		if o.LastModified.After(latest.LastModified) {
			latest = o
		}
	}

	return latest.Key, nil
}

// DeleteObject removes a single object from the configured Minio bucket.
func (bm *BackupManager) DeleteObject(objectName string) error {
	if err := bm.initMinioClient(); err != nil {
		return err
	}

	ctx := context.Background()
	if err := bm.minioClient.RemoveObject(ctx, bm.minioConfig.Bucket, objectName, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("failed to delete object '%s': %w", objectName, err)
	}
	return nil
}

// DeleteObjects removes multiple objects from the configured Minio bucket.
// It attempts to delete each object and aggregates any errors into a single error.
func (bm *BackupManager) DeleteObjects(objectNames []string) error {
	if err := bm.initMinioClient(); err != nil {
		return err
	}

	// Use Minio batch RemoveObjects API for performance when deleting many objects.
	ctx := context.Background()
	objectsCh := make(chan minio.ObjectInfo, len(objectNames))
	go func() {
		defer close(objectsCh)
		for _, k := range objectNames {
			objectsCh <- minio.ObjectInfo{Key: k}
		}
	}()

	errCh := bm.minioClient.RemoveObjects(ctx, bm.minioConfig.Bucket, objectsCh, minio.RemoveObjectsOptions{})

	var errs []string
	for e := range errCh {
		// RemoveObjects returns RemoveObjectError with ObjectName and Err
		errs = append(errs, fmt.Sprintf("%s: %v", e.ObjectName, e.Err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors deleting objects: %s", strings.Join(errs, "; "))
	}

	return nil
}

// ParseNumericRange parses a numeric range string like "1-10" and returns start and end indices.
// The range is 1-based (1 means the first/most recent backup).
func (bm *BackupManager) ParseNumericRange(rangeStr string) (int, int, error) {
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("range must be in format 'N-M' (e.g., '1-10')")
	}

	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start value: %w", err)
	}

	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid end value: %w", err)
	}

	if start < 1 {
		return 0, 0, fmt.Errorf("start must be >= 1")
	}

	if end < start {
		return 0, 0, fmt.Errorf("end must be >= start")
	}

	return start, end, nil
}

// SelectObjectsByNumericRange selects objects by numeric range (1-based, where 1 is most recent).
// Objects are sorted by LastModified in descending order before selection.
func (bm *BackupManager) SelectObjectsByNumericRange(objs []ObjectInfo, start, end int) ([]ObjectInfo, error) {
	if len(objs) == 0 {
		return nil, fmt.Errorf("no objects available")
	}

	// Sort by LastModified descending (most recent first)
	sorted := make([]ObjectInfo, len(objs))
	copy(sorted, objs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LastModified.After(sorted[j].LastModified)
	})

	// Convert 1-based indices to 0-based
	startIdx := start - 1
	endIdx := end - 1

	if startIdx >= len(sorted) {
		return nil, fmt.Errorf("start index %d exceeds number of objects (%d)", start, len(sorted))
	}

	if endIdx >= len(sorted) {
		endIdx = len(sorted) - 1
	}

	return sorted[startIdx : endIdx+1], nil
}

// ParseDateRange parses a date range string in format YYYYMMDD-YYYYMMDD or YYYYMMDD:HHMMSS-YYYYMMDD:HHMMSS
func (bm *BackupManager) ParseDateRange(rangeStr string) (time.Time, time.Time, error) {
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, fmt.Errorf("range must be in format 'YYYYMMDD-YYYYMMDD' or 'YYYYMMDD:HHMMSS-YYYYMMDD:HHMMSS'")
	}

	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	// Determine format based on presence of colon
	var layout string
	if strings.Contains(startStr, ":") {
		layout = "20060102:150405"
	} else {
		layout = "20060102"
	}

	startTime, err := time.Parse(layout, startStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start date: %w", err)
	}

	endTime, err := time.Parse(layout, endStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end date: %w", err)
	}

	// If using date-only format, set end time to end of day
	if layout == "20060102" {
		endTime = endTime.Add(24*time.Hour - time.Second)
	}

	if endTime.Before(startTime) {
		return time.Time{}, time.Time{}, fmt.Errorf("end date must be after start date")
	}

	return startTime, endTime, nil
}

// FilterObjectsByDateRange filters objects to only include those with LastModified between start and end times (inclusive).
func (bm *BackupManager) FilterObjectsByDateRange(objs []ObjectInfo, start, end time.Time) []ObjectInfo {
	var filtered []ObjectInfo
	for _, o := range objs {
		if (o.LastModified.Equal(start) || o.LastModified.After(start)) &&
			(o.LastModified.Equal(end) || o.LastModified.Before(end)) {
			filtered = append(filtered, o)
		}
	}
	return filtered
}

// SelectObjectsForOverwrite selects objects for deletion when using the overwrite mode.
// It sorts objects by LastModified descending (most recent first) and returns all objects
// except the N most recent ones (where N is the remainder parameter).
// If remainder is 0, all objects are selected for deletion.
// If remainder >= total objects, an empty slice is returned (nothing to delete).
func (bm *BackupManager) SelectObjectsForOverwrite(objs []ObjectInfo, remainder int) []ObjectInfo {
	if len(objs) <= remainder {
		// Keep all objects if we have fewer or equal to the remainder
		return []ObjectInfo{}
	}

	// Sort by LastModified descending (most recent first)
	sorted := make([]ObjectInfo, len(objs))
	copy(sorted, objs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LastModified.After(sorted[j].LastModified)
	})

	// Return all objects after the first N (remainder) items
	return sorted[remainder:]
}

// SelectObjectsWithSmartRetention selects backups to delete using date-aware retention policy
// Preserves weekly and monthly backups based on the policy configuration
func (bm *BackupManager) SelectObjectsWithSmartRetention(objs []ObjectInfo, policy *SmartRetentionPolicy) []ObjectInfo {
	if policy == nil || !policy.Enabled {
		// Fallback to simple retention: keep all objects
		return []ObjectInfo{}
	}

	// Sort by LastModified descending (most recent first)
	sorted := make([]ObjectInfo, len(objs))
	copy(sorted, objs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].LastModified.After(sorted[j].LastModified)
	})

	// Classify backups into categories
	type classifiedBackup struct {
		obj       ObjectInfo
		isDaily   bool
		isWeekly  bool
		isMonthly bool
	}

	var classified []classifiedBackup

	for _, obj := range sorted {
		c := classifiedBackup{obj: obj}

		// Check if this backup qualifies as monthly (day of month matches policy)
		if obj.LastModified.Day() == policy.MonthlyDay {
			c.isMonthly = true
		}

		// Check if this backup qualifies as weekly (day of week matches policy)
		if int(obj.LastModified.Weekday()) == policy.WeeklyDay {
			c.isWeekly = true
		}

		// All backups are daily by default
		c.isDaily = true

		classified = append(classified, c)
	}

	// Select backups to preserve based on policy
	var toKeep []ObjectInfo
	var toDelete []ObjectInfo

	dailyCount := 0
	weeklyCount := 0
	monthlyCount := 0

	for _, c := range classified {
		shouldKeep := false

		// Priority order: Monthly > Weekly > Daily
		// This ensures monthly backups are preserved even if they're also weekly/daily

		// Check monthly quota first
		if c.isMonthly && monthlyCount < policy.KeepMonthly {
			shouldKeep = true
			monthlyCount++
		} else if c.isWeekly && weeklyCount < policy.KeepWeekly && !c.isMonthly {
			// Weekly backup (but not already counted as monthly)
			shouldKeep = true
			weeklyCount++
		} else if dailyCount < policy.KeepDaily {
			// Daily backup
			shouldKeep = true
			dailyCount++
		}

		if shouldKeep {
			toKeep = append(toKeep, c.obj)
		} else {
			toDelete = append(toDelete, c.obj)
		}
	}

	bm.logVerbose("Smart retention: keeping %d backups (daily=%d, weekly=%d, monthly=%d), deleting %d",
		len(toKeep), dailyCount, weeklyCount, monthlyCount, len(toDelete))

	return toDelete
}

// getContainersFromConfig loads containers from a YAML config file
func (bm *BackupManager) getContainersFromConfig(configPath string) ([]ContainerInfo, error) {
	config, err := LoadConfigFromFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config file: %w", err)
	}

	var containers []ContainerInfo
	for _, containerCfg := range config.Containers {
		if containerCfg.Skip {
			fmt.Printf("Skipping container %s (marked as skip in config)\n", containerCfg.Name)
			continue
		}

		// Apply defaults
		config.ApplyDefaults(&containerCfg)

		// Resolve working directory
		workingDir := containerCfg.Paths.WorkingDir
		if workingDir == "" {
			// Try to get from container inspection
			if strings.HasPrefix(containerCfg.Name, "/") {
				workingDir = containerCfg.Name
			} else {
				wd, err := bm.getContainerWorkingDir(containerCfg.Name)
				if err != nil {
					fmt.Printf("Warning: could not determine working dir for %s: %v\n", containerCfg.Name, err)
					continue
				}
				workingDir = wd
			}
		}

		containers = append(containers, ContainerInfo{
			Name:       containerCfg.Name,
			WorkingDir: workingDir,
			Type:       containerCfg.Type,
			Config:     &containerCfg,
		})
	}

	return containers, nil
}

// exportDatabase exports a database based on the container configuration
func (bm *BackupManager) exportDatabase(container ContainerInfo, options *BackupOptions) error {
	if container.Config == nil {
		return fmt.Errorf("no configuration provided for container")
	}

	dbConfig := container.Config.Database
	if dbConfig.Type == "" {
		return fmt.Errorf("no database type specified")
	}

	// Use custom export command if provided
	if dbConfig.ExportCommand != "" {
		fmt.Printf("Running custom database export command...\n")
		_, stderr, err := bm.executeCommand(dbConfig.ExportCommand)
		if err != nil {
			return fmt.Errorf("custom export command failed: %w (stderr: %s)", err, stderr)
		}
		return nil
	}

	// Auto-generate export command based on database type
	var exportCmd string
	var exportPath string

	// Determine export path
	if dbConfig.ExportPath != "" {
		exportPath = dbConfig.ExportPath
	} else if container.Config.Paths.DatabaseExportDir != "" {
		exportPath = filepath.Join(container.Config.Paths.DatabaseExportDir, fmt.Sprintf("%s-export.sql", dbConfig.Name))
	} else {
		exportPath = filepath.Join(container.WorkingDir, fmt.Sprintf("%s-export.sql", dbConfig.Name))
	}

	switch strings.ToLower(dbConfig.Type) {
	case "postgres", "postgresql":
		exportCmd = bm.buildPostgresExportCommand(container, dbConfig, exportPath)
	case "mysql", "mariadb":
		exportCmd = bm.buildMySQLExportCommand(container, dbConfig, exportPath)
	case "mongodb", "mongo":
		exportCmd = bm.buildMongoExportCommand(container, dbConfig, exportPath)
	default:
		return fmt.Errorf("unsupported database type: %s", dbConfig.Type)
	}

	fmt.Printf("Exporting %s database %s...\n", dbConfig.Type, dbConfig.Name)
	if options.DryRun {
		fmt.Printf("[DRY RUN] Would run: %s\n", exportCmd)
		return nil
	}

	// Ensure the export directory exists in the correct place before running export.
	// For Postgres and MongoDB we create the directory inside the target container
	// (dbConfig.Container if set, otherwise the app container). For MySQL/MariaDB
	// the command uses host-side redirection (">"), so create the directory on
	// the host (remote when using SSH, or local when running locally).
	exportDir := filepath.Dir(exportPath)
	if exportDir != "" && exportDir != "." {
		swt := strings.ToLower(dbConfig.Type)
		// For Postgres exports we will redirect pg_dump output to the host path
		// (docker exec ... pg_dump ... > /host/path). Therefore ensure the
		// host-side directory exists. For MongoDB we keep creating the directory
		// inside the container since mongodump produces a directory tree.
		switch swt {
		case "mysql", "mariadb", "postgres", "postgresql":
			// Ensure directory exists on host (local or remote via SSH)
			mkdirCmd := fmt.Sprintf(`mkdir -p %s`, exportDir)
			fmt.Printf("Ensuring export directory exists on host: %s\n", mkdirCmd)
			if _, stderr, err := bm.executeCommand(mkdirCmd); err != nil {
				return fmt.Errorf("failed to create export directory on host: %w (stderr: %s)", err, stderr)
			}

		case "mongodb", "mongo":
			// Directory must exist inside the DB container
			targetContainer := dbConfig.Container
			if targetContainer == "" {
				targetContainer = container.Name
			}
			mkdirCmd := fmt.Sprintf(`docker exec %s mkdir -p %s`, targetContainer, exportDir)
			fmt.Printf("Ensuring export directory exists inside container: %s\n", mkdirCmd)
			if _, stderr, err := bm.executeCommand(mkdirCmd); err != nil {
				return fmt.Errorf("failed to create export directory inside container: %w (stderr: %s)", err, stderr)
			}

		default:
			// Fallback: create on host
			mkdirCmd := fmt.Sprintf(`mkdir -p %s`, exportDir)
			fmt.Printf("Ensuring export directory exists on host (fallback): %s\n", mkdirCmd)
			if _, stderr, err := bm.executeCommand(mkdirCmd); err != nil {
				return fmt.Errorf("failed to create export directory on host: %w (stderr: %s)", err, stderr)
			}
		}
	}

	_, stderr, err := bm.executeCommand(exportCmd)
	if err != nil {
		return fmt.Errorf("database export failed: %w (stderr: %s)", err, stderr)
	}

	fmt.Printf("Database exported to %s\n", exportPath)
	return nil
}

// buildPostgresExportCommand builds a pg_dump command for Postgres databases
func (bm *BackupManager) buildPostgresExportCommand(container ContainerInfo, dbConfig DatabaseConfig, exportPath string) string {
	// Use stdout redirection so the dump is written to the host path
	// (docker exec ... pg_dump ... > /host/path). This mirrors the
	// approach used for MySQL and avoids requiring the target path to
	// exist inside the container.
	var baseCmd string
	if dbConfig.Container != "" {
		baseCmd = fmt.Sprintf(`docker exec %s pg_dump -U %s -d %s`, dbConfig.Container, dbConfig.User, dbConfig.Name)
		if dbConfig.Host != "" {
			baseCmd += fmt.Sprintf(` -h %s`, dbConfig.Host)
		}
		if dbConfig.Port > 0 {
			baseCmd += fmt.Sprintf(` -p %d`, dbConfig.Port)
		}
	} else {
		baseCmd = fmt.Sprintf(`docker exec %s pg_dump -U %s -d %s`, container.Name, dbConfig.User, dbConfig.Name)
	}

	// Redirect stdout to the desired exportPath on the host (or remote host when using SSH)
	cmd := fmt.Sprintf(`%s > %s`, baseCmd, exportPath)
	return cmd
}

// buildMySQLExportCommand builds a mysqldump command for MySQL/MariaDB databases
func (bm *BackupManager) buildMySQLExportCommand(container ContainerInfo, dbConfig DatabaseConfig, exportPath string) string {
	if dbConfig.Container != "" {
		cmd := fmt.Sprintf(`docker exec %s mysqldump -u %s %s > %s`,
			dbConfig.Container, dbConfig.User, dbConfig.Name, exportPath)
		if dbConfig.Password != "" {
			cmd = fmt.Sprintf(`docker exec %s mysqldump -u %s -p%s %s > %s`,
				dbConfig.Container, dbConfig.User, dbConfig.Password, dbConfig.Name, exportPath)
		}
		if dbConfig.Host != "" {
			cmd += fmt.Sprintf(` -h %s`, dbConfig.Host)
		}
		if dbConfig.Port > 0 {
			cmd += fmt.Sprintf(` -P %d`, dbConfig.Port)
		}
		return cmd
	}

	cmd := fmt.Sprintf(`docker exec %s mysqldump -u %s %s > %s`,
		container.Name, dbConfig.User, dbConfig.Name, exportPath)
	if dbConfig.Password != "" {
		cmd = fmt.Sprintf(`docker exec %s mysqldump -u %s -p%s %s > %s`,
			container.Name, dbConfig.User, dbConfig.Password, dbConfig.Name, exportPath)
	}
	return cmd
}

// buildMongoExportCommand builds a mongodump command for MongoDB databases
func (bm *BackupManager) buildMongoExportCommand(container ContainerInfo, dbConfig DatabaseConfig, exportPath string) string {
	if dbConfig.Container != "" {
		cmd := fmt.Sprintf(`docker exec %s mongodump --db %s --out %s`,
			dbConfig.Container, dbConfig.Name, exportPath)
		if dbConfig.User != "" {
			cmd += fmt.Sprintf(` --username %s`, dbConfig.User)
		}
		if dbConfig.Password != "" {
			cmd += fmt.Sprintf(` --password %s`, dbConfig.Password)
		}
		return cmd
	}

	cmd := fmt.Sprintf(`docker exec %s mongodump --db %s --out %s`,
		container.Name, dbConfig.Name, exportPath)
	if dbConfig.User != "" {
		cmd += fmt.Sprintf(` --username %s`, dbConfig.User)
	}
	if dbConfig.Password != "" {
		cmd += fmt.Sprintf(` --password %s`, dbConfig.Password)
	}
	return cmd
}

// SanitizeBackup extracts specific content from a backup tarball and removes sensitive data
func (bm *BackupManager) SanitizeBackup(options *SanitizeOptions) error {
	// Create temporary directory for extraction
	tmpDir, err := os.MkdirTemp("", "backup-sanitize-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	extractedDir := filepath.Join(tmpDir, "extracted")
	sanitizedDir := filepath.Join(tmpDir, "sanitized")

	if options.DryRun {
		fmt.Println("\n[DRY RUN] Would perform the following actions:")
		fmt.Printf("1. Extract from: %s\n", options.InputPath)
		fmt.Printf("2. Create temp directory: %s\n", tmpDir)
		fmt.Printf("3. Extract directories: %v\n", options.ExtractDirs)
		fmt.Printf("4. Extract files matching: %v\n", options.ExtractFiles)
		fmt.Println("5. Remove license keys from SQL files")
		fmt.Printf("6. Create sanitized tarball: %s\n", options.OutputPath)
		return nil
	}

	// Create extraction directories
	if err := os.MkdirAll(extractedDir, 0755); err != nil {
		return fmt.Errorf("failed to create extraction directory: %w", err)
	}
	if err := os.MkdirAll(sanitizedDir, 0755); err != nil {
		return fmt.Errorf("failed to create sanitized directory: %w", err)
	}

	fmt.Println("Step 1: Extracting backup tarball...")
	if err := bm.extractTarball(options.InputPath, extractedDir); err != nil {
		return fmt.Errorf("failed to extract tarball: %w", err)
	}

	fmt.Println("Step 2: Filtering and copying content...")
	if err := bm.filterAndCopyContent(extractedDir, sanitizedDir, options); err != nil {
		return fmt.Errorf("failed to filter content: %w", err)
	}

	fmt.Println("Step 3: Sanitizing SQL files...")
	if err := bm.sanitizeSQLFiles(sanitizedDir); err != nil {
		return fmt.Errorf("failed to sanitize SQL files: %w", err)
	}

	fmt.Println("Step 4: Creating sanitized tarball...")
	if err := bm.createTarball(sanitizedDir, options.OutputPath); err != nil {
		return fmt.Errorf("failed to create sanitized tarball: %w", err)
	}

	return nil
}

// extractTarball extracts a tarball to a destination directory
func (bm *BackupManager) extractTarball(tarballPath, destDir string) error {
	cmd := exec.Command("tar", "-xzf", tarballPath, "-C", destDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar extraction failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// filterAndCopyContent filters and copies content based on extract options
func (bm *BackupManager) filterAndCopyContent(srcDir, destDir string, options *SanitizeOptions) error {
	// Walk through the extracted content
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path from source directory
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if relPath == "." {
			return nil
		}

		// Check if this path matches any of the extract directories
		shouldExtractDir := false
		for _, extractDir := range options.ExtractDirs {
			// Check if this path is within one of the extract directories
			// We use filepath.Split to handle path matching correctly and avoid false positives
			// For example, if extractDir is "wp-content", we want to match:
			//   - "wp-content/themes/..."
			//   - "site/www/wp-content/themes/..."
			// But not:
			//   - "my-wp-content-backup/..."
			if strings.HasPrefix(relPath, extractDir+"/") ||
				strings.HasPrefix(relPath, extractDir) && relPath == extractDir ||
				strings.Contains(relPath, "/"+extractDir+"/") {
				shouldExtractDir = true
				break
			}
		}

		// Check if this is a file matching extract file patterns
		shouldExtractFile := false
		if !info.IsDir() {
			for _, pattern := range options.ExtractFiles {
				matched, err := filepath.Match(pattern, filepath.Base(path))
				if err != nil {
					fmt.Printf("Warning: invalid pattern %s: %v\n", pattern, err)
					continue
				}
				if matched {
					shouldExtractFile = true
					break
				}
			}
		}

		// Only copy if it matches directory or file criteria
		if shouldExtractDir || shouldExtractFile {
			destPath := filepath.Join(destDir, relPath)

			if info.IsDir() {
				return os.MkdirAll(destPath, info.Mode())
			}

			// Copy file
			return bm.copyFile(path, destPath, info.Mode())
		}

		return nil
	})
}

// copyFile copies a file from src to dst with the specified permissions
func (bm *BackupManager) copyFile(src, dst string, mode os.FileMode) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

// sanitizeSQLFiles removes license keys from SQL files
func (bm *BackupManager) sanitizeSQLFiles(dir string) error {
	// Use the default list of license keys to remove
	// This list can be extended or customized as needed
	optionsToRemove := DefaultLicenseKeysToRemove

	// Find all SQL files
	var sqlFiles []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(path), ".sql") {
			sqlFiles = append(sqlFiles, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if len(sqlFiles) == 0 {
		fmt.Println("   No SQL files found to sanitize")
		return nil
	}

	fmt.Printf("   Found %d SQL file(s) to sanitize\n", len(sqlFiles))

	for _, sqlFile := range sqlFiles {
		fmt.Printf("   Sanitizing: %s\n", filepath.Base(sqlFile))
		if err := bm.removeLicenseKeysFromSQL(sqlFile, optionsToRemove); err != nil {
			fmt.Printf("   Warning: failed to sanitize %s: %v\n", sqlFile, err)
			continue
		}
	}

	return nil
}

// removeLicenseKeysFromSQL removes license-related entries from a SQL file
func (bm *BackupManager) removeLicenseKeysFromSQL(sqlFile string, optionsToRemove []string) error {
	// Read the SQL file
	content, err := os.ReadFile(sqlFile)
	if err != nil {
		return err
	}

	sqlContent := string(content)
	modified := false

	// For each option to remove, delete SQL statements that insert or update it
	// NOTE: This is a simplified line-based approach that works for most WordPress database dumps.
	// Potential edge cases this approach might miss:
	// - Multi-line SQL statements (e.g., INSERT with line breaks)
	// - Quoted option names that appear in comments or string values
	// - Complex SQL syntax with subqueries or nested statements
	// - Different quoting styles (backticks, single quotes, double quotes)
	// - Escaped characters within option values
	// - REPLACE, UPSERT, or other non-standard INSERT variations
	//
	// For a production-grade solution, consider using a proper SQL parser library like:
	// - github.com/xwb1989/sqlparser (MySQL)
	// - github.com/akamensky/sql-parser
	// - Or calling mysql/mysqldump with specific filtering options
	for _, option := range optionsToRemove {
		// Simple line-based removal for statements containing the option
		lines := strings.Split(sqlContent, "\n")
		var newLines []string
		for _, line := range lines {
			if !strings.Contains(line, option) {
				newLines = append(newLines, line)
			} else {
				modified = true
			}
		}
		sqlContent = strings.Join(newLines, "\n")
	}

	// Also update the _transient_astra-addon_license_status to 0
	// This transient should be set to 0 to indicate no license
	// NOTE: This is a simplified line-based approach. In production, you might want
	// more sophisticated SQL parsing to handle edge cases like:
	// - Multi-line statements
	// - Quoted option names with similar patterns
	// - Complex SQL syntax with subqueries
	// - Different quoting styles
	// For a more robust solution, consider using a proper SQL parser library.
	lines := strings.Split(sqlContent, "\n")
	var newLines []string
	for _, line := range lines {
		if strings.Contains(line, "_transient_astra-addon_license_status") {
			// Try to replace common value patterns with 0
			// This handles INSERT statements like: INSERT INTO `wp_options` VALUES (...,'_transient_astra-addon_license_status','1','yes');
			modified = true
			// Replace single-quoted values after the option name
			// Pattern: '..._transient_astra-addon_license_status','<any_value>','yes'
			// Replace with: '..._transient_astra-addon_license_status','0','yes'
			newLine := strings.ReplaceAll(line, "'_transient_astra-addon_license_status','1'", "'_transient_astra-addon_license_status','0'")
			newLine = strings.ReplaceAll(newLine, "'_transient_astra-addon_license_status',\"1\"", "'_transient_astra-addon_license_status','0'")
			newLines = append(newLines, newLine)
		} else {
			newLines = append(newLines, line)
		}
	}
	sqlContent = strings.Join(newLines, "\n")

	// Write back if modified
	if modified {
		if err := os.WriteFile(sqlFile, []byte(sqlContent), 0644); err != nil {
			return err
		}
	}

	return nil
}

// createTarball creates a tarball from a source directory
func (bm *BackupManager) createTarball(srcDir, tarballPath string) error {
	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(tarballPath), 0755); err != nil {
		return err
	}

	cmd := exec.Command("tar", "-czf", tarballPath, "-C", srcDir, ".")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar creation failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// EstimateCompressedSize estimates the compressed size of a backup using the specified method
func (bm *BackupManager) EstimateCompressedSize(workingDir, parentDir, method string, sampleSize int64) (compressedSize, uncompressedSize int64, err error) {
	// Get uncompressed size
	uncompressedSize, err = bm.getDirectorySize(workingDir, parentDir)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get directory size: %w", err)
	}

	switch method {
	case "heuristic":
		compressedSize, err = bm.estimateHeuristic(workingDir, parentDir, uncompressedSize)
	case "sample":
		compressedSize, err = bm.estimateSample(workingDir, parentDir, sampleSize, uncompressedSize)
	case "accurate":
		compressedSize, err = bm.estimateAccurate(workingDir, parentDir)
	default:
		return 0, uncompressedSize, fmt.Errorf("unknown estimation method: %s (use 'heuristic', 'sample', or 'accurate')", method)
	}

	if err != nil {
		return 0, uncompressedSize, err
	}

	return compressedSize, uncompressedSize, nil
}

// estimateHeuristic uses file extension heuristics to estimate compression (instant, ~80% accurate)
func (bm *BackupManager) estimateHeuristic(workingDir, parentDir string, uncompressedSize int64) (int64, error) {
	// Build command to list all files with sizes and extensions
	var listCmd string
	if parentDir != "" {
		alt := filepath.Join(parentDir, filepath.Base(workingDir))
		listCmd = fmt.Sprintf(`if [ -d "%s" ]; then find "%s" -type f -printf "%%s %%f\n" 2>/dev/null; elif [ -d "%s" ]; then find "%s" -type f -printf "%%s %%f\n" 2>/dev/null; fi`,
			workingDir, workingDir, alt, alt)
	} else {
		listCmd = fmt.Sprintf(`find "%s" -type f -printf "%%s %%f\n" 2>/dev/null`, workingDir)
	}

	var output string
	var stderr string
	var err error

	if bm.sshClient == nil {
		// Local execution
		cmd := exec.Command("bash", "-c", listCmd)
		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf
		outBytes, execErr := cmd.Output()
		output = string(outBytes)
		stderr = stderrBuf.String()
		err = execErr
	} else {
		// Remote execution
		output, stderr, err = bm.executeCommand(listCmd)
	}

	if err != nil {
		return 0, fmt.Errorf("failed to list files: %w (stderr: %s)", err, stderr)
	}

	var estimatedSize int64
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		size, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}

		filename := parts[1]
		ext := strings.ToLower(filepath.Ext(filename))

		// Apply compression ratio based on file type
		switch ext {
		case ".txt", ".log", ".sql", ".php", ".html", ".css", ".js", ".json", ".xml", ".csv", ".md", ".yml", ".yaml":
			// Text files: assume 70% compression (keep 30%)
			estimatedSize += size * 30 / 100
		case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".mp4", ".mp3", ".avi", ".mov", ".zip", ".gz", ".tgz", ".bz2", ".pdf", ".woff", ".woff2", ".ttf", ".eot":
			// Already compressed: assume 5% compression (keep 95%)
			estimatedSize += size * 95 / 100
		case ".svg", ".otf":
			// SVG and fonts: assume 50% compression (keep 50%)
			estimatedSize += size * 50 / 100
		default:
			// Unknown: assume 50% compression (keep 50%)
			estimatedSize += size * 50 / 100
		}
	}

	// Add tar header overhead (~512 bytes per file)
	fileCount := len(lines)
	tarOverhead := int64(fileCount * 512)
	estimatedSize += tarOverhead

	return estimatedSize, nil
}

// estimateSample compresses a sample and extrapolates (fast, ~90% accurate)
func (bm *BackupManager) estimateSample(workingDir, parentDir string, sampleSize, uncompressedSize int64) (int64, error) {
	// Build tar command that samples data
	var tarCmd string
	if parentDir != "" {
		alt := filepath.Join(parentDir, filepath.Base(workingDir))
		tarCmd = fmt.Sprintf(`if [ -d "%s" ]; then tar -cf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s" | head -c %d | gzip -c; elif [ -d "%s" ]; then tar -cf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s" | head -c %d | gzip -c; fi`,
			workingDir, workingDir, sampleSize, alt, alt, sampleSize)
	} else {
		tarCmd = fmt.Sprintf(`tar -cf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s" | head -c %d | gzip -c`,
			workingDir, sampleSize)
	}

	counter := &countingWriter{}

	if bm.sshClient == nil {
		// Local execution
		cmd := exec.Command("bash", "-c", tarCmd)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = counter

		if err := cmd.Run(); err != nil {
			return 0, fmt.Errorf("sample compression failed: %w (stderr: %s)", err, stderr.String())
		}
	} else {
		// Remote execution
		session, err := bm.sshClient.GetSession()
		if err != nil {
			return 0, fmt.Errorf("failed to create SSH session: %w", err)
		}
		defer session.Close()

		stdout, err := session.StdoutPipe()
		if err != nil {
			return 0, fmt.Errorf("failed to create stdout pipe: %w", err)
		}

		remoteCmd := fmt.Sprintf("bash -lc %q", tarCmd)
		if err := session.Start(remoteCmd); err != nil {
			return 0, fmt.Errorf("failed to start sample command: %w", err)
		}

		_, err = io.Copy(counter, stdout)
		if err != nil && err != io.EOF {
			return 0, fmt.Errorf("failed to read sample data: %w", err)
		}

		if err := session.Wait(); err != nil {
			// Ignore broken pipe errors which are expected when using head
			if !strings.Contains(err.Error(), "exit status 141") {
				return 0, fmt.Errorf("sample command failed: %w", err)
			}
		}
	}

	if counter.written == 0 {
		return 0, fmt.Errorf("no data captured in sample")
	}

	// Calculate compression ratio and extrapolate
	// Note: We sampled uncompressed tar, so we need to account for that
	// The ratio is compressedSize / uncompressedSampleSize
	ratio := float64(counter.written) / float64(sampleSize)

	// For extrapolation, we assume the entire uncompressed directory would compress similarly
	estimatedCompressed := int64(float64(uncompressedSize) * ratio)

	return estimatedCompressed, nil
}

// estimateAccurate performs full compression to a discard writer (100% accurate, same speed as real backup)
func (bm *BackupManager) estimateAccurate(workingDir, parentDir string) (int64, error) {
	// Build tar command identical to the real backup
	var tarCmd string
	if parentDir != "" {
		alt := filepath.Join(parentDir, filepath.Base(workingDir))
		tarCmd = fmt.Sprintf(`if [ -d "%s" ]; then tar -czf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s"; elif [ -d "%s" ]; then tar -czf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s"; fi`,
			workingDir, workingDir, alt, alt)
	} else {
		tarCmd = fmt.Sprintf(`tar -czf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s"`, workingDir)
	}

	counter := &countingWriter{}

	if bm.sshClient == nil {
		// Local execution
		cmd := exec.Command("bash", "-lc", tarCmd)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = counter

		if err := cmd.Run(); err != nil {
			return 0, fmt.Errorf("accurate estimation failed: %w (stderr: %s)", err, stderr.String())
		}
	} else {
		// Remote execution
		session, err := bm.sshClient.GetSession()
		if err != nil {
			return 0, fmt.Errorf("failed to create SSH session: %w", err)
		}
		defer session.Close()

		stdout, err := session.StdoutPipe()
		if err != nil {
			return 0, fmt.Errorf("failed to create stdout pipe: %w", err)
		}

		remoteCmd := fmt.Sprintf("bash -lc %q", tarCmd)
		if err := session.Start(remoteCmd); err != nil {
			return 0, fmt.Errorf("failed to start compression: %w", err)
		}

		_, err = io.Copy(counter, stdout)
		if err != nil && err != io.EOF {
			return 0, fmt.Errorf("failed to count compressed data: %w", err)
		}

		if err := session.Wait(); err != nil {
			return 0, fmt.Errorf("compression command failed: %w", err)
		}
	}

	return counter.written, nil
}

// EstimateCapacityFromScan scans containers and estimates storage capacity requirements
func (bm *BackupManager) EstimateCapacityFromScan(containers []ContainerInfo, estimateMethod string, sampleSize int64, options *CapacityEstimateOptions) (*CapacityEstimate, error) {
	if len(containers) == 0 {
		return nil, fmt.Errorf("no containers provided")
	}

	result := &CapacityEstimate{
		EstimationMethod:    estimateMethod,
		SitesScanned:        len(containers),
		DailyRetention:      options.DailyRetention,
		WeeklyRetention:     options.WeeklyRetention,
		MonthlyRetention:    options.MonthlyRetention,
		TotalBackupsPerSite: options.DailyRetention + options.WeeklyRetention + options.MonthlyRetention,
		BufferPercent:       options.BufferPercent,
		Sites:               make([]SiteEstimate, 0, len(containers)),
	}

	var totalCompressed int64
	var totalUncompressed int64

	// Show method warning if using accurate method over SSH
	if estimateMethod == "accurate" && bm.sshClient != nil {
		fmt.Printf("⚠️  Using 'accurate' method over SSH - this will take several minutes per site.\n")
		fmt.Printf("   Consider using 'heuristic' (~instant) or 'sample' (~5-10 sec/site) for faster estimates.\n\n")
	}

	fmt.Printf("Scanning %d container(s)...\n", len(containers))
	startTime := time.Now()

	for i, container := range containers {
		containerStart := time.Now()
		fmt.Printf("  [%d/%d] Analyzing %s...\n", i+1, len(containers), container.Name)

		// Estimate compressed size for this container
		compressedSize, uncompressedSize, err := bm.EstimateCompressedSize(
			container.WorkingDir,
			"", // parentDir not needed for estimation
			estimateMethod,
			sampleSize,
		)

		containerDuration := time.Since(containerStart)

		if err != nil {
			fmt.Printf("    ⚠️  Warning: Could not estimate %s: %v\n", container.Name, err)
			continue
		}

		compressionRatio := 0.0
		if uncompressedSize > 0 {
			compressionRatio = (1.0 - float64(compressedSize)/float64(uncompressedSize)) * 100
		}

		siteEst := SiteEstimate{
			SiteName:         filepath.Base(container.WorkingDir),
			UncompressedSize: uncompressedSize,
			CompressedSize:   compressedSize,
			CompressionRatio: compressionRatio,
		}

		// Calculate storage requirements
		siteEst.HotStorageSize = compressedSize * int64(options.DailyRetention)
		siteEst.ColdStorageSize = compressedSize * int64(options.WeeklyRetention+options.MonthlyRetention)
		siteEst.TotalStorageSize = siteEst.HotStorageSize + siteEst.ColdStorageSize

		result.Sites = append(result.Sites, siteEst)
		totalCompressed += compressedSize
		totalUncompressed += uncompressedSize

		// Show timing and ETA
		avgTimePerSite := time.Since(startTime) / time.Duration(i+1)
		remaining := len(containers) - (i + 1)
		eta := avgTimePerSite * time.Duration(remaining)

		fmt.Printf("    Compressed: %.2f MB, Uncompressed: %.2f MB (%.1f%% saved) [took %s]\n",
			float64(compressedSize)/(1024*1024),
			float64(uncompressedSize)/(1024*1024),
			compressionRatio,
			containerDuration.Round(time.Second))

		if remaining > 0 {
			fmt.Printf("    ⏱️  Avg: %s/site, ETA: %s for %d remaining\n",
				avgTimePerSite.Round(time.Second),
				eta.Round(time.Second),
				remaining)
		}
	}

	if len(result.Sites) == 0 {
		return nil, fmt.Errorf("no sites successfully analyzed")
	}

	totalDuration := time.Since(startTime)
	fmt.Printf("\n✅ Scan complete in %s (avg %s/site)\n\n",
		totalDuration.Round(time.Second),
		(totalDuration / time.Duration(len(result.Sites))).Round(time.Second))

	// Calculate averages
	result.AvgCompressedSize = totalCompressed / int64(len(result.Sites))
	result.AvgUncompressedSize = totalUncompressed / int64(len(result.Sites))
	if result.AvgUncompressedSize > 0 {
		result.AvgCompressionRatio = (1.0 - float64(result.AvgCompressedSize)/float64(result.AvgUncompressedSize)) * 100
	}

	// Calculate per-site storage
	result.PerSiteHotStorage = result.AvgCompressedSize * int64(options.DailyRetention)
	result.PerSiteColdStorage = result.AvgCompressedSize * int64(options.WeeklyRetention+options.MonthlyRetention)
	result.PerSiteTotalStorage = result.PerSiteHotStorage + result.PerSiteColdStorage

	// Calculate fleet-wide storage
	result.FleetHotStorage = result.PerSiteHotStorage * int64(result.SitesScanned)
	result.FleetColdStorage = result.PerSiteColdStorage * int64(result.SitesScanned)
	result.FleetTotalStorage = result.FleetHotStorage + result.FleetColdStorage

	// Add buffer
	result.FleetTotalWithBuffer = int64(float64(result.FleetTotalStorage) * (1.0 + options.BufferPercent/100.0))

	// Calculate growth projections if growth rate specified
	if options.GrowthRate > 0 && options.ProjectionMonths > 0 {
		result.GrowthProjections = calculateGrowthProjections(
			result.FleetHotStorage,
			result.FleetColdStorage,
			options.GrowthRate,
			options.ProjectionMonths,
			options.GlacierPricePerGB,
		)
	}

	// Calculate costs if price specified
	if options.GlacierPricePerGB > 0 {
		coldStorageGB := float64(result.FleetColdStorage) / (1024 * 1024 * 1024)
		result.MonthlyCost = coldStorageGB * options.GlacierPricePerGB

		if options.RetrievalPricePerGB > 0 {
			// Assume 10% monthly retrieval rate
			result.RetrievalCost10Pct = coldStorageGB * 0.10 * options.RetrievalPricePerGB
		}
	}

	return result, nil
}

// EstimateCapacityFromManual creates capacity estimate from manual input
func (bm *BackupManager) EstimateCapacityFromManual(avgCompressedSize int64, siteCount int, options *CapacityEstimateOptions) (*CapacityEstimate, error) {
	result := &CapacityEstimate{
		EstimationMethod:    "manual",
		SitesScanned:        siteCount,
		DailyRetention:      options.DailyRetention,
		WeeklyRetention:     options.WeeklyRetention,
		MonthlyRetention:    options.MonthlyRetention,
		TotalBackupsPerSite: options.DailyRetention + options.WeeklyRetention + options.MonthlyRetention,
		AvgCompressedSize:   avgCompressedSize,
		BufferPercent:       options.BufferPercent,
	}

	// Calculate per-site storage
	result.PerSiteHotStorage = avgCompressedSize * int64(options.DailyRetention)
	result.PerSiteColdStorage = avgCompressedSize * int64(options.WeeklyRetention+options.MonthlyRetention)
	result.PerSiteTotalStorage = result.PerSiteHotStorage + result.PerSiteColdStorage

	// Calculate fleet-wide storage
	result.FleetHotStorage = result.PerSiteHotStorage * int64(siteCount)
	result.FleetColdStorage = result.PerSiteColdStorage * int64(siteCount)
	result.FleetTotalStorage = result.FleetHotStorage + result.FleetColdStorage

	// Add buffer
	result.FleetTotalWithBuffer = int64(float64(result.FleetTotalStorage) * (1.0 + options.BufferPercent/100.0))

	// Calculate growth projections if growth rate specified
	if options.GrowthRate > 0 && options.ProjectionMonths > 0 {
		result.GrowthProjections = calculateGrowthProjections(
			result.FleetHotStorage,
			result.FleetColdStorage,
			options.GrowthRate,
			options.ProjectionMonths,
			options.GlacierPricePerGB,
		)
	}

	// Calculate costs if price specified
	if options.GlacierPricePerGB > 0 {
		coldStorageGB := float64(result.FleetColdStorage) / (1024 * 1024 * 1024)
		result.MonthlyCost = coldStorageGB * options.GlacierPricePerGB

		if options.RetrievalPricePerGB > 0 {
			result.RetrievalCost10Pct = coldStorageGB * 0.10 * options.RetrievalPricePerGB
		}
	}

	return result, nil
}

// EstimateCapacityFromBackup analyzes an existing backup to estimate capacity
func (bm *BackupManager) EstimateCapacityFromBackup(backupKey string, siteCount int, options *CapacityEstimateOptions) (*CapacityEstimate, error) {
	if err := bm.initMinioClient(); err != nil {
		return nil, fmt.Errorf("failed to initialize Minio client: %w", err)
	}

	// Get backup size
	objs, err := bm.ListBackups(backupKey, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to get backup info: %w", err)
	}

	if len(objs) == 0 {
		return nil, fmt.Errorf("backup not found: %s", backupKey)
	}

	backup := objs[0]
	if backup.Key != backupKey {
		return nil, fmt.Errorf("backup not found: %s (found: %s)", backupKey, backup.Key)
	}

	result := &CapacityEstimate{
		EstimationMethod:    "from-backup",
		SitesScanned:        siteCount,
		DailyRetention:      options.DailyRetention,
		WeeklyRetention:     options.WeeklyRetention,
		MonthlyRetention:    options.MonthlyRetention,
		TotalBackupsPerSite: options.DailyRetention + options.WeeklyRetention + options.MonthlyRetention,
		AvgCompressedSize:   backup.Size,
		BufferPercent:       options.BufferPercent,
	}

	// Calculate per-site storage
	result.PerSiteHotStorage = backup.Size * int64(options.DailyRetention)
	result.PerSiteColdStorage = backup.Size * int64(options.WeeklyRetention+options.MonthlyRetention)
	result.PerSiteTotalStorage = result.PerSiteHotStorage + result.PerSiteColdStorage

	// Calculate fleet-wide storage
	result.FleetHotStorage = result.PerSiteHotStorage * int64(siteCount)
	result.FleetColdStorage = result.PerSiteColdStorage * int64(siteCount)
	result.FleetTotalStorage = result.FleetHotStorage + result.FleetColdStorage

	// Add buffer
	result.FleetTotalWithBuffer = int64(float64(result.FleetTotalStorage) * (1.0 + options.BufferPercent/100.0))

	// Calculate growth projections if growth rate specified
	if options.GrowthRate > 0 && options.ProjectionMonths > 0 {
		result.GrowthProjections = calculateGrowthProjections(
			result.FleetHotStorage,
			result.FleetColdStorage,
			options.GrowthRate,
			options.ProjectionMonths,
			options.GlacierPricePerGB,
		)
	}

	// Calculate costs if price specified
	if options.GlacierPricePerGB > 0 {
		coldStorageGB := float64(result.FleetColdStorage) / (1024 * 1024 * 1024)
		result.MonthlyCost = coldStorageGB * options.GlacierPricePerGB

		if options.RetrievalPricePerGB > 0 {
			result.RetrievalCost10Pct = coldStorageGB * 0.10 * options.RetrievalPricePerGB
		}
	}

	return result, nil
}

// calculateGrowthProjections computes storage growth over time
func calculateGrowthProjections(hotStorage, coldStorage int64, growthRate float64, months int, glacierPrice float64) []GrowthProjection {
	projections := make([]GrowthProjection, 0, months)

	currentTotal := float64(hotStorage + coldStorage)
	growthMultiplier := 1.0 + (growthRate / 100.0)

	for month := 1; month <= months; month++ {
		currentTotal *= growthMultiplier
		totalGB := currentTotal / (1024 * 1024 * 1024)

		// Assume same hot/cold ratio
		ratio := float64(coldStorage) / float64(hotStorage+coldStorage)
		coldGB := totalGB * ratio
		hotGB := totalGB * (1.0 - ratio)

		projection := GrowthProjection{
			Month:          month,
			TotalStorageGB: totalGB,
			HotStorageGB:   hotGB,
			ColdStorageGB:  coldGB,
		}

		if glacierPrice > 0 {
			projection.MonthlyCost = coldGB * glacierPrice
		}

		projections = append(projections, projection)
	}

	return projections
}
