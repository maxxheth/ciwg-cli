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
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"ciwg-cli/internal/auth"
)

type MinioConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
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
}

type ContainerInfo struct {
	Name       string
	WorkingDir string
}

type BackupManager struct {
	sshClient   *auth.SSHClient
	minioClient *minio.Client
	minioConfig *MinioConfig
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

func (bm *BackupManager) getContainers(options *BackupOptions) ([]ContainerInfo, error) {
	var containerInputs []string

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
	fmt.Printf("Processing container: %s\n", container.Name)
	fmt.Printf("Working directory: %s\n", container.WorkingDir)

	timestamp := time.Now().Format("20060102-150405")
	backupName := fmt.Sprintf("%s-%s.tgz", filepath.Base(container.WorkingDir), timestamp)

	if options.DryRun {
		fmt.Printf("[DRY RUN] Would clean old SQL files in %s\n", container.Name)
		fmt.Printf("[DRY RUN] Would export DB in %s\n", container.Name)
		fmt.Printf("[DRY RUN] Would create and stream tarball %s to Minio\n", backupName)
		if options.Delete {
			fmt.Printf("[DRY RUN] Would stop and remove container %s\n", container.Name)
			fmt.Printf("[DRY RUN] Would remove directory %s\n", container.WorkingDir)
		}
		fmt.Printf("Done with %s\n\n", container.Name)
		return nil
	}

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

	// Create and stream tarball to Minio
	fmt.Printf("Creating and streaming tarball to Minio...\n")
	if err := bm.streamBackupToMinio(container.WorkingDir, backupName, options.ParentDir); err != nil {
		return fmt.Errorf("failed to stream backup to Minio: %w", err)
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

func (bm *BackupManager) streamBackupToMinio(workingDir, backupName, parentDir string) error {
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
		objectName := fmt.Sprintf("backups/%s", backupName)

		info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, stdout, -1, minio.PutObjectOptions{
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

		fmt.Printf("Successfully uploaded %s (%d bytes) to Minio\n", backupName, info.Size)
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
	objectName := fmt.Sprintf("backups/%s", backupName)

	info, err := bm.minioClient.PutObject(ctx, bm.minioConfig.Bucket, objectName, stdout, -1, minio.PutObjectOptions{
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

	fmt.Printf("Successfully uploaded %s (%d bytes) to Minio\n", backupName, info.Size)
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

	var errs []string
	for _, o := range objectNames {
		if err := bm.DeleteObject(o); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", o, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors deleting objects: %s", strings.Join(errs, "; "))
	}
	return nil
}
