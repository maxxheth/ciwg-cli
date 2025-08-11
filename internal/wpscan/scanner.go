package wpscan

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ciwg-cli/internal/auth"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type Scanner struct {
	dockerClient *client.Client
	apiClient    *APIClient
	config       Config
}

type Config struct {
	UseSSH        bool
	SSHHost       string
	SSHUser       string
	SSHPort       string
	SSHKey        string
	SSHUseAgent   bool
	SSHTimeout    time.Duration
	UseCSV        bool
	CSVFile       string
	ServerRange   string
	Local         bool
	APIKeysCSV    string
	APIKeysColumn string
}

type SiteInfo struct {
	Domain    string `json:"domain" csv:"Domain"`
	Website   string `json:"website" csv:"Website"`
	Server    string `json:"server" csv:"Server"`
	IP        string `json:"ip" csv:"IP"`
	Container string `json:"container,omitempty"`
}

type WordPressAsset struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

type ContainerAssets struct {
	ContainerName string           `json:"container_name"`
	SiteInfo      SiteInfo         `json:"site_info"`
	Plugins       []WordPressAsset `json:"plugins"`
	Themes        []WordPressAsset `json:"themes"`
}

type ScanResults struct {
	Timestamp string                     `json:"timestamp"`
	Sites     []SiteInfo                 `json:"sites"`
	Plugins   map[string]*PluginVulnInfo `json:"plugins"`
	Themes    map[string]*ThemeVulnInfo  `json:"themes"`
	Errors    []string                   `json:"errors,omitempty"`
	Stats     ScanStats                  `json:"stats"`
}

type ScanStats struct {
	TotalSites            int `json:"total_sites"`
	SitesScanned          int `json:"sites_scanned"`
	UniquePlugins         int `json:"unique_plugins"`
	UniqueThemes          int `json:"unique_themes"`
	PluginVulnerabilities int `json:"plugin_vulnerabilities"`
	ThemeVulnerabilities  int `json:"theme_vulnerabilities"`
}

func NewScanner(config Config) (*Scanner, error) {
	s := &Scanner{
		config: config,
	}

	// Only initialize Docker client for local operations
	if !config.UseCSV && config.Local {
		dockerClient, err := client.NewClientWithOpts(
			client.FromEnv,
			client.WithAPIVersionNegotiation(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create Docker client: %w", err)
		}
		s.dockerClient = dockerClient
	}

	// Initialize API client with CSV support
	apiConfig := APIClientConfig{
		CSVFile:   config.APIKeysCSV,
		CSVColumn: config.APIKeysColumn,
	}

	apiClient, err := NewAPIClientWithConfig(apiConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize API client: %w", err)
	}
	s.apiClient = apiClient

	return s, nil
}

// Create SSH client function following domains.go pattern
func (s *Scanner) createSSHClient(serverHost string) (*auth.SSHClient, error) {
	sshConfig := auth.SSHConfig{
		Hostname: serverHost,
		Username: s.config.SSHUser,
		Port:     s.config.SSHPort,
		KeyPath:  s.config.SSHKey,
		UseAgent: s.config.SSHUseAgent,
		Timeout:  s.config.SSHTimeout,
	}

	return auth.NewSSHClient(sshConfig)
}

// Simplify CollectAssetsFromSites to follow domains.go approach
func (s *Scanner) CollectAssetsFromSites(ctx context.Context, sites []SiteInfo) (map[string]bool, map[string]bool, error) {
	plugins := make(map[string]bool)
	themes := make(map[string]bool)

	var mu sync.Mutex
	errors := make([]error, 0)

	// Group sites by server to reduce connection attempts (but don't pool connections)
	serverSites := make(map[string][]SiteInfo)
	for _, site := range sites {
		server := site.Server
		if server == "" {
			server = "localhost"
		}
		serverSites[server] = append(serverSites[server], site)
	}

	log.Printf("Processing %d sites across %d servers", len(sites), len(serverSites))

	// Process servers sequentially to avoid overwhelming SSH connections
	// This follows the same approach as domains.go processServerRange
	for server, sites := range serverSites {
		log.Printf("Processing %d sites on server %s", len(sites), server)

		// Create one SSH client per server and process all sites on that server
		// before moving to the next server (similar to domains.go approach)
		if server != "localhost" {
			sshClient, err := s.createSSHClient(server)
			if err != nil {
				log.Printf("Failed to connect to %s: %v", server, err)
				continue
			}

			// Process all sites on this server sequentially
			for _, site := range sites {
				assets, err := s.extractAssetsFromSiteWithClient(ctx, site, sshClient)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("site %s: %w", site.Domain, err))
					mu.Unlock()
					continue
				}

				mu.Lock()
				for _, plugin := range assets.Plugins {
					plugins[plugin.Name] = true
				}
				for _, theme := range assets.Themes {
					themes[theme.Name] = true
				}
				mu.Unlock()
			}

			sshClient.Close()
		} else {
			// Process local sites
			for _, site := range sites {
				assets, err := s.extractAssetsFromSite(ctx, site)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("site %s: %w", site.Domain, err))
					mu.Unlock()
					continue
				}

				mu.Lock()
				for _, plugin := range assets.Plugins {
					plugins[plugin.Name] = true
				}
				for _, theme := range assets.Themes {
					themes[theme.Name] = true
				}
				mu.Unlock()
			}
		}
	}

	if len(errors) > 0 {
		log.Printf("Encountered %d errors while collecting assets", len(errors))
		for _, err := range errors {
			log.Printf("  - %v", err)
		}
	}

	return plugins, themes, nil
}

// New function that takes an existing SSH client
func (s *Scanner) extractAssetsFromSiteWithClient(ctx context.Context, site SiteInfo, sshClient *auth.SSHClient) (*ContainerAssets, error) {
	containerName := site.Container
	if containerName == "" {
		containerName = "wp_" + strings.Replace(site.Domain, ".", "_", -1)
	}

	// Command to extract plugins and themes as JSON
	wpCmd := `wp plugin list --format=json 2>/dev/null && echo "---SEPARATOR---" && wp theme list --format=json 2>/dev/null`

	// Execute docker exec via SSH - same pattern as domains.go
	dockerCmd := fmt.Sprintf("docker exec %s bash -c '%s'", containerName, wpCmd)
	stdout, stderr, err := sshClient.ExecuteCommand(dockerCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to execute WP-CLI command: %w (stderr: %s)", err, stderr)
	}

	// Use ctx to handle cancellation and timeouts
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return s.parseAssetsOutput(stdout, containerName, site)
}

// Keep the original function for local/single operations
func (s *Scanner) extractAssetsFromSite(ctx context.Context, site SiteInfo) (*ContainerAssets, error) {
	containerName := site.Container
	if containerName == "" {
		containerName = "wp_" + strings.Replace(site.Domain, ".", "_", -1)
	}

	wpCmd := `wp plugin list --format=json 2>/dev/null && echo "---SEPARATOR---" && wp theme list --format=json 2>/dev/null`
	var output string

	if site.Server != "" && site.Server != "localhost" {
		// Create SSH client for single operation (following domains.go pattern)
		sshClient, err := s.createSSHClient(site.Server)
		if err != nil {
			return nil, fmt.Errorf("failed to create SSH client: %w", err)
		}
		defer sshClient.Close()

		dockerCmd := fmt.Sprintf("docker exec %s bash -c '%s'", containerName, wpCmd)
		stdout, stderr, err := sshClient.ExecuteCommand(dockerCmd)
		if err != nil {
			return nil, fmt.Errorf("failed to execute WP-CLI command: %w (stderr: %s)", err, stderr)
		}
		output = stdout
	} else {
		// Execute locally using Docker API
		if s.dockerClient == nil {
			return nil, fmt.Errorf("docker client not available for local execution")
		}

		execConfig := container.ExecOptions{
			AttachStdout: true,
			AttachStderr: true,
			Cmd:          []string{"bash", "-c", wpCmd},
		}

		execID, err := s.dockerClient.ContainerExecCreate(ctx, containerName, execConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create exec: %w", err)
		}

		resp, err := s.dockerClient.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to attach exec: %w", err)
		}
		defer resp.Close()

		outputBytes, err := io.ReadAll(resp.Reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read output: %w", err)
		}
		output = string(outputBytes)
	}

	return s.parseAssetsOutput(output, containerName, site)
}

// Extract parsing logic to separate function
func (s *Scanner) parseAssetsOutput(output, containerName string, site SiteInfo) (*ContainerAssets, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, fmt.Errorf("no output from WP-CLI command for %s", site.Domain)
	}

	parts := strings.Split(output, "---SEPARATOR---")
	if len(parts) != 2 {
		return nil, fmt.Errorf("unexpected output format from %s", site.Domain)
	}

	assets := &ContainerAssets{
		ContainerName: containerName,
		SiteInfo:      site,
	}

	// Parse plugins
	pluginJSON := strings.TrimSpace(parts[0])
	if pluginJSON != "" && pluginJSON != "null" {
		if err := json.Unmarshal([]byte(pluginJSON), &assets.Plugins); err != nil {
			log.Printf("Failed to parse plugins for %s: %v", site.Domain, err)
		}
	}

	// Parse themes
	themeJSON := strings.TrimSpace(parts[1])
	if themeJSON != "" && themeJSON != "null" {
		if err := json.Unmarshal([]byte(themeJSON), &assets.Themes); err != nil {
			log.Printf("Failed to parse themes for %s: %v", site.Domain, err)
		}
	}

	return assets, nil
}

func (s *Scanner) LoadSitesFromFile(filename string) ([]SiteInfo, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".json":
		return s.loadSitesFromJSON(file)
	case ".csv":
		return s.loadSitesFromCSV(file)
	default:
		return nil, fmt.Errorf("unsupported file format: %s", ext)
	}
}

func (s *Scanner) loadSitesFromJSON(r io.Reader) ([]SiteInfo, error) {
	var sites []SiteInfo
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&sites); err != nil {
		return nil, fmt.Errorf("failed to decode JSON: %w", err)
	}
	return sites, nil
}

func (s *Scanner) loadSitesFromCSV(r io.Reader) ([]SiteInfo, error) {
	reader := csv.NewReader(r)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV: %w", err)
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("CSV file is empty")
	}

	// Skip header row
	var sites []SiteInfo
	for i, record := range records[1:] {
		if len(record) < 4 {
			log.Printf("Warning: CSV row %d has insufficient columns, skipping", i+2)
			continue
		}

		sites = append(sites, SiteInfo{
			Domain:  record[0],
			Website: record[1],
			Server:  record[2],
			IP:      record[3],
		})
	}

	return sites, nil
}

func (s *Scanner) GetWordPressContainers(ctx context.Context) ([]string, error) {
	// Handle server range if specified
	if s.config.ServerRange != "" {
		return s.getContainersFromServerRange(ctx)
	}

	// Handle single server or local
	if s.config.Local {
		return s.getContainersLocal(ctx)
	}

	// Single remote server
	return s.getContainersFromSingleServer(ctx, s.config.SSHHost)
}

func (s *Scanner) getContainersLocal(ctx context.Context) ([]string, error) {
	if s.dockerClient == nil {
		return nil, fmt.Errorf("docker client not initialized for local operations")
	}

	containers, err := s.dockerClient.ContainerList(ctx, container.ListOptions{
		All: false, // Only running containers
	})
	if err != nil {
		return nil, err
	}

	var wpContainers []string
	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			if strings.HasPrefix(name, "wp_") {
				wpContainers = append(wpContainers, name)
				break
			}
		}
	}

	return wpContainers, nil
}

func (s *Scanner) getContainersFromSingleServer(ctx context.Context, serverHost string) ([]string, error) {
	sshClient, err := s.createSSHClient(serverHost)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", serverHost, err)
	}
	defer sshClient.Close()

	// Get containers via SSH - same pattern as domains.go
	stdout, stderr, err := sshClient.ExecuteCommand("docker ps --format '{{.Names}}' | grep '^wp_'")
	if err != nil {
		if strings.Contains(stderr, "No such container") || stdout == "" {
			return []string{}, nil // No containers found
		}
		return nil, fmt.Errorf("failed to list containers: %w, stderr: %s", err, stderr)
	}

	var wpContainers []string
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && strings.HasPrefix(line, "wp_") {
			wpContainers = append(wpContainers, fmt.Sprintf("%s@%s", line, serverHost))
		}
	}

	// Use ctx to handle cancellation and timeouts
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return wpContainers, nil
}

func (s *Scanner) getContainersFromServerRange(ctx context.Context) ([]string, error) {
	// Parse server range similar to domains.go and inventory.go
	pattern, start, end, err := parseServerRange(s.config.ServerRange)
	if err != nil {
		return nil, fmt.Errorf("error parsing server range: %w", err)
	}

	var allContainers []string

	// Process servers sequentially like domains.go (not concurrently to avoid SSH issues)
	for i := start; i <= end; i++ {
		serverHost := fmt.Sprintf(pattern, i)
		containers, err := s.getContainersFromSingleServer(ctx, serverHost)
		if err != nil {
			log.Printf("Failed to get containers from %s: %v", serverHost, err)
			continue
		}
		allContainers = append(allContainers, containers...)
	}

	if len(allContainers) == 0 {
		return nil, fmt.Errorf("no WordPress containers found in the specified range")
	}

	log.Printf("Found %d WordPress containers across servers in range %s", len(allContainers), s.config.ServerRange)
	return allContainers, nil
}

func (s *Scanner) ContainersToSites(containers []string) ([]SiteInfo, error) {
	var sites []SiteInfo

	for _, containerRef := range containers {
		var containerName, serverHost string

		if strings.Contains(containerRef, "@") {
			parts := strings.Split(containerRef, "@")
			containerName = parts[0]
			serverHost = parts[1]
		} else {
			containerName = containerRef
			serverHost = "localhost"
		}

		// Extract domain from container name (wp_domain_tld -> domain.tld)
		domain := strings.TrimPrefix(containerName, "wp_")
		domain = strings.Replace(domain, "_", ".", -1)

		sites = append(sites, SiteInfo{
			Domain:    domain,
			Website:   fmt.Sprintf("https://%s", domain),
			Server:    serverHost,
			Container: containerName,
		})
	}

	return sites, nil
}

func (s *Scanner) ScanAssets(ctx context.Context, plugins, themes map[string]bool) (*ScanResults, error) {
	results := &ScanResults{
		Timestamp: time.Now().Format(time.RFC3339),
		Plugins:   make(map[string]*PluginVulnInfo),
		Themes:    make(map[string]*ThemeVulnInfo),
		Errors:    []string{},
		Stats: ScanStats{
			UniquePlugins: len(plugins),
			UniqueThemes:  len(themes),
		},
	}

	// Scan plugins
	log.Printf("Scanning %d plugins for vulnerabilities...", len(plugins))
	for plugin := range plugins {
		info, err := s.apiClient.GetPluginVulnerabilities(ctx, plugin)
		if err != nil {
			results.Errors = append(results.Errors, fmt.Sprintf("plugin %s: %v", plugin, err))
			continue
		}
		results.Plugins[plugin] = info
		results.Stats.PluginVulnerabilities += len(info.Vulnerabilities)
	}

	// Scan themes
	log.Printf("Scanning %d themes for vulnerabilities...", len(themes))
	for theme := range themes {
		info, err := s.apiClient.GetThemeVulnerabilities(ctx, theme)
		if err != nil {
			results.Errors = append(results.Errors, fmt.Sprintf("theme %s: %v", theme, err))
			continue
		}
		results.Themes[theme] = info
		results.Stats.ThemeVulnerabilities += len(info.Vulnerabilities)
	}

	return results, nil
}

func (s *Scanner) Close() error {
	if s.dockerClient != nil {
		s.dockerClient.Close()
	}
	return nil
}

// Helper function to parse server range (reused from other commands)
func parseServerRange(pattern string) (string, int, int, error) {
	parts := strings.Split(pattern, ":")
	if len(parts) != 2 {
		return "", 0, 0, fmt.Errorf("invalid server range format, expected 'pattern:start-end'")
	}

	rangeParts := strings.Split(parts[1], "-")
	if len(rangeParts) != 2 {
		return "", 0, 0, fmt.Errorf("invalid range format, expected 'start-end'")
	}

	start, err := strconv.Atoi(rangeParts[0])
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid start number: %w", err)
	}

	end, err := strconv.Atoi(rangeParts[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid end number: %w", err)
	}

	return parts[0], start, end, nil
}
