package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/health"
)

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Monitor WordPress site health and metrics",
	Long: `Monitor WordPress site health with PromPress metrics integration and generic health checks.
Provides comprehensive site monitoring including:
- Response time and status codes
- SSL certificate validation
- Docker container health
- PromPress metrics (if installed)
- Resource utilization
- WordPress-specific health checks`,
}

var healthCheckCmd = &cobra.Command{
	Use:   "check [hostname]",
	Short: "Perform comprehensive health check on WordPress site",
	Long: `Perform a complete health check including:
- HTTP/HTTPS response time and status
- SSL certificate validation and expiry
- Docker container status and uptime
- PromPress metrics endpoint (if available)
- WordPress site accessibility
- Database connectivity
- PHP-FPM status

Supports single host or server range for batch monitoring.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runHealthCheck,
}

var healthMetricsCmd = &cobra.Command{
	Use:   "metrics [hostname]",
	Short: "Fetch PromPress metrics from WordPress site",
	Long: `Fetch and display PromPress metrics in Prometheus format.
Requires PromPress plugin to be installed and configured.

Metrics include:
- Request rates and response times
- PHP-FPM pool status
- Database query performance
- WordPress cache hit rates
- Object cache statistics
- HTTP response codes
- Memory usage`,
	Args: cobra.MaximumNArgs(1),
	RunE: runHealthMetrics,
}

var healthProbeCmd = &cobra.Command{
	Use:   "probe [hostname]",
	Short: "Quick curl-like health probe with timing details",
	Long: `Perform a quick HTTP/HTTPS probe similar to curl with detailed timing:
- DNS lookup time
- TCP connection time
- TLS handshake time
- Server processing time
- Content transfer time
- Total request time

Useful for quick status checks and performance diagnostics.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runHealthProbe,
}

var healthDashboardCmd = &cobra.Command{
	Use:   "dashboard [hostname]",
	Short: "Display real-time health dashboard with live metrics",
	Long: `Display a real-time dashboard with continuously updating metrics:
- Current response time and status
- Request rate and error rate
- Active connections and threads
- Memory and CPU usage
- PromPress metrics (if available)

Refreshes every interval (default 5s). Press Ctrl+C to exit.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runHealthDashboard,
}

func init() {
	rootCmd.AddCommand(healthCmd)
	healthCmd.AddCommand(healthCheckCmd)
	healthCmd.AddCommand(healthMetricsCmd)
	healthCmd.AddCommand(healthProbeCmd)
	healthCmd.AddCommand(healthDashboardCmd)

	// Add standard SSH flags to all commands
	addSSHFlags(healthCheckCmd)
	addSSHFlags(healthMetricsCmd)
	addSSHFlags(healthProbeCmd)
	addSSHFlags(healthDashboardCmd)

	// Health check command flags
	healthCheckCmd.Flags().String("container", "", "WordPress container name (omit if using --all-containers)")
	healthCheckCmd.Flags().String("server-range", getEnvWithDefault("SERVER_RANGE", ""), "Server range pattern (e.g., wp%d.example.com:0-41:!10,15)")
	healthCheckCmd.Flags().Bool("all-containers", false, "Check all containers matching prefix")
	healthCheckCmd.Flags().String("prefix", "wp_", "Container name prefix for --all-containers")
	healthCheckCmd.Flags().Bool("ssl-check", true, "Validate SSL certificates")
	healthCheckCmd.Flags().Bool("prompress", true, "Attempt to fetch PromPress metrics")
	healthCheckCmd.Flags().String("metrics-path", getEnvWithDefault("PROMPRESS_METRICS_PATH", "metrics"), "PromPress metrics endpoint path")
	healthCheckCmd.Flags().String("metrics-token", getEnvWithDefault("PROMPRESS_TOKEN", ""), "PromPress authentication token")
	healthCheckCmd.Flags().Duration("check-timeout", 30*time.Second, "Health check timeout")
	healthCheckCmd.Flags().StringP("output", "o", "text", "Output format: text, json, prometheus")
	healthCheckCmd.Flags().Bool("docker-stats", true, "Include Docker container statistics")
	healthCheckCmd.Flags().Bool("verbose", false, "Show detailed output")

	// Metrics command flags
	healthMetricsCmd.Flags().String("container", "", "WordPress container name")
	healthMetricsCmd.Flags().String("server-range", getEnvWithDefault("SERVER_RANGE", ""), "Server range pattern")
	healthMetricsCmd.Flags().String("metrics-path", getEnvWithDefault("PROMPRESS_METRICS_PATH", "metrics"), "Metrics endpoint path")
	healthMetricsCmd.Flags().String("metrics-token", getEnvWithDefault("PROMPRESS_TOKEN", ""), "Authentication token")
	healthMetricsCmd.Flags().Duration("metrics-timeout", 10*time.Second, "Request timeout")
	healthMetricsCmd.Flags().StringP("output", "o", "prometheus", "Output format: prometheus, json")
	healthMetricsCmd.Flags().Bool("parse", false, "Parse and format metrics output")

	// Probe command flags
	healthProbeCmd.Flags().String("url", "", "Explicit URL to probe (overrides hostname)")
	healthProbeCmd.Flags().String("container", "", "WordPress container name for domain lookup")
	healthProbeCmd.Flags().String("server-range", getEnvWithDefault("SERVER_RANGE", ""), "Server range pattern")
	healthProbeCmd.Flags().Duration("probe-timeout", 10*time.Second, "Request timeout")
	healthProbeCmd.Flags().Bool("follow-redirects", true, "Follow HTTP redirects")
	healthProbeCmd.Flags().Bool("verify-ssl", true, "Verify SSL certificates")
	healthProbeCmd.Flags().String("method", "GET", "HTTP method (GET, HEAD, POST, etc.)")
	healthProbeCmd.Flags().StringSlice("header", []string{}, "Custom headers (can be specified multiple times)")
	healthProbeCmd.Flags().StringP("output", "o", "text", "Output format: text, json")

	// Dashboard command flags
	healthDashboardCmd.Flags().String("container", "", "WordPress container name")
	healthDashboardCmd.Flags().Duration("interval", 5*time.Second, "Refresh interval")
	healthDashboardCmd.Flags().String("metrics-path", getEnvWithDefault("PROMPRESS_METRICS_PATH", "metrics"), "Metrics endpoint path")
	healthDashboardCmd.Flags().String("metrics-token", getEnvWithDefault("PROMPRESS_TOKEN", ""), "Authentication token")
	healthDashboardCmd.Flags().Bool("prompress", true, "Include PromPress metrics")
	healthDashboardCmd.Flags().Bool("docker-stats", true, "Include Docker statistics")
}

// HealthCheckResult represents the comprehensive health check result
type HealthCheckResult struct {
	Hostname     string                 `json:"hostname"`
	Container    string                 `json:"container,omitempty"`
	Timestamp    time.Time              `json:"timestamp"`
	Status       string                 `json:"status"` // healthy, degraded, unhealthy, unknown
	HTTP         *HTTPHealthResult      `json:"http,omitempty"`
	SSL          *SSLHealthResult       `json:"ssl,omitempty"`
	Docker       *DockerHealthResult    `json:"docker,omitempty"`
	PromPress    *PrompressMetrics      `json:"prompress,omitempty"`
	WordPress    *WordPressHealthResult `json:"wordpress,omitempty"`
	ResponseTime time.Duration          `json:"response_time"`
	ErrorMessage string                 `json:"error_message,omitempty"`
	Warnings     []string               `json:"warnings,omitempty"`
}

type HTTPHealthResult struct {
	StatusCode    int               `json:"status_code"`
	ResponseTime  time.Duration     `json:"response_time"`
	ContentLength int64             `json:"content_length"`
	Headers       map[string]string `json:"headers,omitempty"`
	RedirectChain []string          `json:"redirect_chain,omitempty"`
	DNSLookupTime time.Duration     `json:"dns_lookup_time"`
	ConnectTime   time.Duration     `json:"connect_time"`
	TLSHandshake  time.Duration     `json:"tls_handshake"`
	FirstByteTime time.Duration     `json:"first_byte_time"`
	TransferTime  time.Duration     `json:"transfer_time"`
}

type SSLHealthResult struct {
	Valid           bool      `json:"valid"`
	Issuer          string    `json:"issuer"`
	Subject         string    `json:"subject"`
	NotBefore       time.Time `json:"not_before"`
	NotAfter        time.Time `json:"not_after"`
	DaysUntilExpiry int       `json:"days_until_expiry"`
	Protocol        string    `json:"protocol"`
	CipherSuite     string    `json:"cipher_suite"`
}

type DockerHealthResult struct {
	ContainerID    string        `json:"container_id"`
	Status         string        `json:"status"`
	Health         string        `json:"health,omitempty"`
	Uptime         time.Duration `json:"uptime"`
	RestartCount   int           `json:"restart_count"`
	CPUPercent     float64       `json:"cpu_percent,omitempty"`
	MemoryUsage    int64         `json:"memory_usage,omitempty"`
	MemoryLimit    int64         `json:"memory_limit,omitempty"`
	MemoryPercent  float64       `json:"memory_percent,omitempty"`
	NetworkRxBytes int64         `json:"network_rx_bytes,omitempty"`
	NetworkTxBytes int64         `json:"network_tx_bytes,omitempty"`
}

type PrompressMetrics struct {
	Available       bool          `json:"available"`
	ResponseTime    time.Duration `json:"response_time"`
	MetricsCount    int           `json:"metrics_count"`
	RequestRate     float64       `json:"request_rate,omitempty"`
	ErrorRate       float64       `json:"error_rate,omitempty"`
	AvgResponseTime float64       `json:"avg_response_time,omitempty"`
	PHPMemoryUsage  int64         `json:"php_memory_usage,omitempty"`
	DBQueries       int64         `json:"db_queries,omitempty"`
	CacheHitRate    float64       `json:"cache_hit_rate,omitempty"`
	RawMetrics      string        `json:"raw_metrics,omitempty"`
}

type WordPressHealthResult struct {
	Version       string   `json:"version"`
	SiteURL       string   `json:"site_url"`
	HomeURL       string   `json:"home_url"`
	DBReachable   bool     `json:"db_reachable"`
	CacheEnabled  bool     `json:"cache_enabled"`
	DebugMode     bool     `json:"debug_mode"`
	PluginCount   int      `json:"plugin_count"`
	ThemeName     string   `json:"theme_name"`
	ActivePlugins []string `json:"active_plugins,omitempty"`
}

func runHealthCheck(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processHealthCheckForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required when not using --server-range")
	}

	hostname := args[0]
	return performHealthCheck(cmd, hostname)
}

func processHealthCheckForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("invalid server range: %w", err)
	}

	allContainers, _ := cmd.Flags().GetBool("all-containers")
	output, _ := cmd.Flags().GetString("output")

	results := []HealthCheckResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Limit concurrent checks to avoid overwhelming network/servers
	semaphore := make(chan struct{}, 10)

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)

		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result := performHealthCheckInternal(cmd, host, allContainers)

			mu.Lock()
			results = append(results, result...)
			mu.Unlock()
		}(hostname)
	}

	wg.Wait()

	// Output results
	return formatHealthCheckResults(results, output)
}

func performHealthCheck(cmd *cobra.Command, hostname string) error {
	allContainers, _ := cmd.Flags().GetBool("all-containers")
	output, _ := cmd.Flags().GetString("output")

	results := performHealthCheckInternal(cmd, hostname, allContainers)
	return formatHealthCheckResults(results, output)
}

func performHealthCheckInternal(cmd *cobra.Command, hostname string, allContainers bool) []HealthCheckResult {
	results := []HealthCheckResult{}

	if allContainers {
		// Connect and discover containers
		sshClient, err := createSSHClient(cmd, hostname)
		if err != nil {
			results = append(results, HealthCheckResult{
				Hostname:     hostname,
				Timestamp:    time.Now(),
				Status:       "unknown",
				ErrorMessage: fmt.Sprintf("SSH connection failed: %v", err),
			})
			return results
		}
		defer sshClient.Close()

		prefix, _ := cmd.Flags().GetString("prefix")
		containers, err := discoverContainers(sshClient, prefix)
		if err != nil {
			results = append(results, HealthCheckResult{
				Hostname:     hostname,
				Timestamp:    time.Now(),
				Status:       "unknown",
				ErrorMessage: fmt.Sprintf("Container discovery failed: %v", err),
			})
			return results
		}

		for _, container := range containers {
			result := performSingleHealthCheck(cmd, hostname, container, sshClient)
			results = append(results, result)
		}
	} else {
		container, _ := cmd.Flags().GetString("container")
		sshClient, err := createSSHClient(cmd, hostname)
		if err != nil {
			results = append(results, HealthCheckResult{
				Hostname:     hostname,
				Container:    container,
				Timestamp:    time.Now(),
				Status:       "unknown",
				ErrorMessage: fmt.Sprintf("SSH connection failed: %v", err),
			})
			return results
		}
		defer sshClient.Close()

		result := performSingleHealthCheck(cmd, hostname, container, sshClient)
		results = append(results, result)
	}

	return results
}

func performSingleHealthCheck(cmd *cobra.Command, hostname, container string, sshClient *auth.SSHClient) HealthCheckResult {
	result := HealthCheckResult{
		Hostname:  hostname,
		Container: container,
		Timestamp: time.Now(),
		Status:    "healthy",
		Warnings:  []string{},
	}

	startTime := time.Now()

	// Get site URL from container
	siteURL, err := getWordPressSiteURL(sshClient, container)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Could not determine site URL: %v", err))
		siteURL = fmt.Sprintf("https://%s", hostname)
	}

	timeout, _ := cmd.Flags().GetDuration("check-timeout")

	// HTTP health check
	httpChecker := &health.HTTPChecker{
		Timeout:         timeout,
		FollowRedirects: true,
		VerifySSL:       true,
	}

	httpResult := httpChecker.Check(siteURL, nil)
	if httpResult.Error == nil {
		result.HTTP = &HTTPHealthResult{
			StatusCode:    httpResult.StatusCode,
			ResponseTime:  httpResult.ResponseTime,
			ContentLength: httpResult.ContentLength,
			Headers:       httpResult.Headers,
			RedirectChain: httpResult.RedirectChain,
			DNSLookupTime: httpResult.DNSLookupTime,
			ConnectTime:   httpResult.ConnectTime,
			TLSHandshake:  httpResult.TLSHandshake,
			FirstByteTime: httpResult.FirstByteTime,
			TransferTime:  httpResult.TransferTime,
		}

		if httpResult.StatusCode >= 500 {
			result.Status = "unhealthy"
		} else if httpResult.StatusCode >= 400 {
			result.Status = "degraded"
		}
	} else {
		result.Warnings = append(result.Warnings, fmt.Sprintf("HTTP check failed: %v", httpResult.Error))
		result.Status = "unhealthy"
	}

	// SSL check
	sslCheck, _ := cmd.Flags().GetBool("ssl-check")
	if sslCheck && strings.HasPrefix(siteURL, "https://") {
		sslChecker := &health.SSLChecker{Timeout: timeout}
		sslResult := sslChecker.Check(siteURL)

		if sslResult.Error == nil {
			result.SSL = &SSLHealthResult{
				Valid:           sslResult.Valid,
				Issuer:          sslResult.Issuer,
				Subject:         sslResult.Subject,
				NotBefore:       sslResult.NotBefore,
				NotAfter:        sslResult.NotAfter,
				DaysUntilExpiry: sslResult.DaysUntilExpiry,
				Protocol:        sslResult.Protocol,
				CipherSuite:     sslResult.CipherSuite,
			}

			if !sslResult.Valid {
				result.Status = "unhealthy"
			} else if sslResult.DaysUntilExpiry < 30 {
				result.Warnings = append(result.Warnings, fmt.Sprintf("SSL certificate expires in %d days", sslResult.DaysUntilExpiry))
				if result.Status == "healthy" {
					result.Status = "degraded"
				}
			}
		}
	}

	// Docker health check
	dockerStats, _ := cmd.Flags().GetBool("docker-stats")
	if dockerStats && container != "" {
		dockerChecker := &health.DockerChecker{SSHClient: sshClient}
		dockerResult := dockerChecker.Check(container)

		if dockerResult.Error == nil {
			result.Docker = &DockerHealthResult{
				ContainerID:    dockerResult.ContainerID,
				Status:         dockerResult.Status,
				Health:         dockerResult.Health,
				Uptime:         dockerResult.Uptime,
				RestartCount:   dockerResult.RestartCount,
				CPUPercent:     dockerResult.CPUPercent,
				MemoryUsage:    dockerResult.MemoryUsage,
				MemoryLimit:    dockerResult.MemoryLimit,
				MemoryPercent:  dockerResult.MemoryPercent,
				NetworkRxBytes: dockerResult.NetworkRxBytes,
				NetworkTxBytes: dockerResult.NetworkTxBytes,
			}

			if dockerResult.Status != "running" {
				result.Status = "unhealthy"
			}
			if dockerResult.MemoryPercent > 90 {
				result.Warnings = append(result.Warnings, "High memory usage")
			}
			if dockerResult.CPUPercent > 80 {
				result.Warnings = append(result.Warnings, "High CPU usage")
			}
		}
	}

	// PromPress metrics check
	checkPrompress, _ := cmd.Flags().GetBool("prompress")
	if checkPrompress {
		metricsPath, _ := cmd.Flags().GetString("metrics-path")
		metricsToken, _ := cmd.Flags().GetString("metrics-token")

		prompressChecker := &health.PrompressChecker{Timeout: timeout}
		prompressResult := prompressChecker.Check(siteURL, metricsPath, metricsToken)

		if prompressResult.Error == nil && prompressResult.Available {
			result.PromPress = &PrompressMetrics{
				Available:       prompressResult.Available,
				ResponseTime:    prompressResult.ResponseTime,
				MetricsCount:    prompressResult.MetricsCount,
				RequestRate:     prompressResult.RequestRate,
				ErrorRate:       prompressResult.ErrorRate,
				AvgResponseTime: prompressResult.AvgResponseTime,
				PHPMemoryUsage:  prompressResult.PHPMemoryUsage,
				DBQueries:       prompressResult.DBQueries,
				CacheHitRate:    prompressResult.CacheHitRate,
				RawMetrics:      prompressResult.RawMetrics,
			}
		}
	}

	// WordPress-specific checks
	if container != "" {
		wpChecker := &health.WordPressChecker{SSHClient: sshClient}
		wpResult := wpChecker.Check(container)

		if wpResult.Error == nil {
			result.WordPress = &WordPressHealthResult{
				Version:       wpResult.Version,
				SiteURL:       wpResult.SiteURL,
				HomeURL:       wpResult.HomeURL,
				DBReachable:   wpResult.DBReachable,
				CacheEnabled:  wpResult.CacheEnabled,
				DebugMode:     wpResult.DebugMode,
				PluginCount:   wpResult.PluginCount,
				ThemeName:     wpResult.ThemeName,
				ActivePlugins: wpResult.ActivePlugins,
			}

			if !wpResult.DBReachable {
				result.Status = "unhealthy"
			}
		}
	}

	result.ResponseTime = time.Since(startTime)
	return result
}

func getWordPressSiteURL(sshClient *auth.SSHClient, container string) (string, error) {
	if container == "" {
		return "", fmt.Errorf("container name is required")
	}

	cmd := fmt.Sprintf(`docker exec -u 0 %s wp --allow-root option get siteurl 2>/dev/null`, container)
	stdout, stderr, err := sshClient.ExecuteCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get site URL: %w (stderr: %s)", err, stderr)
	}

	siteURL := strings.TrimSpace(stdout)
	if siteURL == "" {
		return "", fmt.Errorf("site URL is empty")
	}

	return siteURL, nil
}

func checkHTTPHealth(cmd *cobra.Command, url string) *HTTPHealthResult {
	timeout, _ := cmd.Flags().GetDuration("check-timeout")

	checker := &health.HTTPChecker{
		Timeout:         timeout,
		FollowRedirects: true,
		VerifySSL:       true,
	}

	result := checker.Check(url, nil)
	if result.Error != nil {
		return nil
	}

	return &HTTPHealthResult{
		StatusCode:    result.StatusCode,
		ResponseTime:  result.ResponseTime,
		ContentLength: result.ContentLength,
		Headers:       result.Headers,
		RedirectChain: result.RedirectChain,
		DNSLookupTime: result.DNSLookupTime,
		ConnectTime:   result.ConnectTime,
		TLSHandshake:  result.TLSHandshake,
		FirstByteTime: result.FirstByteTime,
		TransferTime:  result.TransferTime,
	}
}

func checkSSLHealth(url string) *SSLHealthResult {
	checker := &health.SSLChecker{Timeout: 10 * time.Second}
	result := checker.Check(url)

	if result.Error != nil {
		return nil
	}

	return &SSLHealthResult{
		Valid:           result.Valid,
		Issuer:          result.Issuer,
		Subject:         result.Subject,
		NotBefore:       result.NotBefore,
		NotAfter:        result.NotAfter,
		DaysUntilExpiry: result.DaysUntilExpiry,
		Protocol:        result.Protocol,
		CipherSuite:     result.CipherSuite,
	}
}

func checkDockerHealth(sshClient *auth.SSHClient, container string) *DockerHealthResult {
	checker := &health.DockerChecker{SSHClient: sshClient}
	result := checker.Check(container)

	if result.Error != nil {
		return nil
	}

	return &DockerHealthResult{
		ContainerID:    result.ContainerID,
		Status:         result.Status,
		Health:         result.Health,
		Uptime:         result.Uptime,
		RestartCount:   result.RestartCount,
		CPUPercent:     result.CPUPercent,
		MemoryUsage:    result.MemoryUsage,
		MemoryLimit:    result.MemoryLimit,
		MemoryPercent:  result.MemoryPercent,
		NetworkRxBytes: result.NetworkRxBytes,
		NetworkTxBytes: result.NetworkTxBytes,
	}
}

func checkPrompressMetrics(siteURL, metricsPath, token string) *PrompressMetrics {
	checker := &health.PrompressChecker{Timeout: 10 * time.Second}
	result := checker.Check(siteURL, metricsPath, token)

	if result.Error != nil || !result.Available {
		return nil
	}

	return &PrompressMetrics{
		Available:       result.Available,
		ResponseTime:    result.ResponseTime,
		MetricsCount:    result.MetricsCount,
		RequestRate:     result.RequestRate,
		ErrorRate:       result.ErrorRate,
		AvgResponseTime: result.AvgResponseTime,
		PHPMemoryUsage:  result.PHPMemoryUsage,
		DBQueries:       result.DBQueries,
		CacheHitRate:    result.CacheHitRate,
		RawMetrics:      result.RawMetrics,
	}
}

func checkWordPressHealth(sshClient *auth.SSHClient, container string) *WordPressHealthResult {
	checker := &health.WordPressChecker{SSHClient: sshClient}
	result := checker.Check(container)

	if result.Error != nil {
		return nil
	}

	return &WordPressHealthResult{
		Version:       result.Version,
		SiteURL:       result.SiteURL,
		HomeURL:       result.HomeURL,
		DBReachable:   result.DBReachable,
		CacheEnabled:  result.CacheEnabled,
		DebugMode:     result.DebugMode,
		PluginCount:   result.PluginCount,
		ThemeName:     result.ThemeName,
		ActivePlugins: result.ActivePlugins,
	}
}

func formatHealthCheckResults(results []HealthCheckResult, format string) error {
	switch format {
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(results)
	case "prometheus":
		return formatAsPrometheus(results)
	default:
		return formatAsText(results)
	}
}

func formatAsPrometheus(results []HealthCheckResult) error {
	// Format results as Prometheus metrics
	for _, result := range results {
		labels := fmt.Sprintf(`hostname="%s",container="%s"`, result.Hostname, result.Container)

		// Health status (1=healthy, 0.5=degraded, 0=unhealthy)
		statusValue := 0.0
		switch result.Status {
		case "healthy":
			statusValue = 1.0
		case "degraded":
			statusValue = 0.5
		case "unhealthy":
			statusValue = 0.0
		}
		fmt.Printf("wordpress_health_status{%s} %.1f\n", labels, statusValue)

		if result.HTTP != nil {
			fmt.Printf("wordpress_http_response_time_seconds{%s} %.3f\n", labels, result.HTTP.ResponseTime.Seconds())
			fmt.Printf("wordpress_http_status_code{%s} %d\n", labels, result.HTTP.StatusCode)
		}

		if result.Docker != nil {
			fmt.Printf("wordpress_docker_memory_percent{%s} %.2f\n", labels, result.Docker.MemoryPercent)
			fmt.Printf("wordpress_docker_cpu_percent{%s} %.2f\n", labels, result.Docker.CPUPercent)
			fmt.Printf("wordpress_docker_uptime_seconds{%s} %.0f\n", labels, result.Docker.Uptime.Seconds())
		}
	}
	return nil
}

func formatAsText(results []HealthCheckResult) error {
	for _, result := range results {
		statusIcon := "✓"
		statusColor := "\033[32m" // Green

		switch result.Status {
		case "degraded":
			statusIcon = "⚠"
			statusColor = "\033[33m" // Yellow
		case "unhealthy":
			statusIcon = "✗"
			statusColor = "\033[31m" // Red
		case "unknown":
			statusIcon = "?"
			statusColor = "\033[90m" // Gray
		}

		fmt.Printf("%s%s\033[0m %s", statusColor, statusIcon, result.Hostname)
		if result.Container != "" {
			fmt.Printf(" [%s]", result.Container)
		}
		fmt.Printf(" - %s (%.2fs)\n", result.Status, result.ResponseTime.Seconds())

		if result.HTTP != nil {
			fmt.Printf("  HTTP: %d in %.3fs\n", result.HTTP.StatusCode, result.HTTP.ResponseTime.Seconds())
		}

		if result.SSL != nil && result.SSL.Valid {
			fmt.Printf("  SSL: Valid, expires in %d days\n", result.SSL.DaysUntilExpiry)
		}

		if result.Docker != nil {
			fmt.Printf("  Docker: %s, uptime %.0fm, CPU %.1f%%, Memory %.1f%%\n",
				result.Docker.Status,
				result.Docker.Uptime.Minutes(),
				result.Docker.CPUPercent,
				result.Docker.MemoryPercent)
		}

		if result.PromPress != nil && result.PromPress.Available {
			fmt.Printf("  PromPress: %d metrics, %.2f req/s\n",
				result.PromPress.MetricsCount,
				result.PromPress.RequestRate)
		}

		for _, warning := range result.Warnings {
			fmt.Printf("  \033[33m⚠\033[0m %s\n", warning)
		}

		if result.ErrorMessage != "" {
			fmt.Printf("  \033[31m✗\033[0m %s\n", result.ErrorMessage)
		}

		fmt.Println()
	}
	return nil
}

func runHealthMetrics(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processMetricsForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required when not using --server-range")
	}

	hostname := args[0]
	container, _ := cmd.Flags().GetString("container")

	// Connect to server
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer sshClient.Close()

	// Get site URL
	siteURL := ""
	if container != "" {
		siteURL, err = getWordPressSiteURL(sshClient, container)
		if err != nil {
			return fmt.Errorf("failed to get site URL: %w", err)
		}
	} else {
		siteURL = fmt.Sprintf("https://%s", hostname)
	}

	// Fetch metrics
	metricsPath, _ := cmd.Flags().GetString("metrics-path")
	metricsToken, _ := cmd.Flags().GetString("metrics-token")
	timeout, _ := cmd.Flags().GetDuration("metrics-timeout")

	checker := &health.PrompressChecker{Timeout: timeout}
	result := checker.Check(siteURL, metricsPath, metricsToken)

	if result.Error != nil {
		return fmt.Errorf("failed to fetch metrics: %w", result.Error)
	}

	if !result.Available {
		return fmt.Errorf("PromPress metrics not available at %s", siteURL)
	}

	// Output metrics
	output, _ := cmd.Flags().GetString("output")
	parse, _ := cmd.Flags().GetBool("parse")

	switch output {
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	case "prometheus":
		if parse {
			fmt.Printf("# PromPress Metrics from %s\n", hostname)
			fmt.Printf("# Response Time: %.3fs\n", result.ResponseTime.Seconds())
			fmt.Printf("# Metrics Count: %d\n\n", result.MetricsCount)
		}
		fmt.Print(result.RawMetrics)
		return nil
	default:
		return fmt.Errorf("unsupported output format: %s", output)
	}
}

func processMetricsForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("invalid server range: %w", err)
	}

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("# Metrics from %s\n", hostname)

		// Create a modified args slice with the hostname
		if err := runHealthMetrics(cmd, []string{hostname}); err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching metrics from %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func runHealthProbe(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processProbeForServerRange(cmd, serverRange)
	}

	url, _ := cmd.Flags().GetString("url")
	if url == "" {
		if len(args) == 0 {
			return fmt.Errorf("hostname or --url is required when not using --server-range")
		}
		url = fmt.Sprintf("https://%s", args[0])
	}

	// Perform probe
	timeout, _ := cmd.Flags().GetDuration("probe-timeout")
	followRedirects, _ := cmd.Flags().GetBool("follow-redirects")
	verifySSL, _ := cmd.Flags().GetBool("verify-ssl")
	headers, _ := cmd.Flags().GetStringSlice("header")

	// Parse headers
	headerMap := make(map[string]string)
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headerMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	checker := &health.HTTPChecker{
		Timeout:         timeout,
		FollowRedirects: followRedirects,
		VerifySSL:       verifySSL,
	}

	result := checker.Check(url, headerMap)

	// Output result
	output, _ := cmd.Flags().GetString("output")

	switch output {
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	default:
		return formatProbeText(url, result)
	}
}

func processProbeForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("invalid server range: %w", err)
	}

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)
		if err := runHealthProbe(cmd, []string{hostname}); err != nil {
			fmt.Fprintf(os.Stderr, "Error probing %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func formatProbeText(url string, result *health.HTTPHealthResult) error {
	if result.Error != nil {
		fmt.Printf("❌ Probe failed: %v\n", result.Error)
		return nil
	}

	// Status line
	statusIcon := "✓"
	statusColor := "\033[32m" // Green

	if result.StatusCode >= 500 {
		statusIcon = "✗"
		statusColor = "\033[31m" // Red
	} else if result.StatusCode >= 400 {
		statusIcon = "⚠"
		statusColor = "\033[33m" // Yellow
	}

	fmt.Printf("%s%s\033[0m %s - Status: %d\n", statusColor, statusIcon, url, result.StatusCode)
	fmt.Println()

	// Timing breakdown
	fmt.Println("Timing breakdown:")
	fmt.Printf("  DNS Lookup:     %8.3fs\n", result.DNSLookupTime.Seconds())
	fmt.Printf("  TCP Connect:    %8.3fs\n", result.ConnectTime.Seconds())

	if result.TLSHandshake > 0 {
		fmt.Printf("  TLS Handshake:  %8.3fs\n", result.TLSHandshake.Seconds())
	}

	fmt.Printf("  First Byte:     %8.3fs\n", result.FirstByteTime.Seconds())
	fmt.Printf("  Transfer:       %8.3fs\n", result.TransferTime.Seconds())
	fmt.Printf("  ───────────────────────\n")
	fmt.Printf("  Total:          %8.3fs\n", result.ResponseTime.Seconds())
	fmt.Println()

	// Response details
	fmt.Println("Response details:")
	fmt.Printf("  Content-Length: %d bytes\n", result.ContentLength)

	if len(result.RedirectChain) > 0 {
		fmt.Printf("  Redirects:      %d\n", len(result.RedirectChain))
		for i, redirect := range result.RedirectChain {
			fmt.Printf("    %d. %s\n", i+1, redirect)
		}
	}

	// Key headers
	if server, ok := result.Headers["Server"]; ok {
		fmt.Printf("  Server:         %s\n", server)
	}
	if contentType, ok := result.Headers["Content-Type"]; ok {
		fmt.Printf("  Content-Type:   %s\n", contentType)
	}

	return nil
}

func runHealthDashboard(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	hostname := args[0]
	container, _ := cmd.Flags().GetString("container")
	interval, _ := cmd.Flags().GetDuration("interval")

	// Connect to server
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer sshClient.Close()

	// Get site URL
	siteURL := ""
	if container != "" {
		siteURL, err = getWordPressSiteURL(sshClient, container)
		if err != nil {
			fmt.Printf("Warning: Could not get site URL: %v\n", err)
			siteURL = fmt.Sprintf("https://%s", hostname)
		}
	} else {
		siteURL = fmt.Sprintf("https://%s", hostname)
	}

	fmt.Printf("\033[2J\033[H") // Clear screen
	fmt.Printf("Health Dashboard: %s\n", hostname)
	if container != "" {
		fmt.Printf("Container: %s\n", container)
	}
	fmt.Printf("Refreshing every %v (Press Ctrl+C to exit)\n\n", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial check
	displayDashboard(cmd, sshClient, hostname, container, siteURL)

	// Continuous updates
	for range ticker.C {
		fmt.Printf("\033[H") // Move cursor to top
		displayDashboard(cmd, sshClient, hostname, container, siteURL)
	}

	return nil
}

func displayDashboard(cmd *cobra.Command, sshClient *auth.SSHClient, hostname, container, siteURL string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("Last updated: %s\n\n", timestamp)

	// HTTP check
	httpChecker := &health.HTTPChecker{
		Timeout:         10 * time.Second,
		FollowRedirects: true,
		VerifySSL:       true,
	}
	httpResult := httpChecker.Check(siteURL, nil)

	fmt.Println("HTTP Status:")
	if httpResult.Error != nil {
		fmt.Printf("  Status: \033[31m✗ Error\033[0m - %v\n", httpResult.Error)
	} else {
		statusColor := "\033[32m" // Green
		if httpResult.StatusCode >= 500 {
			statusColor = "\033[31m" // Red
		} else if httpResult.StatusCode >= 400 {
			statusColor = "\033[33m" // Yellow
		}
		fmt.Printf("  Status: %s%d\033[0m\n", statusColor, httpResult.StatusCode)
		fmt.Printf("  Response Time: %.3fs\n", httpResult.ResponseTime.Seconds())
		fmt.Printf("  Content Length: %d bytes\n", httpResult.ContentLength)
	}
	fmt.Println()

	// Docker stats
	dockerStats, _ := cmd.Flags().GetBool("docker-stats")
	if dockerStats && container != "" {
		dockerChecker := &health.DockerChecker{SSHClient: sshClient}
		dockerResult := dockerChecker.Check(container)

		fmt.Println("Docker Stats:")
		if dockerResult.Error != nil {
			fmt.Printf("  Error: %v\n", dockerResult.Error)
		} else {
			fmt.Printf("  Status: %s\n", dockerResult.Status)
			fmt.Printf("  Uptime: %.0fm\n", dockerResult.Uptime.Minutes())
			fmt.Printf("  CPU: %.1f%%\n", dockerResult.CPUPercent)
			fmt.Printf("  Memory: %.1f%% (%d MB / %d MB)\n",
				dockerResult.MemoryPercent,
				dockerResult.MemoryUsage/1024/1024,
				dockerResult.MemoryLimit/1024/1024)
			fmt.Printf("  Network RX: %d MB\n", dockerResult.NetworkRxBytes/1024/1024)
			fmt.Printf("  Network TX: %d MB\n", dockerResult.NetworkTxBytes/1024/1024)
		}
		fmt.Println()
	}

	// PromPress metrics
	checkPrompress, _ := cmd.Flags().GetBool("prompress")
	if checkPrompress {
		metricsPath, _ := cmd.Flags().GetString("metrics-path")
		metricsToken, _ := cmd.Flags().GetString("metrics-token")

		prompressChecker := &health.PrompressChecker{Timeout: 10 * time.Second}
		prompressResult := prompressChecker.Check(siteURL, metricsPath, metricsToken)

		fmt.Println("PromPress Metrics:")
		if prompressResult.Error != nil || !prompressResult.Available {
			fmt.Printf("  Not available\n")
		} else {
			fmt.Printf("  Metrics Count: %d\n", prompressResult.MetricsCount)
			fmt.Printf("  Request Rate: %.2f req/s\n", prompressResult.RequestRate)
			fmt.Printf("  Error Rate: %.2f err/s\n", prompressResult.ErrorRate)
			if prompressResult.AvgResponseTime > 0 {
				fmt.Printf("  Avg Response: %.3fs\n", prompressResult.AvgResponseTime)
			}
			if prompressResult.PHPMemoryUsage > 0 {
				fmt.Printf("  PHP Memory: %d MB\n", prompressResult.PHPMemoryUsage/1024/1024)
			}
			if prompressResult.DBQueries > 0 {
				fmt.Printf("  DB Queries: %d\n", prompressResult.DBQueries)
			}
			if prompressResult.CacheHitRate > 0 {
				fmt.Printf("  Cache Hit Rate: %.1f%%\n", prompressResult.CacheHitRate*100)
			}
		}
		fmt.Println()
	}
}
