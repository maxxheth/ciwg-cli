package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"ciwg-cli/internal/auth"
)

type MinioConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
	// BucketPath is an optional path prefix within the bucket (e.g., "production/backups")
	BucketPath string
}

type AWSConfig struct {
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
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
}

type ContainerInfo struct {
	Name       string
	WorkingDir string
	// Type indicates the container type: wordpress, custom, postgres, mysql, etc.
	Type string
	// Config holds custom configuration from YAML file
	Config *ContainerConfig
}

type BackupManager struct {
	sshClient   *auth.SSHClient
	minioClient *minio.Client
	minioConfig *MinioConfig
	awsClient   *s3.Client
	awsConfig   *AWSConfig
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
	}
}

// NewBackupManagerWithAWS creates a BackupManager with both Minio and AWS configurations
func NewBackupManagerWithAWS(sshClient *auth.SSHClient, minioConfig *MinioConfig, awsConfig *AWSConfig) *BackupManager {
	return &BackupManager{
		sshClient:   sshClient,
		minioConfig: minioConfig,
		awsConfig:   awsConfig,
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

	client, err := minio.New(bm.minioConfig.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(bm.minioConfig.AccessKey, bm.minioConfig.SecretKey, ""),
		Secure: bm.minioConfig.UseSSL,
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

// initAWSClient initializes the AWS S3 client if not already initialized
func (bm *BackupManager) initAWSClient() error {
	if bm.awsClient != nil {
		return nil
	}

	if bm.awsConfig == nil {
		return fmt.Errorf("AWS configuration is not set")
	}

	// Create AWS config with static credentials
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(bm.awsConfig.Region),
		awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider(
			bm.awsConfig.AccessKey,
			bm.awsConfig.SecretKey,
			"",
		)),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	bm.awsClient = s3.NewFromConfig(cfg)

	// Verify bucket exists
	ctx := context.Background()
	_, err = bm.awsClient.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bm.awsConfig.Bucket),
	})
	if err != nil {
		return fmt.Errorf("bucket %s does not exist or is not accessible: %w", bm.awsConfig.Bucket, err)
	}

	return nil
}

// TestAWSConnection tests the AWS S3 connection with read/write operations
func (bm *BackupManager) TestAWSConnection() error {
	if err := bm.initAWSClient(); err != nil {
		return err
	}

	ctx := context.Background()

	// Step 1: Test bucket existence
	fmt.Printf("1. Testing AWS bucket existence...\n")
	_, err := bm.awsClient.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bm.awsConfig.Bucket),
	})
	if err != nil {
		return fmt.Errorf("failed to access bucket '%s': %w", bm.awsConfig.Bucket, err)
	}
	fmt.Printf("   ✓ AWS Bucket '%s' exists and is accessible\n\n", bm.awsConfig.Bucket)

	// Step 2: Test write operation
	fmt.Printf("2. Testing write operation...\n")
	testObjectName := fmt.Sprintf(".connection-test-%d.txt", time.Now().Unix())
	testContent := []byte("This is an AWS S3 connection test file created by ciwg-cli")

	_, err = bm.awsClient.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bm.awsConfig.Bucket),
		Key:         aws.String(testObjectName),
		Body:        bytes.NewReader(testContent),
		ContentType: aws.String("text/plain"),
	})
	if err != nil {
		return fmt.Errorf("failed to write test object: %w", err)
	}
	fmt.Printf("   ✓ Successfully wrote test object '%s' (%d bytes)\n\n", testObjectName, len(testContent))

	// Step 3: Test read operation
	fmt.Printf("3. Testing read operation...\n")
	result, err := bm.awsClient.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bm.awsConfig.Bucket),
		Key:    aws.String(testObjectName),
	})
	if err != nil {
		return fmt.Errorf("failed to read test object: %w", err)
	}
	defer result.Body.Close()

	readContent, err := io.ReadAll(result.Body)
	if err != nil {
		return fmt.Errorf("failed to read test object content: %w", err)
	}
	if string(readContent) != string(testContent) {
		return fmt.Errorf("content mismatch: read content doesn't match written content")
	}
	fmt.Printf("   ✓ Successfully read test object and verified content\n\n")

	// Step 4: Test delete operation
	fmt.Printf("4. Testing delete operation...\n")
	_, err = bm.awsClient.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bm.awsConfig.Bucket),
		Key:    aws.String(testObjectName),
	})
	if err != nil {
		return fmt.Errorf("failed to delete test object: %w", err)
	}
	fmt.Printf("   ✓ Successfully deleted test object\n")

	return nil
}

// UploadToAWS uploads data from a reader to AWS S3
func (bm *BackupManager) UploadToAWS(objectName string, reader io.Reader, size int64) error {
	if err := bm.initAWSClient(); err != nil {
		return err
	}

	ctx := context.Background()
	_, err := bm.awsClient.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bm.awsConfig.Bucket),
		Key:           aws.String(objectName),
		Body:          reader,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String("application/x-tar"),
	})
	if err != nil {
		return fmt.Errorf("failed to upload to AWS S3: %w", err)
	}

	return nil
}

// ListAWSBackups lists objects in the AWS S3 bucket with optional prefix and limit
func (bm *BackupManager) ListAWSBackups(prefix string, limit int) ([]ObjectInfo, error) {
	if err := bm.initAWSClient(); err != nil {
		return nil, err
	}

	ctx := context.Background()
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(bm.awsConfig.Bucket),
	}

	if prefix != "" {
		input.Prefix = aws.String(prefix)
	}

	if limit > 0 {
		input.MaxKeys = aws.Int32(int32(limit))
	}

	result, err := bm.awsClient.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to list AWS objects: %w", err)
	}

	var objects []ObjectInfo
	for _, obj := range result.Contents {
		objects = append(objects, ObjectInfo{
			Key:          *obj.Key,
			Size:         *obj.Size,
			LastModified: *obj.LastModified,
		})
	}

	// Sort by LastModified descending (most recent first)
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].LastModified.After(objects[j].LastModified)
	})

	return objects, nil
}

// DeleteAWSObjects deletes multiple objects from AWS S3
func (bm *BackupManager) DeleteAWSObjects(keys []string) error {
	if err := bm.initAWSClient(); err != nil {
		return err
	}

	if len(keys) == 0 {
		return nil
	}

	ctx := context.Background()

	// AWS S3 allows batch delete up to 1000 objects at a time
	for i := 0; i < len(keys); i += 1000 {
		end := i + 1000
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]

		var objectIds []types.ObjectIdentifier
		for _, key := range batch {
			objectIds = append(objectIds, types.ObjectIdentifier{
				Key: aws.String(key),
			})
		}

		_, err := bm.awsClient.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(bm.awsConfig.Bucket),
			Delete: &types.Delete{
				Objects: objectIds,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return fmt.Errorf("failed to delete AWS objects: %w", err)
		}
	}

	return nil
}

func (bm *BackupManager) CreateBackups(options *BackupOptions) error {
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

	for _, container := range containers {
		if err := bm.processContainer(container, options); err != nil {
			fmt.Printf("Error processing container %s: %v\n", container.Name, err)
			continue
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

func (bm *BackupManager) processContainer(container ContainerInfo, options *BackupOptions) error {
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
		if options.Delete {
			fmt.Printf("[DRY RUN] Would stop and remove container %s\n", container.Name)
			fmt.Printf("[DRY RUN] Would remove directory %s\n", container.WorkingDir)
		}
		fmt.Printf("Done with %s\n\n", container.Name)
		return nil
	}

	// Run pre-backup commands if specified
	if container.Config != nil && len(container.Config.PreBackupCommands) > 0 {
		fmt.Printf("Running pre-backup commands...\n")
		for _, cmd := range container.Config.PreBackupCommands {
			fmt.Printf("  Running: %s\n", cmd)
			if _, stderr, err := bm.executeCommand(cmd); err != nil {
				return fmt.Errorf("pre-backup command failed: %w (stderr: %s)", err, stderr)
			}
		}
	}

	// Handle database export based on container type
	if container.Type == "wordpress" || container.Type == "" {
		// WordPress-specific backup logic
		if err := bm.exportWordPressDatabase(container); err != nil {
			return err
		}
	} else if container.Config != nil && container.Config.Database.Type != "" {
		// Custom database export
		if err := bm.exportDatabase(container, options); err != nil {
			return err
		}
	}

	// Create and stream tarball to Minio
	fmt.Printf("Creating and streaming tarball to Minio...\n")

	// Determine backup directory - use custom app dir if specified
	backupDir := container.WorkingDir
	if container.Config != nil && container.Config.Paths.AppDir != "" {
		backupDir = container.Config.Paths.AppDir
	}

	var containerBucketPath string
	if container.Config != nil {
		containerBucketPath = container.Config.BucketPath
	}

	if err := bm.streamBackupToMinio(backupDir, backupName, options.ParentDir, containerBucketPath); err != nil {
		return fmt.Errorf("failed to stream backup to Minio: %w", err)
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
	return nil
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

func (bm *BackupManager) streamBackupToMinio(workingDir, backupName, parentDir, containerBucketPath string) error {
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

	// If running locally (no ssh client) run tar locally and stream stdout to Minio
	if bm.sshClient == nil {
		cmd := exec.Command("bash", "-lc", tarCmd)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("failed to create stdout pipe for local tar: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start local tar command: %w", err)
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

		// If AWS is configured, upload to AWS first using TeeReader to capture data
		var reader io.Reader = stdout
		if bm.awsConfig != nil && bm.awsConfig.Bucket != "" {
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
					err := bm.UploadToAWS(objectName, pr, -1)
					if err != nil {
						awsErrChan <- fmt.Errorf("AWS upload failed: %w", err)
					} else {
						awsErrChan <- nil
					}
				}()

				// Continue with Minio upload using the TeeReader
				info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, reader, -1, minio.PutObjectOptions{
					ContentType: "application/gzip",
				})
				if err != nil {
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
					return fmt.Errorf("failed to upload to Minio: %w", err)
				}

				// Wait for AWS upload to complete
				if awsErr := <-awsErrChan; awsErr != nil {
					fmt.Printf("Warning: %v\n", awsErr)
				} else {
					fmt.Printf("Successfully uploaded %s to AWS S3\n", objectName)
				}

				if err := cmd.Wait(); err != nil {
					// Treat tar exit code 1 for "file changed as we read it" as a non-fatal warning
					var exitErr *exec.ExitError
					if errors.As(err, &exitErr) {
						if exitErr.ExitCode() == 1 && strings.Contains(stderr.String(), "file changed as we read it") {
							fmt.Printf("Warning: tar reported non-fatal issue: %s\n", strings.TrimSpace(stderr.String()))
						} else {
							return fmt.Errorf("local tar command failed: %w (stderr: %s)", err, stderr.String())
						}
					} else {
						return fmt.Errorf("local tar command failed: %w (stderr: %s)", err, stderr.String())
					}
				}

				fmt.Printf("Successfully uploaded %s (%d bytes) to Minio\n", objectName, info.Size)
				return nil
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
			return fmt.Errorf("failed to upload to Minio: %w", err)
		}

		if err := cmd.Wait(); err != nil {
			// Treat tar exit code 1 for "file changed as we read it" as a non-fatal warning
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if exitErr.ExitCode() == 1 && strings.Contains(stderr.String(), "file changed as we read it") {
					fmt.Printf("Warning: tar reported non-fatal issue: %s\n", strings.TrimSpace(stderr.String()))
				} else {
					return fmt.Errorf("local tar command failed: %w (stderr: %s)", err, stderr.String())
				}
			} else {
				return fmt.Errorf("local tar command failed: %w (stderr: %s)", err, stderr.String())
			}
		}

		fmt.Printf("Successfully uploaded %s (%d bytes) to Minio\n", objectName, info.Size)
		return nil
	}

	// Remote (ssh) path - run the tarCmd under bash -lc on the remote side
	session, err := bm.sshClient.GetSession()
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Get stdout pipe
	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	remoteCmd := fmt.Sprintf("bash -lc %q", tarCmd)

	// Prepare to capture remote stderr so we can detect benign tar warnings
	remoteStderrPipe, err := session.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe from SSH session: %w", err)
	}
	var remoteStderr bytes.Buffer
	go func() {
		_, _ = io.Copy(&remoteStderr, remoteStderrPipe)
	}()

	// Start the tar command
	if err := session.Start(remoteCmd); err != nil {
		return fmt.Errorf("failed to start tar command: %w", err)
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

	// If AWS is configured, upload to AWS first using TeeReader
	var reader io.Reader = stdout
	if bm.awsConfig != nil && bm.awsConfig.Bucket != "" {
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
				err := bm.UploadToAWS(objectName, pr, -1)
				if err != nil {
					awsErrChan <- fmt.Errorf("AWS upload failed: %w", err)
				} else {
					awsErrChan <- nil
				}
			}()

			// Continue with Minio upload using the TeeReader
			info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, reader, -1, minio.PutObjectOptions{
				ContentType: "application/gzip",
			})
			if err != nil {
				session.Signal("KILL") // Kill the session if upload fails
				return fmt.Errorf("failed to upload to Minio: %w", err)
			}

			// Wait for AWS upload to complete
			if awsErr := <-awsErrChan; awsErr != nil {
				fmt.Printf("Warning: %v\n", awsErr)
			} else {
				fmt.Printf("Successfully uploaded %s to AWS S3\n", objectName)
			}

			// Wait for command to complete
			if err := session.Wait(); err != nil {
				// If remote tar printed "file changed as we read it" consider it a warning
				if strings.Contains(remoteStderr.String(), "file changed as we read it") {
					fmt.Printf("Warning: remote tar reported non-fatal issue: %s\n", strings.TrimSpace(remoteStderr.String()))
				} else {
					return fmt.Errorf("tar command failed: %w (remote stderr: %s)", err, remoteStderr.String())
				}
			}

			fmt.Printf("Successfully uploaded %s (%d bytes) to Minio\n", objectName, info.Size)
			return nil
		}
	}

	// Standard Minio-only upload (no AWS configured or AWS init failed)
	info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, reader, -1, minio.PutObjectOptions{
		ContentType: "application/gzip",
	})
	if err != nil {
		session.Signal("KILL") // Kill the session if upload fails
		return fmt.Errorf("failed to upload to Minio: %w", err)
	}

	// Wait for command to complete
	if err := session.Wait(); err != nil {
		// If remote tar printed "file changed as we read it" consider it a warning
		if strings.Contains(remoteStderr.String(), "file changed as we read it") {
			fmt.Printf("Warning: remote tar reported non-fatal issue: %s\n", strings.TrimSpace(remoteStderr.String()))
		} else {
			return fmt.Errorf("tar command failed: %w (remote stderr: %s)", err, remoteStderr.String())
		}
	}

	fmt.Printf("Successfully uploaded %s (%d bytes) to Minio\n", objectName, info.Size)
	return nil
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
