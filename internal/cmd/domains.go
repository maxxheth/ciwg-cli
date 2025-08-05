package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"ciwg-cli/internal/auth"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	overwrite    bool
	appendFlag   bool
	remove       bool
	dryRun       bool
	verboseCount int
	domainFlag   string
	excludeList  string
	report       bool
)

func newDomainsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domains",
		Short: "Manage domain checks",
		Long:  `Check active and inactive domains, with options to remove inactive ones.`,
	}

	cmd.PersistentFlags().StringVarP(&outputFile, "output", "o", "", "Write CSV results to FILE")
	cmd.PersistentFlags().BoolVar(&overwrite, "overwrite", false, "Overwrite FILE without prompting")
	cmd.PersistentFlags().BoolVar(&appendFlag, "append", false, "Append to FILE without prompting")
	cmd.PersistentFlags().BoolVar(&remove, "remove", false, "Backup and delete sites that are not on this server (inactive only)")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Show what would be backed up and removed without making changes")
	cmd.PersistentFlags().CountVarP(&verboseCount, "verbose", "v", "Set verbosity level (e.g., -v, -vv, -vvv)")
	cmd.PersistentFlags().StringVar(&domainFlag, "domain", "", "Check a single domain instead of scanning all")
	cmd.PersistentFlags().Bool("local", false, "Run locally without SSH (assumes this server hosts the sites)")
	cmd.PersistentFlags().StringVar(&excludeList, "exclude", "", "Pipebar-delimited list or file of domains to exclude from verification")
	cmd.PersistentFlags().BoolVar(&report, "report", false, "Generate a formatted report instead of CSV output")
	cmd.PersistentFlags().StringVar(&serverRange, "server-range", "", "Server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')")

	// Add SSH connection flags to all subcommands
	cmd.PersistentFlags().StringP("user", "u", "", "SSH username (default: current user)")
	cmd.PersistentFlags().StringP("port", "p", "22", "SSH port")
	cmd.PersistentFlags().StringP("key", "k", "", "Path to SSH private key")
	cmd.PersistentFlags().BoolP("agent", "a", true, "Use SSH agent")
	cmd.PersistentFlags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")

	checkActiveCmd := &cobra.Command{
		Use:   "check-active [user@]host",
		Short: "Check for domains that resolve to the server's IP (and backup/remove inactive ones if requested)",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			// Check if server-range is provided
			if serverRange != "" {
				processServerRange(cmd, createSSHClient, serverRange, excludeList, remove, dryRun, report)
				return
			}

			localFlag, _ := cmd.Flags().GetBool("local")
			var serverIP string
			var domainPath string
			var domains []string
			var err error
			var hostname string

			if envPath := os.Getenv("DOMAIN_PATH"); envPath != "" {
				domainPath = envPath
			} else {
				domainPath = "/var/opt"
			}

			if localFlag {
				serverIP, err = getLocalPublicIP()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting local public IP: %v\n", err)
					os.Exit(1)
				}
				hostname, err = getLocalHostname()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting local hostname: %v\n", err)
					os.Exit(1)
				}
				if domainFlag != "" {
					domains = []string{domainFlag}
					log(1, "Checking single domain: %s", domainFlag)
				} else {
					domains, err = findDomainsLocal(domainPath)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error scanning domains: %v\n", err)
						os.Exit(1)
					}
				}
			} else {
				if len(args) < 1 {
					fmt.Fprintf(os.Stderr, "Remote mode requires [user@]host argument\n")
					os.Exit(1)
				}

				sshClient, err := createSSHClient(cmd, args[0])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error creating SSH client: %v\n", err)
					os.Exit(1)
				}
				defer sshClient.Close()

				serverIP, err = getPublicIP(sshClient)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting public IP: %v\n", err)
					os.Exit(1)
				}

				hostname, err = getRemoteHostname(sshClient)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting remote hostname: %v\n", err)
					os.Exit(1)
				}

				if domainFlag != "" {
					domains = []string{domainFlag}
					log(1, "Checking single domain: %s", domainFlag)
				} else {
					domains, err = findDomains(sshClient, domainPath)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error scanning domains: %v\n", err)
						os.Exit(1)
					}
				}
			}

			// Parse exclude list
			excluded := make(map[string]struct{})
			if excludeList != "" {
				if strings.Contains(excludeList, "|") {
					for d := range strings.SplitSeq(excludeList, "|") {
						excluded[strings.TrimSpace(d)] = struct{}{}
					}
				} else {
					// Assume it's a file
					data, err := os.ReadFile(excludeList)
					if err == nil {
						for _, d := range strings.Split(string(data), "\n") {
							d = strings.TrimSpace(d)
							if d != "" {
								excluded[d] = struct{}{}
							}
						}
					}
				}
			}

			// Cache results for final report generation
			var results []string
			var removedDomains []string

			for _, domain := range domains {
				if _, skip := excluded[domain]; skip {
					log(1, "Skipping excluded domain: %s", domain)
					continue
				}
				match, _, err := domainMatchesServer(domain, serverIP)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error checking domain %s: %v\n", domain, err)
					continue
				}

				// Format: URL,Active,Host
				hostDisplay := hostname
				if !strings.HasSuffix(hostname, ".ciwgserver.com") && hostname != "localhost" {
					hostDisplay = hostname + ".ciwgserver.com"
				}

				if match {
					results = append(results, fmt.Sprintf("%s,true,%s", domain, hostDisplay))
				} else {
					// Initially mark as inactive
					results = append(results, fmt.Sprintf("%s,false,%s", domain, hostDisplay))

					// Perform removal operations if requested
					if remove || dryRun {
						var removalSuccess bool
						if localFlag {
							removalSuccess = backupAndRemoveLocal(domainPath, domain, dryRun)
						} else {
							sshClient, _ := createSSHClient(cmd, args[0])
							removalSuccess = backupAndRemove(sshClient, domainPath, domain, dryRun)
						}

						// Track successfully removed domains
						if removalSuccess || dryRun {
							removedDomains = append(removedDomains, domain)
						}
					}
				}
			}

			// Generate final report with complete data
			if report {
				generateFinalReport(results, removedDomains, remove || dryRun)
			} else {
				writeResults(results)
			}
		},
	}

	cmd.AddCommand(checkActiveCmd)
	return cmd
}

func init() {
	rootCmd.AddCommand(newDomainsCmd())
}

func getPublicIP(client *auth.SSHClient) (string, error) {
	log(2, "Fetching public IP from remote server")
	cmd := "dig +short myip.opendns.com @resolver1.opendns.com"
	stdout, stderr, err := client.ExecuteCommand(cmd)
	if err != nil {
		log(1, "dig command failed: %v. Stderr: %s", err, stderr)
		log(2, "Falling back to curl to get public IP")
		cmd = "curl -s https://api.ipify.org"
		stdout, stderr, err = client.ExecuteCommand(cmd)
		if err != nil {
			return "", fmt.Errorf("failed to get public IP via curl: %w, stderr: %s", err, stderr)
		}
	}
	ipStr := strings.TrimSpace(stdout)
	log(1, "Server public IP: %s", ipStr)
	return ipStr, nil
}

func findDomains(client *auth.SSHClient, domainPath string) ([]string, error) {
	log(1, "Scanning for domains in %s on remote server", domainPath)
	cmd := fmt.Sprintf("find %s -name 'docker-compose.yml'", domainPath)
	stdout, stderr, err := client.ExecuteCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to find domains: %w, stderr: %s", err, stderr)
	}

	var domains []string
	domainRegex := regexp.MustCompile(`(?i)^(www\.)?([a-z0-9-]+(\.[a-z]{2,})+)$`)
	files := strings.SplitSeq(strings.TrimSpace(stdout), "\n")
	for file := range files {
		if file != "" {
			domain := filepath.Base(filepath.Dir(file))
			// Remove any subdomains such as "www"
			domain = strings.TrimPrefix(domain, "www.")

			// Verify that the domain matches the expected format
			if !domainRegex.MatchString(domain) {
				log(1, "Skipping invalid domain: %s", domain)
				continue
			}

			domains = append(domains, domain)

			// Dedupe domains

			if !slices.Contains(domains, domain) {
				domains = append(domains, domain)
			}

		}
	}
	log(1, "Found %d domains to check in %s", len(domains), domainPath)
	return domains, nil
}

func domainMatchesServer(domain, serverIP string) (bool, []string, error) {
	// Try Cloudflare API first
	cfEmail := os.Getenv("CLOUDFLARE_EMAIL")
	cfAPIKey := os.Getenv("CLOUDFLARE_API_KEY")

	if cfEmail != "" && cfAPIKey != "" {
		log(2, "Cloudflare credentials found, checking via API for domain %s", domain)
		_, cfRecords, err := checkCloudflare(domain, serverIP, cfEmail, cfAPIKey)
		if err == nil {
			if slices.Contains(cfRecords, serverIP) {
				return true, cfRecords, nil
			}
			return false, cfRecords, nil
		}
		log(1, "Cloudflare API check failed for %s: %v. Falling back to DNS.", domain, err)
	}
	// Fallback: direct DNS lookup
	log(2, "Checking DNS A records for %s", domain)
	aRecords, err := net.LookupHost(domain)
	if err != nil {
		return false, nil, err
	}
	nsMatch, nsRecords, err := checkCFNSRecords(domain)

	hasCloudflareNS := false
	for _, ns := range nsRecords {
		if strings.HasSuffix(ns, "ns.cloudflare.com") {
			hasCloudflareNS = true
			break
		}
	}

	// fmt.Printf("NS records for %s: %v | Cloudflare NS: %v\n | Has Cloudflare NS subdomain: %v\n", domain, nsRecords, hasCloudflareNS, nsMatch)

	if err != nil {
		return false, nil, fmt.Errorf("error checking NS records for %s: %v", domain, err)
	}

	if !nsMatch && hasCloudflareNS {
		log(1, "NS records for %s do not match expected values: %v", domain, nsRecords)

		fmt.Printf("Domain %s managed under different Cloudflare account. DNS A records: %v", domain, aRecords)

		// Print a line break
		fmt.Println()

		return true, aRecords, nil // Return true because we don't know for sure whether the DNS records on that Cloudflare account actually point to our servers or not.
	}

	log(3, "DNS A records for %s: %v", domain, aRecords)
	if slices.Contains(aRecords, serverIP) {
		return true, aRecords, nil
	}
	return false, aRecords, nil
}

func checkCloudflare(domain, serverIP, email, apiKey string) (bool, []string, error) {
	// Get zone ID
	zoneID, err := getCloudflareZoneID(domain, email, apiKey)
	if err != nil || zoneID == "" {
		return false, nil, err
	}
	log(3, "Cloudflare Zone ID for %s: %s", domain, zoneID)
	// Get A records
	records, err := getCloudflareARecords(zoneID, domain, email, apiKey)
	if err != nil {
		return false, nil, err
	}
	log(3, "Cloudflare A records for %s: %v | Server IP: %s", domain, records, serverIP)
	// Check if server IP is in A record
	if slices.Contains(records, serverIP) {
		return true, records, nil
	}
	return false, records, nil
}

func getCloudflareZoneID(domain, email, apiKey string) (string, error) {
	log(3, "Fetching Cloudflare Zone ID for %s", domain)
	apiURL := "https://api.cloudflare.com/client/v4/zones?name=" + domain
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Auth-Email", email)
	req.Header.Set("X-Auth-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Add timeout to prevent hanging
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// Parse JSON
	var result struct {
		Success bool `json:"success"`
		Result  []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	log(4, "Cloudflare ZoneID API response for %s: %s", domain, string(body))
	if !result.Success || len(result.Result) == 0 {
		return "", fmt.Errorf("error: Cloudflare API error or no zone found")
	}
	return result.Result[0].ID, nil
}

func getCloudflareARecords(zoneID, domain, email, apiKey string) ([]string, error) {
	log(3, "Fetching Cloudflare A records for zone %s, domain %s", zoneID, domain)
	apiURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?type=A&name=%s", zoneID, domain)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Auth-Email", email)
	req.Header.Set("X-Auth-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Add timeout to prevent hanging
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Parse JSON
	var result struct {
		Success bool `json:"success"`
		Result  []struct {
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	log(4, "Cloudflare A-Record API response for %s: %s", domain, string(body))
	if !result.Success {
		return nil, fmt.Errorf("error: Cloudflare API error getting A records")
	}
	ips := make([]string, 0)
	for _, rec := range result.Result {
		ips = append(ips, rec.Content)
	}
	return ips, nil
}

func writeResults(results []string) {
	if outputFile == "" {
		// Print header to stdout
		fmt.Println("URL,Active,Host")
		for _, line := range results {
			fmt.Println(line)
		}
		return
	}
	var f *os.File
	var err error
	if overwrite {
		f, err = os.Create(outputFile)
	} else if appendFlag {
		f, err = os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	} else {
		if _, err := os.Stat(outputFile); err == nil {
			fmt.Fprintf(os.Stderr, "File %s exists. Use --overwrite or --append.\n", outputFile)
			return
		}
		f, err = os.Create(outputFile)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening output file: %v\n", err)
		return
	}
	defer f.Close()

	// Write header to file (only if creating new file or overwriting)
	if !appendFlag {
		fmt.Fprintln(f, "URL,Active,Host")
	}

	for _, line := range results {
		fmt.Fprintln(f, line)
	}
	fmt.Fprintf(os.Stderr, "CSV results written to %s\n", outputFile)
}

type DomainResult struct {
	Domain string
	Active bool
	Host   string
}

func writeReportToFile(activeCount, inactiveCount int, hostCounts map[string]int, inactiveDomains []string) {
	var f *os.File
	var err error

	if overwrite {
		f, err = os.Create(outputFile)
	} else if appendFlag {
		f, err = os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	} else {
		if _, err := os.Stat(outputFile); err == nil {
			fmt.Fprintf(os.Stderr, "File %s exists. Use --overwrite or --append.\n", outputFile)
			return
		}
		f, err = os.Create(outputFile)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening output file: %v\n", err)
		return
	}
	defer f.Close()

	// Write report to file
	fmt.Fprintln(f, "# WordPress Website Domain Analysis Report")
	fmt.Fprintln(f, "")
	fmt.Fprintln(f, "## Overall Summary")
	fmt.Fprintln(f, "")
	fmt.Fprintf(f, "| %d Websites Active | %d Websites Inactive |\n", activeCount, inactiveCount)
	fmt.Fprintln(f, "| :---: | :---: |")
	fmt.Fprintln(f, "")
	fmt.Fprintln(f, "---")
	fmt.Fprintln(f, "")

	if activeCount > 0 {
		fmt.Fprintln(f, "## Server Inventory Summary")
		fmt.Fprintln(f, "")

		var tableRows []string
		colCount := 0
		currentRow := ""

		for host, count := range hostCounts {
			switch colCount {
			case 0:
				currentRow = fmt.Sprintf("| %s | %d Domains", host, count)
			case 1:
				currentRow += fmt.Sprintf(" | %s | %d Domains |\n", host, count)
				tableRows = append(tableRows, currentRow)
				currentRow = ""
				colCount = -1
			}
			colCount++
		}

		if currentRow != "" && colCount == 1 {
			currentRow += " |  |  |\n"
			tableRows = append(tableRows, currentRow)
		}

		fmt.Fprintln(f, "| Server | Domain Count | Server | Domain Count |")
		fmt.Fprintln(f, "| :---- | :---- | :---- | :---- |")

		for _, row := range tableRows {
			fmt.Fprint(f, row)
		}

		fmt.Fprintln(f, "")
		fmt.Fprintln(f, "---")
		fmt.Fprintln(f, "")
	}

	if len(inactiveDomains) > 0 {
		fmt.Fprintln(f, "## Inactive Domain Details")
		fmt.Fprintln(f, "")
		fmt.Fprintln(f, "| Domains Removed/Inactive | Active Domains by Server |")
		fmt.Fprintln(f, "| :---- | :---- |")

		inactiveText := strings.Join(inactiveDomains, " ")
		activeText := ""
		for host, count := range hostCounts {
			if activeText != "" {
				activeText += " "
			}
			activeText += fmt.Sprintf("%s: %d domains", host, count)
		}

		fmt.Fprintf(f, "| %s | %s |\n", inactiveText, activeText)
		fmt.Fprintln(f, "")
	}

	fmt.Fprintf(os.Stderr, "Report written to %s\n", outputFile)
}

// Stub for backup and removal logic
func backupAndRemove(client *auth.SSHClient, domainPath, domain string, dryRun bool) bool {
	sitePath := filepath.Join(domainPath, domain)
	log(1, "Starting backup and remove process for %s", domain)

	// Check if directory exists on remote
	_, _, err := client.ExecuteCommand(fmt.Sprintf("test -d %s", sitePath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Directory not found on remote: %s\n", sitePath)
		return false
	}

	// 1. Find container name from docker-compose.yml on remote
	log(2, "Reading container name from docker-compose.yml for %s", domain)
	composePath := filepath.Join(sitePath, "docker-compose.yml")
	containerName, err := getContainerName(client, composePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting container name: %v\n", err)
		return false
	}
	log(2, "Found container name: %s", containerName)

	// Get database name
	log(2, "Reading database name from docker-compose.yml for %s", domain)
	dbName, err := getDatabaseName(client, composePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting database name: %v\n", err)
		// Continue even if DB name isn't found, as we can still remove files.
	} else {
		log(2, "Found database name: %s", dbName)
	}

	// 2. Export database using Docker on remote
	log(2, "Exporting database for %s", containerName)
	if dryRun {
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would export database from container %s\n", containerName)
	} else {
		if err := exportDatabase(client, containerName); err != nil {
			fmt.Fprintf(os.Stderr, "Error exporting database: %v\n", err)
			// Continue even if DB export fails
		}
	}

	// 3. Create timestamped tarball on remote
	log(2, "Creating backup tarball for %s", domain)
	backupDir := filepath.Join(domainPath, "backup-tarballs")
	timestamp := time.Now().Format("20060102-150405")
	tarballName := fmt.Sprintf("%s-%s.tgz", domain, timestamp)
	tarballPath := filepath.Join(backupDir, tarballName)
	if dryRun {
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would create backup archive: %s\n", tarballPath)
	} else {
		if err := createTarball(client, tarballPath, sitePath, backupDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating tarball: %v\n", err)
			return false
		}
	}

	// 4. Drop the database
	if dbName != "" {
		log(2, "Dropping database %s", dbName)
		if dryRun {
			fmt.Fprintf(os.Stderr, "[DRY RUN] Would drop database %s\n", dbName)
		} else {
			if err := dropDatabase(client, dbName); err != nil {
				// Log error but continue with removal process
				fmt.Fprintf(os.Stderr, "Error dropping database: %v\n", err)
				return false
			}
		}
	}

	// 5. Remove container and site directory on remote
	log(2, "Removing container and site directory for %s", domain)
	if dryRun {
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would remove container %s\n", containerName)
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would remove directory %s\n", sitePath)
	} else {
		if err := removeContainer(client, containerName); err != nil {
			fmt.Fprintf(os.Stderr, "Error removing container: %v\n", err)
			return false
		}
		if _, _, err := client.ExecuteCommand(fmt.Sprintf("rm -rf %s", sitePath)); err != nil {
			fmt.Fprintf(os.Stderr, "Error removing site directory: %v\n", err)
			return false
		}
	}

	if !dryRun {
		fmt.Fprintf(os.Stderr, "Successfully backed up and removed %s\n", domain)
	}
	return true
}

func getContainerName(client *auth.SSHClient, composePath string) (string, error) {
	cmd := fmt.Sprintf("cat %s", composePath)
	stdout, stderr, err := client.ExecuteCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read remote compose file: %w, stderr: %s", err, stderr)
	}

	var compose struct {
		Services map[string]interface{} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(stdout), &compose); err != nil {
		return "", err
	}
	for name := range compose.Services {
		if strings.HasPrefix(name, "wp_") {
			return name, nil
		}
	}
	return "", fmt.Errorf("no wp_ service found in %s", composePath)
}

func getDatabaseName(client *auth.SSHClient, composePath string) (string, error) {
	cmd := fmt.Sprintf("cat %s", composePath)
	stdout, stderr, err := client.ExecuteCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read remote compose file: %w, stderr: %s", err, stderr)
	}

	var compose struct {
		Services map[string]struct {
			Environment []string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(stdout), &compose); err != nil {
		return "", fmt.Errorf("failed to unmarshal compose file: %w", err)
	}

	for _, service := range compose.Services {
		for _, env := range service.Environment {
			if strings.HasPrefix(env, "WORDPRESS_DB_NAME=") {
				return strings.TrimPrefix(env, "WORDPRESS_DB_NAME="), nil
			}
		}
	}

	return "", fmt.Errorf("no WORDPRESS_DB_NAME found in %s", composePath)
}

func exportDatabase(client *auth.SSHClient, containerName string) error {

	cmd := fmt.Sprintf("docker exec %s sh -c 'cd /var/www/html/wp-content && wp db export /dev/stdout --allow-root'", containerName)
	_, stderr, err := client.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("docker exec for db export failed: %w, stderr: %s", err, stderr)
	}

	// Verify that an *.sql file exists in wp-content
	findCmd := fmt.Sprintf("docker exec %s sh -c 'cd /var/www/html/wp-content && find . -maxdepth 1 -name \"*.sql\"'", containerName)
	stdout, stderr, err := client.ExecuteCommand(findCmd)
	if err != nil {
		return fmt.Errorf("failed to verify .sql file in wp-content: %w, stderr: %s", err, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		return fmt.Errorf("no .sql file found in wp-content after export")
	}
	log(2, "SQL export file(s) found: %s", strings.TrimSpace(stdout))
	return nil
}

func createTarball(client *auth.SSHClient, tarballPath, sourcePath, backupDir string) error {
	// Ensure backup directory exists
	mkdirCmd := fmt.Sprintf("mkdir -p %s", backupDir)
	if _, stderr, err := client.ExecuteCommand(mkdirCmd); err != nil {
		return fmt.Errorf("failed to create backup dir on remote: %w, stderr: %s", err, stderr)
	}

	// Create tarball
	sourceDir := filepath.Dir(sourcePath)
	sourceBase := filepath.Base(sourcePath)
	tarCmd := fmt.Sprintf("tar -czf %s -C %s %s", tarballPath, sourceDir, sourceBase)
	_, stderr, err := client.ExecuteCommand(tarCmd)
	if err != nil {
		return fmt.Errorf("failed to create tarball on remote: %w, stderr: %s", err, stderr)
	}
	return nil
}

func dropDatabase(client *auth.SSHClient, dbName string) error {
	rootUser := os.Getenv("MYSQL_ROOT_USER")
	rootPass := os.Getenv("MYSQL_ROOT_PASSWD")

	if rootUser == "" {
		rootUser = "root"
	}
	if rootPass == "" {
		return fmt.Errorf("MYSQL_ROOT_PASSWD environment variable is not set, cannot drop database")
	}
	if dbName == "" {
		return fmt.Errorf("database name is empty, cannot drop")
	}

	// Pass password directly via -p flag
	cmd := fmt.Sprintf("docker exec mysql mysql -u%s -p%s -e 'DROP DATABASE IF EXISTS `%s`'", rootUser, rootPass, dbName)

	_, stderr, err := client.ExecuteCommand(cmd)
	if err != nil {
		if strings.Contains(stderr, "database doesn't exist") {
			log(1, "Database %s does not exist, nothing to drop.", dbName)
			return nil
		}
		return fmt.Errorf("failed to drop database %s: %w, stderr: %s", dbName, err, stderr)
	}
	log(1, "Successfully dropped database %s", dbName)
	return nil
}

func removeContainer(client *auth.SSHClient, containerName string) error {
	cmd := fmt.Sprintf("docker rm -f %s", containerName)
	_, stderr, err := client.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to remove container %s: %w, stderr: %s", containerName, err, stderr)
	}
	return nil
}

func log(level int, format string, a ...any) {
	if verboseCount >= level {
		prefix := ""
		switch level {
		case 1:
			prefix = "[INFO] "
		case 2:
			prefix = "[DEBUG] "
		case 3, 4:
			prefix = "[TRACE] "
		}
		fmt.Fprintf(os.Stderr, prefix+format+"\n", a...)
	}
}

func getLocalHostname() (string, error) {
	return os.Hostname()
}

// getRemoteHostname retrieves the hostname from a remote server via SSH.
func getRemoteHostname(client *auth.SSHClient) (string, error) {
	stdout, stderr, err := client.ExecuteCommand("hostname")
	if err != nil {
		return "", fmt.Errorf("failed to get remote hostname: %w, stderr: %s", err, stderr)
	}
	return strings.TrimSpace(stdout), nil
}

// Local helpers
func getLocalPublicIP() (string, error) {
	cmds := []string{
		"dig +short myip.opendns.com @resolver1.opendns.com",
		"curl -s https://api.ipify.org",
	}
	for _, cmd := range cmds {
		out, err := runLocalCommand(cmd)
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
	}
	return "", fmt.Errorf("could not determine public IP locally")
}

func findDomainsLocal(domainPath string) ([]string, error) {
	log(1, "Scanning for domains in %s locally", domainPath)
	cmd := fmt.Sprintf("find %s -name 'docker-compose.yml'", domainPath)
	out, err := runLocalCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to find domains locally: %w", err)
	}
	var domains []string
	domainRegex := regexp.MustCompile(`(?i)^(www\.)?([a-z0-9-]+(\.[a-z]{2,})+)$`)
	files := strings.SplitSeq(strings.TrimSpace(out), "\n")
	for file := range files {
		if file != "" {
			domain := filepath.Base(filepath.Dir(file))
			domain = strings.TrimPrefix(domain, "www.")
			if !domainRegex.MatchString(domain) {
				log(1, "Skipping invalid domain: %s", domain)
				continue
			}
			if !slices.Contains(domains, domain) {
				domains = append(domains, domain)
			}
		}
	}
	log(1, "Found %d domains to check in %s", len(domains), domainPath)
	return domains, nil
}

func runLocalCommand(cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput()
	return string(out), err
}

// Stub for local backup and removal logic
func backupAndRemoveLocal(domainPath, domain string, dryRun bool) bool {
	sitePath := filepath.Join(domainPath, domain)
	log(1, "[LOCAL] Starting backup and remove process for %s", domain)

	// Check if directory exists locally
	if _, err := os.Stat(sitePath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Directory not found locally: %s\n", sitePath)
		return false
	}

	// 1. Find container name from docker-compose.yml locally
	log(2, "Reading container name from docker-compose.yml for %s", domain)
	composePath := filepath.Join(sitePath, "docker-compose.yml")
	containerName, err := getContainerNameLocal(composePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting container name: %v\n", err)
		return false
	}
	log(2, "Found container name: %s", containerName)

	// Get database name
	log(2, "[LOCAL] Reading database name from docker-compose.yml for %s", domain)
	dbName, err := getDatabaseNameLocal(composePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting database name: %v\n", err)
	} else {
		log(2, "[LOCAL] Found database name: %s", dbName)
	}

	// 2. Export database using Docker locally
	log(2, "Exporting database for %s", containerName)
	if dryRun {
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would export database from container %s\n", containerName)
	} else {
		if err := exportDatabaseLocal(containerName); err != nil {
			fmt.Fprintf(os.Stderr, "Error exporting database: %v\n", err)
			// Continue even if DB export fails
		}
	}

	// 3. Create timestamped tarball locally
	log(2, "Creating backup tarball for %s", domain)
	backupDir := filepath.Join(domainPath, "backup-tarballs")
	timestamp := time.Now().Format("20060102-150405")
	tarballName := fmt.Sprintf("%s-%s.tgz", domain, timestamp)
	tarballPath := filepath.Join(backupDir, tarballName)
	if dryRun {
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would create backup archive: %s\n", tarballPath)
	} else {
		if err := createTarballLocal(tarballPath, sitePath, backupDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating tarball: %v\n", err)
			return false
		}
	}

	// 4. Drop the database
	if dbName != "" {
		log(2, "[LOCAL] Dropping database %s", dbName)
		if dryRun {
			fmt.Fprintf(os.Stderr, "[DRY RUN] Would drop database %s\n", dbName)
		} else {
			if err := dropDatabaseLocal(dbName); err != nil {
				fmt.Fprintf(os.Stderr, "Error dropping database: %v\n", err)
			}
		}
	}

	// 5. Remove container and site directory locally
	log(2, "Removing container and site directory for %s", domain)
	if dryRun {
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would remove container %s\n", containerName)
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would remove directory %s\n", sitePath)
	} else {
		if err := removeContainerLocal(containerName); err != nil {
			fmt.Fprintf(os.Stderr, "Error removing container: %v\n", err)
		}
		if err := os.RemoveAll(sitePath); err != nil {
			fmt.Fprintf(os.Stderr, "Error removing site directory: %v\n", err)
		}
	}

	if !dryRun {
		fmt.Fprintf(os.Stderr, "Successfully backed up and removed %s\n", domain)
	}
	return true
}

// Local helper implementations
func getContainerNameLocal(composePath string) (string, error) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return "", fmt.Errorf("failed to read compose file: %w", err)
	}
	var compose struct {
		Services map[string]interface{} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return "", err
	}
	for name := range compose.Services {
		if strings.HasPrefix(name, "wp_") {
			return name, nil
		}
	}
	return "", fmt.Errorf("no wp_ service found in %s", composePath)
}

func getDatabaseNameLocal(composePath string) (string, error) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return "", fmt.Errorf("failed to read compose file: %w", err)
	}
	var compose struct {
		Services map[string]struct {
			Environment []string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return "", err
	}
	for _, service := range compose.Services {
		for _, env := range service.Environment {
			if strings.HasPrefix(env, "WORDPRESS_DB_NAME=") {
				return strings.TrimPrefix(env, "WORDPRESS_DB_NAME="), nil
			}
		}
	}
	return "", fmt.Errorf("no WORDPRESS_DB_NAME found in %s", composePath)
}

func exportDatabaseLocal(containerName string) error {
	cmd := fmt.Sprintf("docker exec %s sh -c 'cd /var/www/html/wp-content && wp db export --allow-root'", containerName)
	out, err := runLocalCommand(cmd)
	if err != nil {
		return fmt.Errorf("docker exec for db export failed: %w, output: %s", err, out)
	}
	// Verify that an *.sql file exists in wp-content
	findCmd := fmt.Sprintf("docker exec %s sh -c 'cd /var/www/html/wp-content && find . -maxdepth 1 -name \"*.sql\"'", containerName)
	out, err = runLocalCommand(findCmd)
	if err != nil {
		return fmt.Errorf("failed to verify .sql file in wp-content: %w, output: %s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("no .sql file found in wp-content after export")
	}
	log(2, "SQL export file(s) found: %s", strings.TrimSpace(out))
	return nil
}

func createTarballLocal(tarballPath, sourcePath, backupDir string) error {
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("failed to create backup dir locally: %w", err)
	}
	sourceDir := filepath.Dir(sourcePath)
	sourceBase := filepath.Base(sourcePath)
	cmd := fmt.Sprintf("tar -czf %s -C %s %s", tarballPath, sourceDir, sourceBase)
	out, err := runLocalCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to create tarball locally: %w, output: %s", err, out)
	}
	return nil
}

func dropDatabaseLocal(dbName string) error {
	rootUser := os.Getenv("MYSQL_ROOT_USER")
	rootPass := os.Getenv("MYSQL_ROOT_PASSWD")

	if rootUser == "" {
		rootUser = "root"
	}
	if rootPass == "" {
		return fmt.Errorf("MYSQL_ROOT_PASSWD environment variable is not set, cannot drop database")
	}
	if dbName == "" {
		return fmt.Errorf("database name is empty, cannot drop")
	}

	// Pass password directly via -p flag
	cmd := fmt.Sprintf("docker exec mysql mysql -u%s -p%s -e 'DROP DATABASE IF EXISTS `%s`'", rootUser, rootPass, dbName)
	out, err := runLocalCommand(cmd)
	if err != nil {
		if strings.Contains(out, "database doesn't exist") {
			log(1, "[LOCAL] Database %s does not exist, nothing to drop.", dbName)
			return nil
		}
		return fmt.Errorf("failed to drop database %s locally: %w, output: %s", dbName, err, out)
	}
	log(1, "[LOCAL] Successfully dropped database %s", dbName)
	return nil
}

func removeContainerLocal(containerName string) error {
	cmd := fmt.Sprintf("docker rm -f %s", containerName)
	out, err := runLocalCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to remove container %s: %w, output: %s", containerName, err, out)
	}
	return nil
}

// checkCFNSRecords checks if the domain's NS records match either NS1 or NS2 environment variables.
func checkCFNSRecords(domain string) (bool, []string, error) {
	ns1 := os.Getenv("NS1")
	ns2 := os.Getenv("NS2")
	if ns1 == "" && ns2 == "" {
		return false, nil, fmt.Errorf("NS1 and NS2 environment variables are not set")
	}

	cmd := fmt.Sprintf("dig ns +short %s", domain)
	out := exec.Command("sh", "-c", cmd)
	nsRecords, err := out.Output()
	if err != nil {
		return false, nil, fmt.Errorf("failed to run dig for NS records: %w", err)
	}

	nsRecordsStr := strings.TrimSpace(string(nsRecords))
	if nsRecordsStr == "" {
		return false, nil, fmt.Errorf("no NS records found for domain %s", domain)
	}

	nsRecordsSlice := strings.Split(nsRecordsStr, "\n")
	match := false
	for _, ns := range nsRecordsSlice {
		ns = strings.TrimSuffix(ns, ".")
		if ns == ns1 || ns == ns2 {
			match = true
			break
		}
	}

	return match, nsRecordsSlice, nil
}

// Add this new function after the existing generateReport function:

func generateFinalReport(results []string, removedDomains []string, removalOperationPerformed bool) {
	// Parse results into structured data
	hostCounts := make(map[string]int)
	activeCount := 0
	inactiveCount := 0
	var inactiveDomains []string

	for _, line := range results {
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			continue
		}

		domain := parts[0]
		active := parts[1] == "true"
		host := parts[2]

		if active {
			activeCount++
			hostCounts[host]++
		} else {
			inactiveCount++
			inactiveDomains = append(inactiveDomains, domain)
		}
	}

	// Calculate final counts after removal operations
	removedCount := len(removedDomains)
	finalInactiveCount := inactiveCount
	if removalOperationPerformed {
		finalInactiveCount = inactiveCount - removedCount
	}

	// Generate report
	fmt.Println("# WordPress Website Domain Analysis Report")
	fmt.Println()

	// Overall Summary
	fmt.Println("## Overall Summary")
	fmt.Println()
	if removalOperationPerformed && removedCount > 0 {
		fmt.Printf("| %d Websites Active | %d Websites Removed/Inactive |\n", activeCount, removedCount)
		fmt.Println("| :---: | :---: |")
		fmt.Println()
		if finalInactiveCount > 0 {
			fmt.Printf("*Note: %d inactive websites remain (removal failed or skipped)*\n", finalInactiveCount)
			fmt.Println()
		}
	} else {
		fmt.Printf("| %d Websites Active | %d Websites Inactive |\n", activeCount, inactiveCount)
		fmt.Println("| :---: | :---: |")
		fmt.Println()
	}
	fmt.Println("---")
	fmt.Println()

	// Server Inventory Summary (same as existing generateReport)
	if activeCount > 0 {
		fmt.Println("## Server Inventory Summary")
		fmt.Println()

		var tableRows []string
		colCount := 0
		currentRow := ""

		for host, count := range hostCounts {
			switch colCount {
			case 0:
				currentRow = fmt.Sprintf("| %s | %d Domains", host, count)
			case 1:
				currentRow += fmt.Sprintf(" | %s | %d Domains |\n", host, count)
				tableRows = append(tableRows, currentRow)
				currentRow = ""
				colCount = -1
			}
			colCount++
		}

		if currentRow != "" && colCount == 1 {
			currentRow += " |  |  |\n"
			tableRows = append(tableRows, currentRow)
		}

		fmt.Println("| Server | Domain Count | Server | Domain Count |")
		fmt.Println("| :---- | :---- | :---- | :---- |")

		for _, row := range tableRows {
			fmt.Print(row)
		}

		fmt.Println()
		fmt.Println("---")
		fmt.Println()
	}

	// Domain Details
	if removalOperationPerformed && len(removedDomains) > 0 {
		fmt.Println("## Removed Domain Details")
		fmt.Println()
		fmt.Println("| Domains Removed/Inactive | Active Domains by Server |")
		fmt.Println("| :---- | :---- |")

		removedText := strings.Join(removedDomains, " ")
		activeText := ""
		for host, count := range hostCounts {
			if activeText != "" {
				activeText += " "
			}
			activeText += fmt.Sprintf("%s: %d domains", host, count)
		}

		fmt.Printf("| %s | %s |\n", removedText, activeText)
		fmt.Println()

		if finalInactiveCount > 0 {
			remainingInactive := make([]string, 0)
			for _, domain := range inactiveDomains {
				found := false
				for _, removed := range removedDomains {
					if domain == removed {
						found = true
						break
					}
				}
				if !found {
					remainingInactive = append(remainingInactive, domain)
				}
			}
			if len(remainingInactive) > 0 {
				fmt.Println("### Remaining Inactive Domains")
				fmt.Println()
				fmt.Printf("The following %d domains remain inactive and were not removed:\n", len(remainingInactive))
				fmt.Printf("%s\n", strings.Join(remainingInactive, " "))
				fmt.Println()
			}
		}
	} else if len(inactiveDomains) > 0 {
		fmt.Println("## Inactive Domain Details")
		fmt.Println()
		fmt.Println("| Domains Removed/Inactive | Active Domains by Server |")
		fmt.Println("| :---- | :---- |")

		inactiveText := strings.Join(inactiveDomains, " ")
		activeText := ""
		for host, count := range hostCounts {
			if activeText != "" {
				activeText += " "
			}
			activeText += fmt.Sprintf("%s: %d domains", host, count)
		}

		fmt.Printf("| %s | %s |\n", inactiveText, activeText)
		fmt.Println()
	}

	// Write to file if specified (reuse existing writeReportToFile or create similar)
	if outputFile != "" {
		// You can reuse the existing writeReportToFile logic here
		writeReportToFile(activeCount, finalInactiveCount, hostCounts, inactiveDomains)
	}
}

// Add new function to process multiple servers
func processServerRange(cmd *cobra.Command, createSSHClientFunc func(*cobra.Command, string) (*auth.SSHClient, error), serverRange string, excludeList string, remove bool, dryRun bool, report bool) {
	pattern, start, end, err := parseServerRange(serverRange)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing server range: %v\n", err)
		os.Exit(1)
	}

	// Parse exclude list once
	excluded := make(map[string]struct{})
	if excludeList != "" {
		if strings.Contains(excludeList, "|") {
			for d := range strings.SplitSeq(excludeList, "|") {
				excluded[strings.TrimSpace(d)] = struct{}{}
			}
		} else {
			// Assume it's a file
			data, err := os.ReadFile(excludeList)
			if err == nil {
				for _, d := range strings.Split(string(data), "\n") {
					d = strings.TrimSpace(d)
					if d != "" {
						excluded[d] = struct{}{}
					}
				}
			}
		}
	}

	// Accumulate all results from all servers
	var allResults []string
	var allRemovedDomains []string
	totalActiveCount := 0
	totalInactiveCount := 0
	hostCounts := make(map[string]int)

	// Process each server in the range
	for i := start; i <= end; i++ {
		serverHost := fmt.Sprintf(pattern, i)
		log(1, "Processing server: %s", serverHost)

		// Create SSH client for this server
		sshClient, err := createSSHClientFunc(cmd, serverHost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error connecting to %s: %v\n", serverHost, err)
			continue
		}

		// Get server info
		serverIP, err := getPublicIP(sshClient)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting public IP for %s: %v\n", serverHost, err)
			sshClient.Close()
			continue
		}

		hostname, err := getRemoteHostname(sshClient)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting hostname for %s: %v\n", serverHost, err)
			sshClient.Close()
			continue
		}

		// Get domain path
		domainPath := "/var/opt"
		if envPath := os.Getenv("DOMAIN_PATH"); envPath != "" {
			domainPath = envPath
		}

		// Find domains on this server
		domains, err := findDomains(sshClient, domainPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error scanning domains on %s: %v\n", serverHost, err)
			sshClient.Close()
			continue
		}

		// Process domains for this server
		serverActiveCount := 0
		for _, domain := range domains {
			if _, skip := excluded[domain]; skip {
				log(1, "Skipping excluded domain: %s", domain)
				continue
			}

			match, _, err := domainMatchesServer(domain, serverIP)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error checking domain %s on %s: %v\n", domain, serverHost, err)
				continue
			}

			hostDisplay := hostname
			if !strings.HasSuffix(hostname, ".ciwgserver.com") && hostname != "localhost" {
				hostDisplay = hostname + ".ciwgserver.com"
			}

			if match {
				allResults = append(allResults, fmt.Sprintf("%s,true,%s", domain, hostDisplay))
				serverActiveCount++
				totalActiveCount++
			} else {
				allResults = append(allResults, fmt.Sprintf("%s,false,%s", domain, hostDisplay))
				totalInactiveCount++

				// Perform removal if requested
				if remove || dryRun {
					if backupAndRemove(sshClient, domainPath, domain, dryRun) {
						allRemovedDomains = append(allRemovedDomains, domain)
					}
				}
			}
		}

		// Track host counts
		if serverActiveCount > 0 {
			hostDisplay := hostname
			if !strings.HasSuffix(hostname, ".ciwgserver.com") && hostname != "localhost" {
				hostDisplay = hostname + ".ciwgserver.com"
			}
			hostCounts[hostDisplay] = serverActiveCount
		}

		sshClient.Close()
	}

	// Generate consolidated report
	if report {
		generateConsolidatedReport(totalActiveCount, totalInactiveCount, hostCounts, allRemovedDomains, remove || dryRun)
	} else {
		writeResults(allResults)
	}
}

// Add new consolidated report generator
func generateConsolidatedReport(activeCount, inactiveCount int, hostCounts map[string]int, removedDomains []string, removalPerformed bool) {
	removedCount := len(removedDomains)

	// Print to stdout
	fmt.Println("# WordPress Website Pruning Summary")
	fmt.Println()
	fmt.Println("# Overall Summary")
	fmt.Println()

	if removalPerformed && removedCount > 0 {
		fmt.Printf("| %d Websites Removed | %d Websites Remaining |\n", removedCount, activeCount)
	} else {
		fmt.Printf("| %d Websites Active | %d Websites Inactive |\n", activeCount, inactiveCount)
	}
	fmt.Println("| :---: | :---: |")
	fmt.Println()
	fmt.Println("---")
	fmt.Println()

	// Server Inventory Summary
	if len(hostCounts) > 0 {
		fmt.Println("# Server Inventory Summary")
		fmt.Println()

		var serverEntries []string
		// Sort server names for consistent output
		var servers []string
		for server := range hostCounts {
			servers = append(servers, server)
		}
		sort.Strings(servers)

		for _, server := range servers {
			count := hostCounts[server]
			serverEntries = append(serverEntries, fmt.Sprintf("%s | %d Domains", server, count))
		}

		fmt.Printf("| %s |\n", strings.Join(serverEntries, " "))
		fmt.Println("| :---- | :---- |")
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
	}

	// Domain Details
	if removalPerformed && len(removedDomains) > 0 {
		fmt.Println("# Domain Details")
		fmt.Println()
		fmt.Printf("| Domains Removed from WP Servers %s |  |\n", strings.Join(removedDomains, " "))
		fmt.Println("| :---- | :---- |")
	}

	// Write to file if specified
	if outputFile != "" {
		writeConsolidatedReportToFile(activeCount, removedCount, inactiveCount, hostCounts, removedDomains, removalPerformed)
	}
}

// Update the file writer to match
func writeConsolidatedReportToFile(activeCount, removedCount, inactiveCount int, hostCounts map[string]int, removedDomains []string, removalPerformed bool) {
	var f *os.File
	var err error

	// For consolidated reports, we should overwrite by default
	f, err = os.Create(outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening output file: %v\n", err)
		return
	}
	defer f.Close()

	// Write the same format as stdout
	fmt.Fprintln(f, "# WordPress Website Pruning Summary")
	fmt.Fprintln(f, "")
	fmt.Fprintln(f, "# Overall Summary")
	fmt.Fprintln(f, "")

	if removalPerformed && removedCount > 0 {
		fmt.Fprintf(f, "| %d Websites Removed | %d Websites Remaining |\n", removedCount, activeCount)
	} else {
		fmt.Fprintf(f, "| %d Websites Active | %d Websites Inactive |\n", activeCount, inactiveCount)
	}
	fmt.Fprintln(f, "| :---: | :---: |")
	fmt.Fprintln(f, "")

	if len(hostCounts) > 0 {
		fmt.Fprintln(f, "# Server Inventory Summary")
		fmt.Fprintln(f, "")

		var serverEntries []string
		var servers []string
		for server := range hostCounts {
			servers = append(servers, server)
		}
		sort.Strings(servers)

		for _, server := range servers {
			count := hostCounts[server]
			serverEntries = append(serverEntries, fmt.Sprintf("%s | %d Domains", server, count))
		}

		fmt.Fprintf(f, "| %s |\n", strings.Join(serverEntries, " "))
		fmt.Fprintln(f, "| :---- | :---- |")
		fmt.Fprintln(f, "")
		fmt.Fprintln(f, "---")
		fmt.Fprintln(f, "")
	}

	if removalPerformed && len(removedDomains) > 0 {
		fmt.Fprintln(f, "# Domain Details")
		fmt.Fprintln(f, "")
		fmt.Fprintf(f, "| Domains Removed from WP Servers %s |  |\n", strings.Join(removedDomains, " "))
		fmt.Fprintln(f, "| :---- | :---- |")
	}

	fmt.Fprintf(os.Stderr, "Report written to %s\n", outputFile)
}

// Helper function to create SSH client (reuse from domains.go pattern)
