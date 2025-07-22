package cmd

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	l "log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
)

var (
	backupDir         string
	gracePeriodDays   int
	cancelledSitesDir string
	testGracePeriod   bool
)

func archiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Archives and removes sites after a grace period.",
		Long: `Scans a directory of cancelled sites for a 'cancellation-epoch.txt' file.
If the timestamp in the file is older than the grace period, the site is archived.

Archival process includes:
1. Exporting the database from the site's Docker container (if found).
2. Stopping and removing the Docker container.
3. Creating a .tar.gz archive of the site files.
4. Removing the site directory.`,
		Run: runArchive,
	}

	// Assumes 'dryRun' is a global variable in the cmd package (e.g., in root.go)
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without making changes.")
	cmd.PersistentFlags().StringVar(&backupDir, "backup-dir", "/var/opt/backups", "Directory to store site archives.")
	cmd.PersistentFlags().IntVar(&gracePeriodDays, "grace-period", 30, "Number of days before a cancelled site is archived.")
	cmd.PersistentFlags().StringVar(&cancelledSitesDir, "cancelled-dir", "/var/opt/cancelled_sites", "Directory containing cancelled sites.")
	cmd.PersistentFlags().BoolVar(&testGracePeriod, "test-grace-period", false, "Test the grace period as seconds with a countdown.")

	return cmd
}

func init() {
	// Assumes 'rootCmd' is a global variable in the cmd package
	rootCmd.AddCommand(archiveCmd())
}

func runArchive(cmd *cobra.Command, args []string) {
	if testGracePeriod {
		durationInSeconds := gracePeriodDays
		if durationInSeconds <= 0 {
			l.Println("For testing, --grace-period must be a positive number of seconds.")
			return
		}
		l.Printf("Starting test countdown for %d seconds.", durationInSeconds)
		for i := durationInSeconds; i > 0; i-- {
			fmt.Printf("\rTime remaining: %d seconds... ", i)
			time.Sleep(1 * time.Second)
		}
		fmt.Println("\rCountdown finished.              ") // Use spaces to clear the line
		return
	}

	if _, err := exec.LookPath("docker"); err != nil {
		l.Fatalf("Docker command not found in PATH. Please install Docker and ensure it's accessible.")
	}

	l.Printf("Starting archive job for sites in %s...", cancelledSitesDir)

	cutoff := time.Now().AddDate(0, 0, -gracePeriodDays)
	l.Printf("Grace period is %d days. Archiving sites cancelled before %s.", gracePeriodDays, cutoff.Format("2006-01-02"))

	sites, err := os.ReadDir(cancelledSitesDir)
	if err != nil {
		if os.IsNotExist(err) {
			l.Printf("Cancelled sites directory '%s' does not exist. Nothing to do.", cancelledSitesDir)
			return
		}
		l.Fatalf("Error reading cancelled sites directory %s: %v", cancelledSitesDir, err)
	}

	archivedCount, skippedCount, errorCount := 0, 0, 0

	for _, site := range sites {
		if !site.IsDir() {
			continue
		}

		siteName := site.Name()
		sitePath := filepath.Join(cancelledSitesDir, siteName)
		epochFile := filepath.Join(sitePath, "cancellation-epoch.txt")

		content, err := os.ReadFile(epochFile)
		if err != nil {
			l.Printf("Skipping %s: no valid cancellation-epoch.txt file found.", siteName)
			skippedCount++
			continue
		}

		epoch, err := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64)
		if err != nil {
			l.Printf("Error parsing epoch for %s: %v. Skipping.", siteName, err)
			errorCount++
			continue
		}

		cancellationTime := time.Unix(epoch, 0)
		if cancellationTime.After(cutoff) {
			l.Printf("Skipping %s: cancelled on %s, still within grace period.", siteName, cancellationTime.Format("2006-01-02"))
			skippedCount++
			continue
		}

		l.Printf("Archiving %s: cancelled on %s, grace period expired.", siteName, cancellationTime.Format("2006-01-02"))
		if err := archiveAndRemoveSite(sitePath); err != nil {
			l.Printf("Failed to archive site %s: %v", siteName, err)
			errorCount++
		} else {
			l.Printf("Successfully archived and removed %s.", siteName)
			archivedCount++
		}
	}

	l.Println("\n--- Archive Job Summary ---")
	l.Printf("Archived: %d", archivedCount)
	l.Printf("Skipped:  %d", skippedCount)
	l.Printf("Errors:   %d", errorCount)
	l.Println("---------------------------")
}

func archiveAndRemoveSite(sitePath string) error {
	siteName := filepath.Base(sitePath)
	baseName := strings.Split(siteName, ".")[0]
	containerName := "wp_" + baseName

	if dryRun {
		l.Println("\n-- DRY RUN --")
		l.Printf("[DRY RUN] Would process site: %s", siteName)
		l.Printf("[DRY RUN]   - Find and stop/remove container: %s", containerName)
		l.Printf("[DRY RUN]   - Export database from container")
		archivePath := filepath.Join(backupDir, fmt.Sprintf("%s_%s.tar.gz", siteName, time.Now().Format("20060102_150405")))
		l.Printf("[DRY RUN]   - Create archive at %s", archivePath)
		l.Printf("[DRY RUN]   - Remove directory %s", sitePath)
		l.Println("-- END DRY RUN --")
		return nil
	}

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("could not create docker client: %w", err)
	}

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("could not create backup directory %s: %w", backupDir, err)
	}

	// Find container
	filter := filters.NewArgs(filters.Arg("name", "^/"+containerName+"$"))
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: filter})
	if err != nil {
		l.Printf("Warning: could not list containers to find %s: %v", containerName, err)
	}

	if len(containers) == 0 {
		l.Printf("  - Container %s not found. Proceeding with file archival only.", containerName)
	} else {
		containerID := containers[0].ID
		l.Printf("  - Found container %s (%s)", containerName, containerID[:12])

		// Export DB using `docker exec`
		dbExportCmd := exec.Command("docker", "exec", containerName, "wp", "db", "export", "/var/www/html/wp-content/mysql.sql", "--allow-root")
		if output, err := dbExportCmd.CombinedOutput(); err != nil {
			l.Printf("  - Warning: could not export database. It might be stopped or wp-cli is unavailable. Error: %v. Output: %s", err, string(output))
		} else {
			l.Printf("  - Database exported successfully.")
		}

		// Stop and remove container
		l.Printf("  - Stopping and removing container %s...", containerName)
		timeoutSeconds := 30
		if err := cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeoutSeconds}); err != nil {
			l.Printf("  - Warning: could not stop container %s: %v", containerName, err)
		}
		if err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{}); err != nil {
			l.Printf("  - Warning: could not remove container %s: %v", containerName, err)
		}
	}

	// Create archive
	archiveName := fmt.Sprintf("%s_%s.tar.gz", siteName, time.Now().Format("20060102_150405"))
	archivePath := filepath.Join(backupDir, archiveName)
	l.Printf("  - Archiving %s to %s...", sitePath, archivePath)
	if err := createTarGz(sitePath, archivePath); err != nil {
		return fmt.Errorf("could not create archive: %w", err)
	}

	// Remove site directory
	l.Printf("  - Removing site directory %s...", sitePath)
	if err := os.RemoveAll(sitePath); err != nil {
		return fmt.Errorf("could not remove site directory: %w", err)
	}

	return nil
}

// createTarGz creates a .tar.gz archive of a source directory.
func createTarGz(source, target string) error {
	outFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	source = filepath.Clean(source)
	baseDir := filepath.Dir(source)

	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		header.Name = relPath

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		return err
	})

}
