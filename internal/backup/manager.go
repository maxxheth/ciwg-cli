package backup

import (
	"context"
	"fmt"
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

func NewBackupManager(sshClient *auth.SSHClient, minioConfig *MinioConfig) *BackupManager {
	return &BackupManager{
		sshClient:   sshClient,
		minioConfig: minioConfig,
	}
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
	output, stderr, err := bm.sshClient.ExecuteCommand(cmd)
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
	output, stderr, err := bm.sshClient.ExecuteCommand(cmd)
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
	output, stderr, err := bm.sshClient.ExecuteCommand(cmd)
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

	// Clean old SQL files
	fmt.Printf("Cleaning old SQL files in %s...\n", container.Name)
	cleanCmd := fmt.Sprintf(`docker exec -u 0 "%s" find /var/www/html -name "*.sql" -type f -mmin +120 -exec rm -f {} \;`, container.Name)
	if _, stderr, err := bm.sshClient.ExecuteCommand(cleanCmd); err != nil {
		fmt.Printf("Warning: failed to clean old SQL files: %v (stderr: %s)\n", err, stderr)
	}

	// Export database
	fmt.Printf("Exporting DB in %s...\n", container.Name)
	exportCmd := fmt.Sprintf(`docker exec -u 0 "%s" wp --allow-root db export`, container.Name)
	if _, stderr, err := bm.sshClient.ExecuteCommand(exportCmd); err != nil {
		return fmt.Errorf("failed to export database: %w (stderr: %s)", err, stderr)
	}

	// Create and stream tarball to Minio
	fmt.Printf("Creating and streaming tarball to Minio...\n")
	if err := bm.streamBackupToMinio(container.WorkingDir, backupName); err != nil {
		return fmt.Errorf("failed to stream backup to Minio: %w", err)
	}

	if options.Delete {
		fmt.Printf("Stopping and removing container %s...\n", container.Name)
		stopCmd := fmt.Sprintf(`docker stop "%s" 2>/dev/null || true`, container.Name)
		bm.sshClient.ExecuteCommand(stopCmd)

		removeCmd := fmt.Sprintf(`docker rm "%s" 2>/dev/null || true`, container.Name)
		bm.sshClient.ExecuteCommand(removeCmd)

		fmt.Printf("Removing directory %s...\n", container.WorkingDir)
		rmCmd := fmt.Sprintf(`rm -rf "%s"`, container.WorkingDir)
		if _, stderr, err := bm.sshClient.ExecuteCommand(rmCmd); err != nil {
			fmt.Printf("Warning: failed to remove directory: %v (stderr: %s)\n", err, stderr)
		}
	}

	fmt.Printf("Done with %s\n\n", container.Name)
	return nil
}

func (bm *BackupManager) streamBackupToMinio(workingDir, backupName string) error {
	// Create tar command that outputs to stdout
	tarCmd := fmt.Sprintf(`cd /var/opt && tar -czf - --exclude="*.tgz" --exclude="*.tar.gz" --exclude="*.zip" "%s"`, workingDir)

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

	// Start the tar command
	if err := session.Start(tarCmd); err != nil {
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
		return fmt.Errorf("tar command failed: %w", err)
	}

	fmt.Printf("Successfully uploaded %s (%d bytes) to Minio\n", backupName, info.Size)
	return nil
}

func (bm *BackupManager) readRemoteFile(filePath string) ([]byte, error) {
	cmd := fmt.Sprintf("cat %s", filePath)
	output, stderr, err := bm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w (stderr: %s)", err, stderr)
	}
	return []byte(output), nil
}
