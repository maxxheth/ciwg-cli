package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ciwg-cli/internal/auth"

	"github.com/joho/godotenv"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/cobra"
)

var (
	minioEndpoint     string
	minioAccessKey    string
	minioSecretKey    string
	minioBucket       string
	minioSSL          bool
	backupServerRange string
)

var backupCmd = &cobra.Command{
	Use:   "backup [user@]host",
	Short: "Backup WordPress sites to Minio S3 storage",
	Long: `This command finds WordPress Docker containers on a server, exports their databases using WP-CLI, 
creates a tarball of their data, and uploads it to a Minio S3-compatible bucket.`,
	Args: cobra.MaximumNArgs(1),
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Attempt to load .env file from the current directory.
		// This allows setting MINIO_* variables in a .env file.
		godotenv.Load()
	},
	RunE: runBackup,
}

func init() {
	rootCmd.AddCommand(backupCmd)

	// Minio connection flags
	backupCmd.Flags().StringVar(&minioEndpoint, "minio-endpoint", "", "Minio server endpoint (e.g., 's3.amazonaws.com')")
	backupCmd.Flags().StringVar(&minioAccessKey, "minio-access-key", "", "Minio access key ID")
	backupCmd.Flags().StringVar(&minioSecretKey, "minio-secret-key", "", "Minio secret access key")
	backupCmd.Flags().StringVar(&minioBucket, "minio-bucket", "", "Minio bucket name")
	backupCmd.Flags().BoolVar(&minioSSL, "minio-ssl", true, "Use SSL for Minio connection")

	// Server and SSH flags
	backupCmd.Flags().StringVar(&backupServerRange, "server-range", "", "Server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')")
	backupCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	backupCmd.Flags().StringP("port", "p", "22", "SSH port")
	backupCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	backupCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	backupCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runBackup(cmd *cobra.Command, args []string) error {
	// Load config from flags or fall back to environment variables
	if minioEndpoint == "" {
		minioEndpoint = os.Getenv("MINIO_ENDPOINT")
	}
	if minioBucket == "" {
		minioBucket = os.Getenv("MINIO_BUCKET")
	}
	if minioAccessKey == "" {
		minioAccessKey = os.Getenv("MINIO_ACCESS_KEY")
	}
	if minioSecretKey == "" {
		minioSecretKey = os.Getenv("MINIO_SECRET_KEY")
	}

	// After attempting to load, validate that all required config exists
	if minioEndpoint == "" {
		return fmt.Errorf("minio endpoint is required. Provide it via the --minio-endpoint flag or MINIO_ENDPOINT environment variable")
	}
	if minioBucket == "" {
		return fmt.Errorf("minio bucket is required. Provide it via the --minio-bucket flag or MINIO_BUCKET environment variable")
	}
	if minioAccessKey == "" || minioSecretKey == "" {
		return fmt.Errorf("minio access key and secret key are required. Provide them via flags or environment variables (MINIO_ACCESS_KEY, MINIO_SECRET_KEY)")
	}

	// Initialize Minio client
	minioClient, err := minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioAccessKey, minioSecretKey, ""),
		Secure: minioSSL,
	})
	if err != nil {
		return fmt.Errorf("could not initialize minio client: %w", err)
	}

	ctx := context.Background()
	exists, err := minioClient.BucketExists(ctx, minioBucket)
	if err != nil {
		return fmt.Errorf("failed to check for minio bucket: %w", err)
	}
	if !exists {
		return fmt.Errorf("minio bucket '%s' does not exist", minioBucket)
	}
	fmt.Printf("Successfully connected to Minio bucket '%s'.\n", minioBucket)

	// Process servers
	if backupServerRange != "" {
		pattern, start, end, exclusions, err := parseServerRange(backupServerRange)
		if err != nil {
			return fmt.Errorf("error parsing server range: %w", err)
		}

		for i := start; i <= end; i++ {
			if exclusions[i] {
				continue
			}
			serverHost := fmt.Sprintf(pattern, i)
			fmt.Printf("\n--- Processing server: %s ---\n", serverHost)
			if err := processServerBackup(cmd, serverHost, minioClient); err != nil {
				fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", serverHost, err)
			}
		}
	} else {
		if len(args) < 1 {
			return fmt.Errorf("remote host argument is required when not using --server-range")
		}
		serverHost := args[0]
		fmt.Printf("\n--- Processing server: %s ---\n", serverHost)
		if err := processServerBackup(cmd, serverHost, minioClient); err != nil {
			return fmt.Errorf("error processing %s: %w", serverHost, err)
		}
	}

	fmt.Println("\nBackup process completed.")
	return nil
}

func processServerBackup(cmd *cobra.Command, serverHost string, minioClient *minio.Client) error {
	sshClient, err := createSSHClient(cmd, serverHost)
	if err != nil {
		return fmt.Errorf("failed to create SSH client for %s: %w", serverHost, err)
	}
	defer sshClient.Close()

	// 1. Find WordPress containers
	containers, err := findWpContainers(sshClient)
	if err != nil {
		return fmt.Errorf("failed to find WP containers: %w", err)
	}

	if len(containers) == 0 {
		fmt.Println("No 'wp_*' containers found on this server.")
		return nil
	}

	fmt.Printf("Found %d WordPress containers.\n", len(containers))

	for containerName, workingDir := range containers {
		domain := filepath.Base(workingDir)
		fmt.Printf("Backing up site '%s' (container: %s)...\n", domain, containerName)

		// 2. Export database using WP-CLI
		dbBackupFile := fmt.Sprintf("db_backup_%d.sql", time.Now().Unix())
		dbBackupPath := filepath.Join(workingDir, "www", "wp-content", dbBackupFile)
		dbExportCmd := fmt.Sprintf("docker exec %s wp db export %s --allow-root", containerName, dbBackupPath)

		fmt.Printf("  - Exporting database to %s...\n", dbBackupPath)
		_, stderr, err := sshClient.ExecuteCommand(dbExportCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  - Error exporting database for %s: %v\n  - Stderr: %s\n", containerName, err, stderr)
			continue // Skip to next container
		}

		// 3. Create tarball and pipe to Minio
		fmt.Println("  - Creating tarball and uploading to Minio...")
		tarballName := fmt.Sprintf("backup-%s.tar.gz", time.Now().Format("2006-01-02-150405"))
		minioObjectName := fmt.Sprintf("%s/%s", domain, tarballName)

		// Command to create tarball and stream to stdout
		parentDir := filepath.Dir(workingDir)
		baseDir := filepath.Base(workingDir)
		tarCmd := fmt.Sprintf("tar -czf - -C %s %s", parentDir, baseDir)

		// Execute tar command via a session to get stdout pipe
		session, err := sshClient.GetSession()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  - Failed to create SSH session: %v\n", err)
			continue
		}
		defer session.Close()

		stdoutPipe, err := session.StdoutPipe()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  - Failed to get stdout pipe: %v\n", err)
			continue
		}

		if err := session.Start(tarCmd); err != nil {
			fmt.Fprintf(os.Stderr, "  - Failed to start tar command: %v\n", err)
			continue
		}

		// 4. Upload the stream to Minio
		uploadInfo, err := minioClient.PutObject(context.Background(), minioBucket, minioObjectName, stdoutPipe, -1, minio.PutObjectOptions{
			ContentType: "application/gzip",
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  - Failed to upload to Minio: %v\n", err)
			session.Wait() // Ensure session resources are cleaned up
			continue
		}

		if err := session.Wait(); err != nil {
			// This might catch errors if the tar command fails after starting
			if exitErr, ok := err.(*os.SyscallError); ok && exitErr.Err.Error() == "EOF" {
				// Ignore EOF, it's expected when the remote command finishes
			} else if err != io.EOF {
				fmt.Fprintf(os.Stderr, "  - Error during tar command execution: %v\n", err)
				continue
			}
		}

		fmt.Printf("  - Successfully uploaded %s (Size: %.2f MB)\n", minioObjectName, float64(uploadInfo.Size)/1024/1024)

		// 5. Clean up the database dump on the remote server
		fmt.Println("  - Cleaning up remote database dump...")
		cleanupCmd := fmt.Sprintf("rm %s", dbBackupPath)
		_, stderr, err = sshClient.ExecuteCommand(cleanupCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  - Warning: failed to clean up remote file %s: %v\n  - Stderr: %s\n", dbBackupPath, err, stderr)
		}
	}

	return nil
}

// findWpContainers discovers running containers starting with "wp_" and their working directories.
func findWpContainers(sshClient *auth.SSHClient) (map[string]string, error) {
	containerMap := make(map[string]string)

	// Get all 'wp_*' container names
	listCmd := "docker ps --filter 'name=wp_*' --format '{{.Names}}'"
	stdout, stderr, err := sshClient.ExecuteCommand(listCmd)
	if err != nil {
		if stdout == "" { // No containers found is not an error
			return containerMap, nil
		}
		return nil, fmt.Errorf("failed to list containers: %w, stderr: %s", err, stderr)
	}

	containerNames := strings.Fields(stdout)
	if len(containerNames) == 0 {
		return containerMap, nil
	}

	// Get working directory for each container
	for _, name := range containerNames {
		inspectCmd := fmt.Sprintf(`docker inspect -f '{{.Config.Labels "com.docker.compose.project.working_dir"}}' %s`, name)
		workDir, stderr, err := sshClient.ExecuteCommand(inspectCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not inspect container %s to find working dir: %v. Stderr: %s\n", name, err, stderr)
			continue
		}

		workDir = strings.TrimSpace(workDir)
		if workDir == "" {
			fmt.Fprintf(os.Stderr, "Working directory not found for container %s, skipping.\n", name)
			continue
		}
		containerMap[name] = workDir
	}

	return containerMap, nil
}
