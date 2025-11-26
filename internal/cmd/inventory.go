package cmd

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ciwg-cli/internal/auth"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
)

type ContainerInfo struct {
	Domain  string `json:"domain"`
	Website string `json:"website"`
	Server  string `json:"server"`
	IP      string `json:"ip"`
}

var (
	inventoryOutputFile   string
	inventoryServerRange  string
	inventoryLocal        bool
	inventoryFormat       string
	inventoryFilterSite   string
	inventoryFilterServer string

	// new flag: produce per-server site counts from existing inventory JSON
	inventoryGetQty bool

	// search subcommand flags
	searchPattern       string
	searchAction        string
	searchExec          string
	searchCaseSensitive bool
	searchRegex         bool
	searchMaxDepth      int
	searchGetIPAddr     bool
)

// inferFormatFromFilename infers the output format from the file extension.
// Returns the inferred format or the fallback format if no extension is recognized.
func inferFormatFromFilename(filename, fallback string) string {
	if filename == "" {
		return fallback
	}
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".json":
		return "json"
	case ".csv":
		return "csv"
	default:
		return fallback
	}
}

var inventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "Manage WordPress container inventory",
	Long:  `Generate, search, and manage inventory of WordPress sites with their domains, URLs, and server information.`,
}

var inventoryGenerateCmd = &cobra.Command{
	Use:   "generate [user@]host",
	Short: "Generate inventory of WordPress containers",
	Long:  `Scan Docker containers and generate an inventory of WordPress sites with their domains, URLs, and server information.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runInventoryGenerate,
}

var inventorySearchCmd = &cobra.Command{
	Use:   "search PATTERN",
	Short: "Search for WordPress sites matching a pattern across servers",
	Long: `Search for WordPress site directories matching a pattern across one or more servers.
Supports wildcards, regex, and various output formats.

Examples:
  # Search for sites containing "example"
  ciwg inventory search "example" --server-range="wp%d.ciwgserver.com:0-41"
  
  # Search with regex
  ciwg inventory search ".*\.dev$" --regex --server-range="wp%d.ciwgserver.com:0-10"
  
  # Search and list containers
  ciwg inventory search "acomfort" --server-range="wp%d.ciwgserver.com:0-41" --action="list-containers"
  
  # Search and execute custom command
  ciwg inventory search "mysite" --server-range="wp%d.ciwgserver.com:0-5" --exec="docker ps"
`,
	Args: cobra.ExactArgs(1),
	RunE: runInventorySearch,
}

// var inventoryGenerateCmd = &cobra.Command{
// 	Use:   "generate [user@]host",
// 	Short: "Generate inventory of WordPress containers on specified server(s)",
// 	Args:  cobra.MaximumNArgs(1),
// }

func init() {
	rootCmd.AddCommand(inventoryCmd)
	inventoryCmd.AddCommand(inventoryGenerateCmd)
	inventoryCmd.AddCommand(inventorySearchCmd)

	// Generate command flags
	inventoryGenerateCmd.Flags().StringVarP(&inventoryOutputFile, "output", "o", "inventory.json", "Output file for inventory")
	inventoryGenerateCmd.Flags().StringVar(&inventoryServerRange, "server-range", "", "Server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')")
	inventoryGenerateCmd.Flags().BoolVar(&inventoryLocal, "local", false, "Run locally without SSH")
	inventoryGenerateCmd.Flags().StringVar(&inventoryFormat, "format", "json", "Export format [DEPRECATED: use file extension in --output instead]")
	inventoryGenerateCmd.Flags().MarkDeprecated("format", "use file extension in --output flag instead (e.g., --output=file.csv)")
	inventoryGenerateCmd.Flags().StringVar(&inventoryFilterSite, "filter-by-site", "", "Filter by site list (file path, pipe-delimited string, or stdin)")
	inventoryGenerateCmd.Flags().StringVar(&inventoryFilterServer, "filter-by-server", "", "Filter by server list (file path, pipe-delimited string, or stdin)")
	inventoryGenerateCmd.Flags().BoolVar(&inventoryGetQty, "get-qty", false, "Read inventory JSON and output per-server site counts")

	// SSH connection flags for generate
	inventoryGenerateCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	inventoryGenerateCmd.Flags().StringP("port", "p", "22", "SSH port")
	inventoryGenerateCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	inventoryGenerateCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	inventoryGenerateCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")

	// Search command flags
	inventorySearchCmd.Flags().StringVar(&inventoryServerRange, "server-range", "", "Server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')")
	inventorySearchCmd.Flags().StringVarP(&inventoryOutputFile, "output", "o", "", "Output file for search results")
	inventorySearchCmd.Flags().StringVar(&inventoryFormat, "format", "text", "Output format [DEPRECATED: use file extension in --output instead]")
	inventorySearchCmd.Flags().MarkDeprecated("format", "use file extension in --output flag instead (e.g., --output=file.csv)")
	inventorySearchCmd.Flags().StringVar(&searchAction, "action", "", "Action to perform on matches: list-containers, show-compose, backup")
	inventorySearchCmd.Flags().StringVar(&searchExec, "exec", "", "Custom command to execute on matched servers")
	inventorySearchCmd.Flags().BoolVar(&searchCaseSensitive, "case-sensitive", false, "Case-sensitive pattern matching")
	inventorySearchCmd.Flags().BoolVar(&searchRegex, "regex", false, "Treat pattern as regex")
	inventorySearchCmd.Flags().IntVar(&searchMaxDepth, "max-depth", 1, "Maximum directory depth to search")
	inventorySearchCmd.Flags().BoolVar(&inventoryLocal, "local", false, "Search locally without SSH")
	inventorySearchCmd.Flags().BoolVar(&searchGetIPAddr, "get-ip-addr", false, "Output only IP addresses of servers with matches")

	// SSH connection flags for search
	inventorySearchCmd.Flags().StringP("user", "u", "", "SSH username (default: current user)")
	inventorySearchCmd.Flags().StringP("port", "p", "22", "SSH port")
	inventorySearchCmd.Flags().StringP("key", "k", "", "Path to SSH private key")
	inventorySearchCmd.Flags().BoolP("agent", "a", true, "Use SSH agent")
	inventorySearchCmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

func runInventoryGenerate(cmd *cobra.Command, args []string) error {
	// If --get-qty was requested, read the inventory JSON file and produce the tally.
	if inventoryGetQty {
		type tallyEntry struct {
			Server  string `json:"server"`
			SiteQty int    `json:"siteQty"`
		}

		// Read input JSON (use inventoryOutputFile default or provided)
		inPath := inventoryOutputFile
		if inPath == "" {
			inPath = "inventory.json"
		}
		data, err := os.ReadFile(inPath)
		if err != nil {
			return fmt.Errorf("failed to read inventory file %s: %w", inPath, err)
		}

		var items []ContainerInfo
		if err := json.Unmarshal(data, &items); err != nil {
			return fmt.Errorf("failed to parse inventory JSON: %w", err)
		}

		// Tally by server, normalizing simple names to include the ciwg domain when missing.
		counts := make(map[string]int)
		for _, it := range items {
			srv := strings.TrimSpace(it.Server)
			if srv == "" {
				continue
			}
			// If server doesn't contain a dot, append the default domain for clarity.
			if !strings.Contains(srv, ".") {
				srv = srv + ".ciwgserver.com"
			}
			counts[srv]++
		}

		// Build slice and sort by numeric server index when possible (wpNNN)
		var list []tallyEntry
		for s, q := range counts {
			list = append(list, tallyEntry{Server: s, SiteQty: q})
		}

		// Sort helper: try to extract numeric suffix after "wp" and sort by that, fallback to string compare.
		extractIdx := func(s string) (int, bool) {
			// e.g. wp12.ciwgserver.com or wp12
			if strings.HasPrefix(s, "wp") {
				rest := s[2:]
				// strip after first dot if present
				if idx := strings.IndexByte(rest, '.'); idx >= 0 {
					rest = rest[:idx]
				}
				if n, err := strconv.Atoi(rest); err == nil {
					return n, true
				}
			}
			return 0, false
		}

		sort.Slice(list, func(i, j int) bool {
			ai, aok := extractIdx(list[i].Server)
			bi, bok := extractIdx(list[j].Server)
			if aok && bok {
				return ai < bi
			}
			if aok != bok {
				return aok // numbers come before non-numeric
			}
			return list[i].Server < list[j].Server
		})

		out, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal tally JSON: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	var allInventory []ContainerInfo

	if inventoryServerRange != "" {
		// Process multiple servers
		pattern, start, end, exclusions, err := parseServerRange(inventoryServerRange)
		if err != nil {
			return fmt.Errorf("error parsing server range: %w", err)
		}

		for i := start; i <= end; i++ {
			if exclusions[i] {
				fmt.Printf("Skipping server index: %d\n", i)
				continue
			}
			serverHost := fmt.Sprintf(pattern, i)
			fmt.Printf("Processing server: %s\n", serverHost)

			inventory, err := processServer(cmd, serverHost)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", serverHost, err)
				continue
			}
			allInventory = append(allInventory, inventory...)
		}
	} else if inventoryLocal {
		// Process locally
		fmt.Println("Processing local server...")
		inventory, err := processLocalServer()
		if err != nil {
			return fmt.Errorf("error processing local server: %w", err)
		}
		allInventory = append(allInventory, inventory...)
	} else {
		// Process single remote server
		if len(args) < 1 {
			return fmt.Errorf("remote mode requires [user@]host argument")
		}
		inventory, err := processServer(cmd, args[0])
		if err != nil {
			return fmt.Errorf("error processing server: %w", err)
		}
		allInventory = append(allInventory, inventory...)
	}

	// Apply filters to the collected inventory
	filteredInventory, err := applyFilters(allInventory, inventoryFilterSite, inventoryFilterServer)
	if err != nil {
		return fmt.Errorf("error applying filters: %w", err)
	}

	// Infer format from output file extension (unless --format is explicitly set)
	format := inventoryFormat
	if !cmd.Flags().Changed("format") {
		format = inferFormatFromFilename(inventoryOutputFile, "json")
	}

	// Write results to file based on the selected format
	switch strings.ToLower(format) {
	case "json":
		// Ensure the output file has a .json extension
		if filepath.Ext(inventoryOutputFile) != ".json" {
			inventoryOutputFile = strings.TrimSuffix(inventoryOutputFile, filepath.Ext(inventoryOutputFile)) + ".json"
		}
		jsonData, err := json.MarshalIndent(filteredInventory, "", "  ")
		if err != nil {
			return fmt.Errorf("error marshaling inventory to JSON: %w", err)
		}
		if err := os.WriteFile(inventoryOutputFile, jsonData, 0644); err != nil {
			return fmt.Errorf("error writing inventory file: %w", err)
		}
	case "csv":
		// Ensure the output file has a .csv extension
		if filepath.Ext(inventoryOutputFile) != ".csv" {
			inventoryOutputFile = strings.TrimSuffix(inventoryOutputFile, filepath.Ext(inventoryOutputFile)) + ".csv"
		}
		if err := writeCSVOutput(inventoryOutputFile, filteredInventory); err != nil {
			return fmt.Errorf("error writing CSV output: %w", err)
		}
	default:
		return fmt.Errorf("unsupported format: %s. Please use 'json' or 'csv'", format)
	}

	fmt.Printf("Inventory written to %s\n", inventoryOutputFile)
	fmt.Printf("Total containers found: %d\n", len(filteredInventory))
	return nil
}

func processServer(cmd *cobra.Command, serverHost string) ([]ContainerInfo, error) {
	// Create SSH client
	sshClient, err := createSSHClient(cmd, serverHost)
	if err != nil {
		return nil, fmt.Errorf("error creating SSH client: %w", err)
	}
	defer sshClient.Close()

	// Get server public IP
	serverIP, err := getServerPublicIP(sshClient)
	if err != nil {
		return nil, fmt.Errorf("error getting server IP: %w", err)
	}

	// Get hostname
	hostname, err := getServerHostname(sshClient)
	if err != nil {
		return nil, fmt.Errorf("error getting hostname: %w", err)
	}

	// Get container information via Docker API over SSH
	inventory, err := getContainersViaSSH(sshClient, hostname, serverIP)
	if err != nil {
		return nil, fmt.Errorf("error getting containers: %w", err)
	}

	return inventory, nil
}

func processLocalServer() ([]ContainerInfo, error) {
	// Get local public IP
	serverIP, err := getLocalServerIP()
	if err != nil {
		return nil, fmt.Errorf("error getting local IP: %w", err)
	}

	// Get local hostname
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("error getting hostname: %w", err)
	}

	// Get container information via local Docker API
	inventory, err := getContainersLocal(hostname, serverIP)
	if err != nil {
		return nil, fmt.Errorf("error getting containers: %w", err)
	}

	return inventory, nil
}

func getServerPublicIP(client *auth.SSHClient) (string, error) {
	cmd := "dig +short myip.opendns.com @resolver1.opendns.com"
	stdout, stderr, err := client.ExecuteCommand(cmd)
	if err != nil && len(stderr) > 0 {
		// Fallback to curl
		cmd = "curl -s https://api.ipify.org"
		stdout, stderr, err = client.ExecuteCommand(cmd)
		if err != nil {
			return "", fmt.Errorf("failed to get public IP: %w, stderr: %s", err, stderr)
		}
	}
	return strings.TrimSpace(stdout), nil
}

func getServerHostname(client *auth.SSHClient) (string, error) {
	stdout, stderr, err := client.ExecuteCommand("hostname")
	if err != nil {
		return "", fmt.Errorf("failed to get hostname: %w, stderr: %s", err, stderr)
	}
	return strings.TrimSpace(stdout), nil
}

func getLocalServerIP() (string, error) {
	// Try multiple methods to get public IP
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
	return "", fmt.Errorf("could not determine public IP")
}

// normalizeURL strips scheme, 'www.' prefix, and trailing slashes for fuzzy matching.
func normalizeURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimRight(s, "/")
	return s
}

func applyFilters(inventory []ContainerInfo, siteFilter, serverFilter string) ([]ContainerInfo, error) {
	siteFilterList, err := parseFilterList(siteFilter, true) // Normalize sites
	if err != nil {
		return nil, fmt.Errorf("failed to parse site filter: %w", err)
	}
	serverFilterList, err := parseFilterList(serverFilter, false) // Do not normalize servers
	if err != nil {
		return nil, fmt.Errorf("failed to parse server filter: %w", err)
	}

	if len(siteFilterList) == 0 && len(serverFilterList) == 0 {
		return inventory, nil
	}

	var filtered []ContainerInfo
	for _, item := range inventory {
		// Normalize the container's WP_HOME value for comparison
		normalizedWebsite := normalizeURL(item.Website)
		siteMatch := len(siteFilterList) == 0 || siteFilterList[normalizedWebsite]

		serverMatch := len(serverFilterList) == 0 || serverFilterList[item.Server]

		if siteMatch && serverMatch {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func parseFilterList(filter string, normalize bool) (map[string]bool, error) {
	list := make(map[string]bool)
	if filter == "" {
		return list, nil
	}

	var lines []string
	// Check for stdin, file path, or pipe-delimited string
	if filter == "-" {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading from stdin: %w", err)
		}
	} else if _, err := os.Stat(filter); err == nil {
		content, err := os.ReadFile(filter)
		if err != nil {
			return nil, fmt.Errorf("error reading filter file: %w", err)
		}
		lines = strings.Split(string(content), "\n")
	} else {
		lines = strings.Split(filter, "|")
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if normalize {
			list[normalizeURL(trimmed)] = true
		} else {
			list[trimmed] = true
		}
	}
	return list, nil
}

func writeCSVOutput(filePath string, inventory []ContainerInfo) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	header := []string{"Domain", "Website", "Server", "IP"}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write data rows
	for _, item := range inventory {
		record := []string{item.Domain, item.Website, item.Server, item.IP}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("failed to write CSV record for domain %s: %w", item.Domain, err)
		}
	}
	return nil
}

func getContainersViaSSH(sshClient *auth.SSHClient, hostname, serverIP string) ([]ContainerInfo, error) {
	var inventory []ContainerInfo

	// Use docker CLI via SSH since Docker SDK over SSH is complex
	// First, get all wp_ containers
	stdout, stderr, err := sshClient.ExecuteCommand("docker ps --format='{{.Names}}' | grep '^wp_'")
	if err != nil {
		if strings.Contains(stderr, "No such container") || stdout == "" {
			// No containers found
			return inventory, nil
		}
		return nil, fmt.Errorf("failed to list containers: %w, stderr: %s", err, stderr)
	}

	containers := strings.Split(strings.TrimSpace(stdout), "\n")

	for _, containerName := range containers {
		if containerName == "" {
			continue
		}

		// Get container inspect data
		inspectCmd := fmt.Sprintf("docker inspect %s", containerName)
		inspectOut, _, err := sshClient.ExecuteCommand(inspectCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting container %s: %v\n", containerName, err)
			continue
		}

		fmt.Printf("Inspect output for %s:\n%s\n", containerName, inspectOut)

		// Parse WP_HOME from environment variables
		wpHome := ""
		wpHomeCmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Env | map(select(contains("WP_HOME="))) | .[0] | split("=")[1]'`, containerName)
		wpHomeOut, _, err := sshClient.ExecuteCommand(wpHomeCmd)
		if err == nil {
			wpHome = strings.TrimSpace(wpHomeOut)
		}

		// Get working directory (domain)
		domainCmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"'`, containerName)
		domainOut, _, err := sshClient.ExecuteCommand(domainCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting domain for container %s: %v\n", containerName, err)
			continue
		}

		workingDir := strings.TrimSpace(domainOut)
		domain := filepath.Base(workingDir)

		// Create container info
		info := ContainerInfo{
			Domain:  domain,
			Website: wpHome,
			Server:  hostname,
			IP:      serverIP,
		}

		inventory = append(inventory, info)
	}

	return inventory, nil
}

func getContainersLocal(hostname, serverIP string) ([]ContainerInfo, error) {
	var inventory []ContainerInfo

	// Create Docker client
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	// List containers
	containers, err := cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	for _, container := range containers {
		// Check if container name starts with wp_
		containerName := ""
		for _, name := range container.Names {
			name = strings.TrimPrefix(name, "/")
			if strings.HasPrefix(name, "wp_") {
				containerName = name
				break
			}
		}

		if containerName == "" {
			continue
		}

		// Inspect container
		inspect, err := cli.ContainerInspect(ctx, container.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error inspecting container %s: %v\n", containerName, err)
			continue
		}

		// Find WP_HOME in environment variables
		wpHome := ""
		for _, env := range inspect.Config.Env {
			if strings.HasPrefix(env, "WP_HOME=") {
				wpHome = strings.TrimPrefix(env, "WP_HOME=")
				break
			}
		}

		// Get working directory from labels
		workingDir := inspect.Config.Labels["com.docker.compose.project.working_dir"]
		domain := filepath.Base(workingDir)

		// Create container info
		info := ContainerInfo{
			Domain:  domain,
			Website: wpHome,
			Server:  hostname,
			IP:      serverIP,
		}

		inventory = append(inventory, info)
	}

	return inventory, nil
}

// SearchResult represents a search match
type SearchResult struct {
	Server   string   `json:"server"`
	Hostname string   `json:"hostname"`
	IP       string   `json:"ip"`
	Matches  []string `json:"matches"`
	Error    string   `json:"error,omitempty"`
}

func runInventorySearch(cmd *cobra.Command, args []string) error {
	pattern := args[0]

	if inventoryServerRange == "" && !inventoryLocal {
		return fmt.Errorf("either --server-range or --local is required")
	}

	var results []SearchResult

	if inventoryLocal {
		// Search locally
		result := searchLocalServer(pattern)
		results = append(results, result)
	} else {
		// Search across server range
		var err error
		results, err = searchServers(cmd, pattern, inventoryServerRange)
		if err != nil {
			return fmt.Errorf("search failed: %w", err)
		}
	}

	// If --get-ip-addr flag is set, output only IP addresses
	if searchGetIPAddr {
		return outputIPAddresses(results, inventoryOutputFile)
	}

	// Execute actions if specified
	if searchAction != "" || searchExec != "" {
		for _, result := range results {
			if len(result.Matches) > 0 && result.Error == "" {
				if err := executeSearchAction(cmd, result, searchAction, searchExec); err != nil {
					fmt.Fprintf(os.Stderr, "Error executing action on %s: %v\n", result.Server, err)
				}
			}
		}
	}

	// Infer format from output file extension (unless --format is explicitly set)
	format := inventoryFormat
	if !cmd.Flags().Changed("format") {
		format = inferFormatFromFilename(inventoryOutputFile, "text")
	}

	// Output results
	return outputSearchResults(results, format, inventoryOutputFile)
}

func searchServers(cmd *cobra.Command, pattern, serverRange string) ([]SearchResult, error) {
	patternParsed, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return nil, fmt.Errorf("invalid server range: %w", err)
	}

	var results []SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Limit concurrent searches
	semaphore := make(chan struct{}, 10)

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(patternParsed, i)

		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result := searchRemoteServer(cmd, host, pattern)

			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}(hostname)
	}

	wg.Wait()

	// Sort results by server name
	sort.Slice(results, func(i, j int) bool {
		return results[i].Server < results[j].Server
	})

	return results, nil
}

func searchRemoteServer(cmd *cobra.Command, hostname, pattern string) SearchResult {
	result := SearchResult{
		Server:  hostname,
		Matches: []string{},
	}

	// Create SSH client
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		result.Error = fmt.Sprintf("SSH connection failed: %v", err)
		return result
	}
	defer sshClient.Close()

	// Get server info
	serverIP, err := getServerPublicIP(sshClient)
	if err == nil {
		result.IP = serverIP
	}

	hostnameOut, err := getServerHostname(sshClient)
	if err == nil {
		result.Hostname = hostnameOut
	}

	// Build find command
	searchPath := "/var/opt/sites"
	findCmd := buildFindCommand(searchPath, pattern, searchMaxDepth, searchCaseSensitive, searchRegex)

	// Execute search
	stdout, stderr, err := sshClient.ExecuteCommand(findCmd)
	if err != nil {
		// Check if it's just "no matches found" vs actual error
		if !strings.Contains(stderr, "No such file") && stderr != "" {
			result.Error = fmt.Sprintf("Search failed: %v", err)
		}
		return result
	}

	// Parse matches
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result.Matches = append(result.Matches, line)
		}
	}

	return result
}

func searchLocalServer(pattern string) SearchResult {
	result := SearchResult{
		Server:  "localhost",
		Matches: []string{},
	}

	// Get local hostname
	hostname, err := os.Hostname()
	if err == nil {
		result.Hostname = hostname
	}

	// Get local IP
	ip, err := getLocalServerIP()
	if err == nil {
		result.IP = ip
	}

	// Build find command
	searchPath := "/var/opt/sites"
	findCmd := buildFindCommand(searchPath, pattern, searchMaxDepth, searchCaseSensitive, searchRegex)

	// Execute locally
	out, err := runLocalCommand(findCmd)
	if err != nil {
		result.Error = fmt.Sprintf("Search failed: %v", err)
		return result
	}

	// Parse matches
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result.Matches = append(result.Matches, line)
		}
	}

	return result
}

func buildFindCommand(searchPath, pattern string, maxDepth int, caseSensitive, regex bool) string {
	var cmd strings.Builder

	cmd.WriteString(fmt.Sprintf("find %s -maxdepth %d -type d", searchPath, maxDepth))

	if regex {
		// Use -regex for pattern matching
		if caseSensitive {
			cmd.WriteString(fmt.Sprintf(" -regex '.*/%s'", pattern))
		} else {
			cmd.WriteString(fmt.Sprintf(" -iregex '.*/%s'", pattern))
		}
	} else {
		// Use -name for wildcard matching
		if caseSensitive {
			cmd.WriteString(fmt.Sprintf(" -name '*%s*'", pattern))
		} else {
			cmd.WriteString(fmt.Sprintf(" -iname '*%s*'", pattern))
		}
	}

	cmd.WriteString(" -print 2>/dev/null")

	return cmd.String()
}

func executeSearchAction(cmd *cobra.Command, result SearchResult, action, execCmd string) error {
	// Create SSH client
	sshClient, err := createSSHClient(cmd, result.Server)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer sshClient.Close()

	fmt.Printf("\n=== Executing action on %s ===\n", result.Server)

	// Execute custom command if specified
	if execCmd != "" {
		fmt.Printf("Running: %s\n", execCmd)
		stdout, stderr, err := sshClient.ExecuteCommand(execCmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\nStderr: %s\n", err, stderr)
		} else {
			fmt.Println(stdout)
		}
		return nil
	}

	// Execute predefined actions
	switch action {
	case "list-containers":
		for _, match := range result.Matches {
			domain := filepath.Base(match)
			containerName := fmt.Sprintf("wp_%s", strings.ReplaceAll(domain, ".", "_"))

			fmt.Printf("\nSite: %s\n", domain)
			fmt.Printf("Path: %s\n", match)

			// Check if container exists and is running
			checkCmd := fmt.Sprintf("docker ps -a --filter name=%s --format '{{.Names}}\t{{.Status}}'", containerName)
			stdout, _, err := sshClient.ExecuteCommand(checkCmd)
			if err == nil && strings.TrimSpace(stdout) != "" {
				fmt.Printf("Container: %s\n", strings.TrimSpace(stdout))
			} else {
				fmt.Printf("Container: Not found or not running\n")
			}
		}

	case "show-compose":
		for _, match := range result.Matches {
			composePath := filepath.Join(match, "docker-compose.yml")
			fmt.Printf("\n=== %s ===\n", composePath)

			catCmd := fmt.Sprintf("cat %s 2>/dev/null", composePath)
			stdout, _, err := sshClient.ExecuteCommand(catCmd)
			if err == nil {
				fmt.Println(stdout)
			} else {
				fmt.Printf("Error reading docker-compose.yml\n")
			}
		}

	case "backup":
		fmt.Println("Backup action not yet implemented")
		// TODO: Integrate with existing backup functionality

	default:
		return fmt.Errorf("unknown action: %s", action)
	}

	return nil
}

func outputIPAddresses(results []SearchResult, outputFile string) error {
	var ips []string
	seen := make(map[string]bool)

	for _, result := range results {
		// Only include servers with matches and valid IPs
		if len(result.Matches) > 0 && result.Error == "" && result.IP != "" {
			if !seen[result.IP] {
				ips = append(ips, result.IP)
				seen[result.IP] = true
			}
		}
	}

	output := strings.Join(ips, "\n")
	if output != "" {
		output += "\n"
	}

	if outputFile != "" {
		return os.WriteFile(outputFile, []byte(output), 0644)
	}

	fmt.Print(output)
	return nil
}

func outputSearchResults(results []SearchResult, format, outputFile string) error {
	switch strings.ToLower(format) {
	case "json":
		return outputSearchJSON(results, outputFile)
	case "csv":
		return outputSearchCSV(results, outputFile)
	default:
		return outputSearchText(results, outputFile)
	}
}

func outputSearchText(results []SearchResult, outputFile string) error {
	var output strings.Builder

	totalMatches := 0
	serversWithMatches := 0

	for _, result := range results {
		if len(result.Matches) > 0 {
			serversWithMatches++
			totalMatches += len(result.Matches)

			output.WriteString(fmt.Sprintf("\n✓ %s", result.Server))
			if result.Hostname != "" {
				output.WriteString(fmt.Sprintf(" [%s]", result.Hostname))
			}
			if result.IP != "" {
				output.WriteString(fmt.Sprintf(" (%s)", result.IP))
			}
			output.WriteString(fmt.Sprintf(" - %d match(es)\n", len(result.Matches)))

			for _, match := range result.Matches {
				output.WriteString(fmt.Sprintf("  %s\n", match))
			}
		} else if result.Error != "" {
			output.WriteString(fmt.Sprintf("\n✗ %s - %s\n", result.Server, result.Error))
		}
	}

	output.WriteString(fmt.Sprintf("\n\nSummary: %d match(es) found on %d server(s) out of %d checked\n",
		totalMatches, serversWithMatches, len(results)))

	if outputFile != "" {
		return os.WriteFile(outputFile, []byte(output.String()), 0644)
	}

	fmt.Print(output.String())
	return nil
}

func outputSearchJSON(results []SearchResult, outputFile string) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	if outputFile != "" {
		return os.WriteFile(outputFile, data, 0644)
	}

	fmt.Println(string(data))
	return nil
}

func outputSearchCSV(results []SearchResult, outputFile string) error {
	var records [][]string
	records = append(records, []string{"Server", "Hostname", "IP", "Match", "Error"})

	for _, result := range results {
		if len(result.Matches) > 0 {
			for _, match := range result.Matches {
				records = append(records, []string{
					result.Server,
					result.Hostname,
					result.IP,
					match,
					"",
				})
			}
		} else {
			records = append(records, []string{
				result.Server,
				result.Hostname,
				result.IP,
				"",
				result.Error,
			})
		}
	}

	var f *os.File
	var err error

	if outputFile != "" {
		f, err = os.Create(outputFile)
		if err != nil {
			return fmt.Errorf("failed to create CSV file: %w", err)
		}
		defer f.Close()
	} else {
		f = os.Stdout
	}

	writer := csv.NewWriter(f)
	defer writer.Flush()

	return writer.WriteAll(records)
}

// Parse server range pattern (e.g., 'wp%d.ciwgserver.com:0-41')
