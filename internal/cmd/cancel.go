package cmd

import (
	"fmt"
	l "log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
)

var (
	containerPath string
	sitesDir      string
)

// Abridged list of common Top-Level Domains.
var tlds = []string{
	"com", "org", "net", "edu", "gov", "io", "co", "ai", "app", "dev", "tech",
	"info", "biz", "xyz", "me", "us", "ca", "uk", "au", "de", "fr", "jp", "cn",
	"online", "store", "site", "website", "space", "agency", "solutions", "studio",
	"cloud", "digital", "world", "life", "today", "news", "bl", "shop", "art",
	"design", "photography", "video", "music", "software", "code", "systems",
	"global", "marketing", "media", "network", "finance", "consulting", "community",
	"health", "care", "clinic", "realty", "realestate", "homes", "properties",
	"legal", "law", "attorney", "lawyer", "travel", "tours", "auto", "cars", "car",
	"fashion", "style", "beauty", "fitness", "fit", "yoga", "food", "coffee",
	"pizza", "restaurant", "bar", "cafe", "games", "game", "play", "show", "movie",
	"film", "tv", "pro", "work", "works", "tools", "build", "farm", "garden", "green",
	"eco", "asia", "eu", "london", "nyc", "paris", "tokyo",
}

func cancelCmds() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel a site by moving its container to a cancelled directory.",
		Long: `This command cancels a website installation by moving its container directory.
You can specify the container path directly or use the interactive mode to select a site.`,
		Run: runCancel,
	}

	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without making changes.")
	cmd.PersistentFlags().StringVar(&containerPath, "container-path", "", "Direct path to the site container to cancel.")
	cmd.PersistentFlags().StringVar(&sitesDir, "sites-dir", "/var/opt", "The directory where site containers are located.")

	return cmd
}

func init() {
	rootCmd.AddCommand(cancelCmds())
}

func runCancel(cmd *cobra.Command, args []string) {
	targetPath := containerPath

	if targetPath == "" {
		selectedSite, err := selectSiteInteractive()
		if err != nil {
			l.Fatalf("Could not select a site: %v", err)
		}
		if selectedSite == "" {
			fmt.Println("No site selected. Exiting.")
			return
		}
		targetPath = selectedSite
	}

	cancelSite(targetPath)
}

func selectSiteInteractive() (string, error) {
	fmt.Printf("Searching for sites in %s...\n", sitesDir)

	// Use os.ReadDir which is preferred over ioutil.ReadDir
	files, err := os.ReadDir(sitesDir)
	if err != nil {
		return "", fmt.Errorf("failed to read sites directory %s: %w", sitesDir, err)
	}

	// Regex to match {domainName}.{TLD}
	tldPattern := strings.Join(tlds, "|")
	re, err := regexp.Compile(`^[a-zA-Z0-9-]+\.(` + tldPattern + `)$`)
	if err != nil {
		return "", fmt.Errorf("failed to compile regex: %w", err)
	}

	var siteDirs []string
	for _, file := range files {
		if file.IsDir() && re.MatchString(file.Name()) {
			siteDirs = append(siteDirs, file.Name())
		}
	}

	if len(siteDirs) == 0 {
		return "", fmt.Errorf("no site directories found in %s matching the expected pattern", sitesDir)
	}

	var selection string
	prompt := &survey.Select{
		Message:  "Select a site to cancel:",
		Options:  siteDirs,
		PageSize: 15,
	}
	if err := survey.AskOne(prompt, &selection); err != nil {
		// Handles user cancellation (e.g., Ctrl+C)
		return "", nil
	}

	return filepath.Join(sitesDir, selection), nil
}

func cancelSite(path string) {
	path = filepath.Clean(path)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		l.Fatalf("Error: Directory does not exist at %s", path)
	}

	parentDir := filepath.Dir(path)
	cancelledSitesDir := filepath.Join(parentDir, "cancelled_sites")
	siteName := filepath.Base(path)
	destinationPath := filepath.Join(cancelledSitesDir, siteName)

	fmt.Printf("Preparing to cancel site: %s\n", siteName)
	fmt.Printf("  Source: %s\n", path)
	fmt.Printf("  Destination: %s\n", destinationPath)

	if dryRun {
		fmt.Println("\n-- DRY RUN --")
		fmt.Printf("[DRY RUN] Would check/create directory: %s\n", cancelledSitesDir)
		fmt.Printf("[DRY RUN] Would move %s to %s\n", path, destinationPath)
		epochFile := filepath.Join(destinationPath, "cancellation-epoch.txt")
		fmt.Printf("[DRY RUN] Would create cancellation timestamp file: %s\n", epochFile)
		fmt.Println("\nDry run complete. No changes were made.")
		return
	}

	if err := os.MkdirAll(cancelledSitesDir, 0755); err != nil {
		l.Fatalf("Error creating directory %s: %v", cancelledSitesDir, err)
	}
	if err := os.Rename(path, destinationPath); err != nil {
		l.Fatalf("Error moving directory from %s to %s: %v", path, destinationPath, err)
	}

	// Create a cancellation timestamp file
	epochFile := filepath.Join(destinationPath, "cancellation-epoch.txt")
	epochTime := fmt.Sprintf("%d", time.Now().Unix())
	if err := os.WriteFile(epochFile, []byte(epochTime), 0644); err != nil {
		// Use a non-fatal log message here so cancellation itself is not considered a failure.
		l.Printf("Warning: could not write cancellation timestamp file to %s: %v", epochFile, err)
	}

	fmt.Printf("\nSuccessfully cancelled site %s.\n", siteName)
	fmt.Printf("Moved to: %s\n", destinationPath)
}
