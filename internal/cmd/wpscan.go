package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ciwg-cli/internal/wpscan"

	"github.com/spf13/cobra"
)

var (
	wpscanOutputFile    string
	wpscanServerRange   string
	wpscanLocal         bool
	wpscanFormat        string
	wpscanUseCSV        bool
	wpscanCSVFile       string
	wpscanAPIKeysCSV    string
	wpscanAPIKeysColumn string
)

var wpscanCmd = &cobra.Command{
	Use:   "wpscan",
	Short: "WordPress vulnerability scanning using WPScan API",
	Long:  `Scan WordPress plugins and themes for vulnerabilities across Docker containers.`,
}

var wpscanScanCmd = &cobra.Command{
	Use:   "scan [user@]host",
	Short: "Scan WordPress sites for vulnerabilities",
	Long: `Scan WordPress plugins and themes for vulnerabilities using the WPScan API.
Can scan locally or across multiple remote servers.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWPScanScan,
}

func init() {
	rootCmd.AddCommand(wpscanCmd)
	wpscanCmd.AddCommand(wpscanScanCmd)

	// WPScan specific flags
	wpscanScanCmd.Flags().StringVarP(&wpscanOutputFile, "output", "o", "wpscan-results.json", "Output file for scan results")
	wpscanScanCmd.Flags().StringVar(&wpscanServerRange, "server-range", "", "Server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')")
	wpscanScanCmd.Flags().BoolVar(&wpscanLocal, "local", false, "Run locally without SSH")
	wpscanScanCmd.Flags().StringVar(&wpscanFormat, "format", "json", "Export format (json or csv)")
	wpscanScanCmd.Flags().BoolVar(&wpscanUseCSV, "use-csv", false, "Use existing CSV file instead of Docker discovery")
	wpscanScanCmd.Flags().StringVar(&wpscanCSVFile, "csv-file", "ciwg-cli-site-results.csv", "CSV file to use for site list")

	// API key management flags
	wpscanScanCmd.Flags().StringVar(&wpscanAPIKeysCSV, "api-keys-csv", "", "CSV file containing API keys")
	wpscanScanCmd.Flags().StringVar(&wpscanAPIKeysColumn, "api-keys-column", "api_key", "Column name for API keys in CSV")

	// SSH connection flags (following the pattern from other commands)
	wpscanScanCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	wpscanScanCmd.Flags().StringP("port", "p", "22", "SSH port")
	wpscanScanCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	wpscanScanCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	wpscanScanCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runWPScanScan(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Initialize scanner
	config := wpscan.Config{
		UseSSH:        !wpscanLocal && len(args) > 0,
		UseCSV:        wpscanUseCSV,
		CSVFile:       wpscanCSVFile,
		ServerRange:   wpscanServerRange,
		Local:         wpscanLocal,
		APIKeysCSV:    wpscanAPIKeysCSV,
		APIKeysColumn: wpscanAPIKeysColumn,
	}

	if config.UseSSH && len(args) > 0 {
		// Parse SSH connection details from args[0]
		hostParts := strings.Split(args[0], "@")
		if len(hostParts) == 2 {
			config.SSHUser = hostParts[0]
			config.SSHHost = hostParts[1]
		} else {
			config.SSHHost = args[0]
		}

		// Override with flags if provided
		if user, _ := cmd.Flags().GetString("user"); user != "" {
			config.SSHUser = user
		}
		if port, _ := cmd.Flags().GetString("port"); port != "" {
			config.SSHPort = port
		}
		if key, _ := cmd.Flags().GetString("key"); key != "" {
			config.SSHKey = key
		}
		if agent, _ := cmd.Flags().GetBool("agent"); !agent {
			config.SSHUseAgent = false
		} else {
			config.SSHUseAgent = true
		}
		if timeout, _ := cmd.Flags().GetDuration("timeout"); timeout > 0 {
			config.SSHTimeout = timeout
		}
	}

	scanner, err := wpscan.NewScanner(config)
	if err != nil {
		return fmt.Errorf("failed to initialize scanner: %w", err)
	}
	defer scanner.Close()

	// Get site list
	var sites []wpscan.SiteInfo
	if config.UseCSV {
		sites, err = scanner.LoadSitesFromFile(config.CSVFile)
		if err != nil {
			return fmt.Errorf("failed to load sites from file: %w", err)
		}
		fmt.Printf("Loaded %d sites from %s\n", len(sites), config.CSVFile)
	} else {
		// Get containers and extract site info
		containers, err := scanner.GetWordPressContainers(ctx)
		if err != nil {
			return fmt.Errorf("failed to get containers: %w", err)
		}

		sites, err = scanner.ContainersToSites(containers)
		if err != nil {
			return fmt.Errorf("failed to convert containers to sites: %w", err)
		}
		fmt.Printf("Found %d WordPress containers across %d servers\n", len(sites), countUniqueServers(sites))
	}

	// Collect plugins and themes
	fmt.Println("Collecting plugins and themes...")
	plugins, themes, err := scanner.CollectAssetsFromSites(ctx, sites)
	if err != nil {
		return fmt.Errorf("failed to collect assets: %w", err)
	}

	fmt.Printf("Found %d unique plugins and %d unique themes\n", len(plugins), len(themes))

	// Scan assets with API
	fmt.Println("Scanning for vulnerabilities...")
	results, err := scanner.ScanAssets(ctx, plugins, themes)
	if err != nil {
		return fmt.Errorf("failed to scan assets: %w", err)
	}

	// Save results
	if err := saveWPScanResults(wpscanOutputFile, results, wpscanFormat); err != nil {
		return fmt.Errorf("failed to save results: %w", err)
	}

	// Print summary
	printScanSummary(results)
	fmt.Printf("Results saved to %s\n", wpscanOutputFile)

	return nil
}

func saveWPScanResults(filename string, results *wpscan.ScanResults, format string) error {
	switch strings.ToLower(format) {
	case "json":
		if filepath.Ext(filename) != ".json" {
			filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ".json"
		}
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filename, data, 0644)

	case "csv":
		if filepath.Ext(filename) != ".csv" {
			filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ".csv"
		}
		return writeWPScanCSV(filename, results)

	default:
		return fmt.Errorf("unsupported format: %s. Use 'json' or 'csv'", format)
	}
}

func writeWPScanCSV(filename string, results *wpscan.ScanResults) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()

	// Write header
	fmt.Fprintln(file, "Type,Name,Version,VulnCount,HighestSeverity,LatestVersion")

	// Write plugin results
	for slug, info := range results.Plugins {
		severity := getHighestSeverity(info.Vulnerabilities)
		fmt.Fprintf(file, "Plugin,%s,%s,%d,%s,%s\n",
			slug, "N/A", len(info.Vulnerabilities), severity, info.LatestVersion)
	}

	// Write theme results
	for slug, info := range results.Themes {
		severity := getHighestSeverity(info.Vulnerabilities)
		fmt.Fprintf(file, "Theme,%s,%s,%d,%s,%s\n",
			slug, "N/A", len(info.Vulnerabilities), severity, info.LatestVersion)
	}

	return nil
}

func getHighestSeverity(vulns []wpscan.Vulnerability) string {
	if len(vulns) == 0 {
		return "None"
	}
	// Simplified severity assessment - in practice, you'd parse the vulnerability details
	return "Medium"
}

func countUniqueServers(sites []wpscan.SiteInfo) int {
	servers := make(map[string]bool)
	for _, site := range sites {
		servers[site.Server] = true
	}
	return len(servers)
}

func printScanSummary(results *wpscan.ScanResults) {
	fmt.Println("\n=== Scan Summary ===")

	pluginVulns := 0
	for _, info := range results.Plugins {
		pluginVulns += len(info.Vulnerabilities)
	}

	themeVulns := 0
	for _, info := range results.Themes {
		themeVulns += len(info.Vulnerabilities)
	}

	fmt.Printf("Plugins scanned: %d\n", len(results.Plugins))
	fmt.Printf("Themes scanned: %d\n", len(results.Themes))
	fmt.Printf("Plugin vulnerabilities: %d\n", pluginVulns)
	fmt.Printf("Theme vulnerabilities: %d\n", themeVulns)
	fmt.Printf("Total vulnerabilities: %d\n", pluginVulns+themeVulns)

	if len(results.Errors) > 0 {
		fmt.Printf("Errors encountered: %d\n", len(results.Errors))

		// Group errors by type for better debugging
		errorCounts := make(map[string]int)
		errorExamples := make(map[string]string)

		for _, errMsg := range results.Errors {
			// Categorize errors
			var category string
			switch {
			case strings.Contains(errMsg, "failed to create SSH client"):
				category = "SSH Connection"
			case strings.Contains(errMsg, "failed to execute WP-CLI command"):
				category = "WP-CLI Execution"
			case strings.Contains(errMsg, "docker exec"):
				category = "Docker Execution"
			case strings.Contains(errMsg, "No such container"):
				category = "Container Not Found"
			case strings.Contains(errMsg, "connection reset"):
				category = "Network Connection"
			case strings.Contains(errMsg, "timeout"):
				category = "Timeout"
			case strings.Contains(errMsg, "permission denied"):
				category = "Permission"
			case strings.Contains(errMsg, "no output from WP-CLI"):
				category = "Empty WP-CLI Output"
			case strings.Contains(errMsg, "unexpected output format"):
				category = "WP-CLI Parse Error"
			default:
				category = "Other"
			}

			errorCounts[category]++
			if errorExamples[category] == "" {
				errorExamples[category] = errMsg
			}
		}

		fmt.Println("\n=== Error Breakdown ===")
		for category, count := range errorCounts {
			fmt.Printf("%s: %d errors\n", category, count)
			fmt.Printf("  Example: %s\n", errorExamples[category])
		}

		// Also log all errors to a file for detailed debugging
		logErrorsToFile(results.Errors)
	}

	fmt.Printf("Scan completed at: %s\n", results.Timestamp)
}

func logErrorsToFile(errors []string) {
	errorLogFile := fmt.Sprintf("wpscan-errors-%s.log", time.Now().Format("2006-01-02-15-04-05"))

	file, err := os.Create(errorLogFile)
	if err != nil {
		fmt.Printf("Warning: Could not create error log file: %v\n", err)
		return
	}
	defer file.Close()

	fmt.Fprintf(file, "WPScan Error Log - %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(file, "Total Errors: %d\n\n", len(errors))

	for i, errMsg := range errors {
		fmt.Fprintf(file, "Error %d: %s\n", i+1, errMsg)
	}

	fmt.Printf("Detailed errors logged to: %s\n", errorLogFile)
}
