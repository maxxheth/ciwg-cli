package health

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"ciwg-cli/internal/auth"
)

// HTTPChecker performs HTTP health checks with detailed timing
type HTTPChecker struct {
	Timeout         time.Duration
	FollowRedirects bool
	VerifySSL       bool
}

// HTTPHealthResult contains detailed HTTP health information
type HTTPHealthResult struct {
	StatusCode    int
	ResponseTime  time.Duration
	ContentLength int64
	Headers       map[string]string
	RedirectChain []string
	DNSLookupTime time.Duration
	ConnectTime   time.Duration
	TLSHandshake  time.Duration
	FirstByteTime time.Duration
	TransferTime  time.Duration
	Error         error
}

// Check performs an HTTP health check with detailed timing
func (c *HTTPChecker) Check(url string, headers map[string]string) *HTTPHealthResult {
	result := &HTTPHealthResult{
		Headers:       make(map[string]string),
		RedirectChain: []string{},
	}

	// Timing trackers
	var dnsStart, connectStart, tlsStart, firstByteStart time.Time

	// Create HTTP client
	client := &http.Client{
		Timeout: c.Timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: !c.VerifySSL,
			},
		},
	}

	if !c.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	} else {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			result.RedirectChain = append(result.RedirectChain, req.URL.String())
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		}
	}

	// Create request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		result.Error = fmt.Errorf("failed to create request: %w", err)
		return result
	}

	// Add custom headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Set up tracing
	trace := &httptrace.ClientTrace{
		DNSStart: func(_ httptrace.DNSStartInfo) {
			dnsStart = time.Now()
		},
		DNSDone: func(_ httptrace.DNSDoneInfo) {
			result.DNSLookupTime = time.Since(dnsStart)
		},
		ConnectStart: func(_, _ string) {
			connectStart = time.Now()
		},
		ConnectDone: func(_, _ string, _ error) {
			result.ConnectTime = time.Since(connectStart)
		},
		TLSHandshakeStart: func() {
			tlsStart = time.Now()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			result.TLSHandshake = time.Since(tlsStart)
		},
		GotFirstResponseByte: func() {
			result.FirstByteTime = time.Since(firstByteStart)
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	// Perform request
	startTime := time.Now()
	firstByteStart = startTime

	resp, err := client.Do(req)
	if err != nil {
		result.Error = fmt.Errorf("request failed: %w", err)
		result.ResponseTime = time.Since(startTime)
		return result
	}
	defer resp.Body.Close()

	// Read response body
	transferStart := time.Now()
	bodyBytes, _ := io.ReadAll(resp.Body)
	result.TransferTime = time.Since(transferStart)
	result.ResponseTime = time.Since(startTime)

	// Populate result
	result.StatusCode = resp.StatusCode
	result.ContentLength = int64(len(bodyBytes))

	for k, v := range resp.Header {
		if len(v) > 0 {
			result.Headers[k] = v[0]
		}
	}

	return result
}

// SSLChecker validates SSL certificates
type SSLChecker struct {
	Timeout time.Duration
}

// SSLHealthResult contains SSL certificate information
type SSLHealthResult struct {
	Valid           bool
	Issuer          string
	Subject         string
	NotBefore       time.Time
	NotAfter        time.Time
	DaysUntilExpiry int
	Protocol        string
	CipherSuite     string
	Error           error
}

// Check validates an SSL certificate
func (c *SSLChecker) Check(hostname string) *SSLHealthResult {
	result := &SSLHealthResult{}

	// Extract hostname from URL if needed
	hostname = strings.TrimPrefix(hostname, "https://")
	hostname = strings.TrimPrefix(hostname, "http://")
	if idx := strings.Index(hostname, "/"); idx != -1 {
		hostname = hostname[:idx]
	}
	if idx := strings.Index(hostname, ":"); idx != -1 {
		hostname = hostname[:idx]
	}

	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: c.Timeout},
		"tcp",
		fmt.Sprintf("%s:443", hostname),
		&tls.Config{},
	)
	if err != nil {
		result.Error = fmt.Errorf("TLS connection failed: %w", err)
		return result
	}
	defer conn.Close()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		result.Error = fmt.Errorf("no certificates found")
		return result
	}

	cert := state.PeerCertificates[0]
	now := time.Now()

	result.Valid = true
	result.Issuer = cert.Issuer.String()
	result.Subject = cert.Subject.String()
	result.NotBefore = cert.NotBefore
	result.NotAfter = cert.NotAfter
	result.DaysUntilExpiry = int(cert.NotAfter.Sub(now).Hours() / 24)
	result.Protocol = tls.VersionName(state.Version)
	result.CipherSuite = tls.CipherSuiteName(state.CipherSuite)

	// Check validity
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		result.Valid = false
		result.Error = fmt.Errorf("certificate not valid for current time")
	}

	return result
}

// DockerChecker checks Docker container health
type DockerChecker struct {
	SSHClient *auth.SSHClient
}

// DockerHealthResult contains Docker container health information
type DockerHealthResult struct {
	ContainerID    string
	Status         string
	Health         string
	Uptime         time.Duration
	RestartCount   int
	CPUPercent     float64
	MemoryUsage    int64
	MemoryLimit    int64
	MemoryPercent  float64
	NetworkRxBytes int64
	NetworkTxBytes int64
	Error          error
}

// Check gets Docker container health status
func (c *DockerChecker) Check(container string) *DockerHealthResult {
	result := &DockerHealthResult{}

	if container == "" {
		result.Error = fmt.Errorf("container name is required")
		return result
	}

	// Get container status
	cmd := fmt.Sprintf(`docker inspect %s --format '{{.State.Status}}|{{.State.Health.Status}}|{{.State.StartedAt}}|{{.RestartCount}}|{{.Id}}'`, container)
	stdout, stderr, err := c.SSHClient.ExecuteCommand(cmd)
	if err != nil {
		result.Error = fmt.Errorf("failed to inspect container: %w (stderr: %s)", err, stderr)
		return result
	}

	parts := strings.Split(strings.TrimSpace(stdout), "|")
	if len(parts) >= 5 {
		result.Status = parts[0]
		result.Health = parts[1]
		result.ContainerID = parts[4][:12]

		// Parse uptime
		if startedAt, err := time.Parse(time.RFC3339Nano, parts[2]); err == nil {
			result.Uptime = time.Since(startedAt)
		}

		// Parse restart count
		fmt.Sscanf(parts[3], "%d", &result.RestartCount)
	}

	// Get container stats (non-streaming)
	cmd = fmt.Sprintf(`docker stats %s --no-stream --format '{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}|{{.NetIO}}'`, container)
	stdout, stderr, err = c.SSHClient.ExecuteCommand(cmd)
	if err == nil {
		parts := strings.Split(strings.TrimSpace(stdout), "|")
		if len(parts) >= 4 {
			// Parse CPU percentage
			cpuStr := strings.TrimSuffix(parts[0], "%")
			fmt.Sscanf(cpuStr, "%f", &result.CPUPercent)

			// Parse memory usage (format: "XXXMiB / YYYMiB")
			memParts := strings.Split(parts[1], " / ")
			if len(memParts) == 2 {
				result.MemoryUsage = parseMemorySize(memParts[0])
				result.MemoryLimit = parseMemorySize(memParts[1])
			}

			// Parse memory percentage
			memPercStr := strings.TrimSuffix(parts[2], "%")
			fmt.Sscanf(memPercStr, "%f", &result.MemoryPercent)

			// Parse network I/O (format: "XXXkB / YYYkB")
			netParts := strings.Split(parts[3], " / ")
			if len(netParts) == 2 {
				result.NetworkRxBytes = parseMemorySize(netParts[0])
				result.NetworkTxBytes = parseMemorySize(netParts[1])
			}
		}
	}

	return result
}

// parseMemorySize parses Docker memory sizes (e.g., "123.4MiB", "1.2GiB")
func parseMemorySize(s string) int64 {
	s = strings.TrimSpace(s)
	var value float64
	var unit string

	fmt.Sscanf(s, "%f%s", &value, &unit)

	multiplier := int64(1)
	switch strings.ToUpper(unit) {
	case "KIB", "KB":
		multiplier = 1024
	case "MIB", "MB":
		multiplier = 1024 * 1024
	case "GIB", "GB":
		multiplier = 1024 * 1024 * 1024
	}

	return int64(value * float64(multiplier))
}

// PrompressChecker fetches PromPress metrics
type PrompressChecker struct {
	Timeout time.Duration
}

// PrompressMetrics contains PromPress metrics information
type PrompressMetrics struct {
	Available       bool
	ResponseTime    time.Duration
	MetricsCount    int
	RequestRate     float64
	ErrorRate       float64
	AvgResponseTime float64
	PHPMemoryUsage  int64
	DBQueries       int64
	CacheHitRate    float64
	RawMetrics      string
	Error           error
}

// Check fetches and parses PromPress metrics
func (c *PrompressChecker) Check(siteURL, metricsPath, token string) *PrompressMetrics {
	result := &PrompressMetrics{}

	// Build metrics URL
	metricsURL := strings.TrimRight(siteURL, "/") + "/" + strings.TrimLeft(metricsPath, "/")

	// Create HTTP client
	client := &http.Client{
		Timeout: c.Timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	// Create request
	req, err := http.NewRequest("GET", metricsURL, nil)
	if err != nil {
		result.Error = fmt.Errorf("failed to create request: %w", err)
		return result
	}

	// Add authentication if token provided
	if token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}

	// Perform request
	startTime := time.Now()
	resp, err := client.Do(req)
	result.ResponseTime = time.Since(startTime)

	if err != nil {
		result.Error = fmt.Errorf("metrics request failed: %w", err)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Errorf("metrics endpoint returned status %d", resp.StatusCode)
		return result
	}

	// Read metrics
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = fmt.Errorf("failed to read metrics: %w", err)
		return result
	}

	result.Available = true
	result.RawMetrics = string(bodyBytes)

	// Parse metrics
	result.parseMetrics()

	return result
}

// parseMetrics extracts key metrics from Prometheus format
func (m *PrompressMetrics) parseMetrics() {
	lines := strings.Split(m.RawMetrics, "\n")
	m.MetricsCount = 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		m.MetricsCount++

		// Extract common metrics
		if strings.HasPrefix(line, "wordpress_requests_total") {
			var value float64
			fmt.Sscanf(line, "wordpress_requests_total %f", &value)
			m.RequestRate = value
		} else if strings.HasPrefix(line, "wordpress_request_duration_seconds") {
			var value float64
			fmt.Sscanf(line, "wordpress_request_duration_seconds %f", &value)
			m.AvgResponseTime = value
		} else if strings.HasPrefix(line, "wordpress_errors_total") {
			var value float64
			fmt.Sscanf(line, "wordpress_errors_total %f", &value)
			m.ErrorRate = value
		} else if strings.HasPrefix(line, "wordpress_php_memory_bytes") {
			var value int64
			fmt.Sscanf(line, "wordpress_php_memory_bytes %d", &value)
			m.PHPMemoryUsage = value
		} else if strings.HasPrefix(line, "wordpress_db_queries_total") {
			var value int64
			fmt.Sscanf(line, "wordpress_db_queries_total %d", &value)
			m.DBQueries = value
		} else if strings.HasPrefix(line, "wordpress_cache_hit_rate") {
			var value float64
			fmt.Sscanf(line, "wordpress_cache_hit_rate %f", &value)
			m.CacheHitRate = value
		}
	}
}

// WordPressChecker checks WordPress-specific health
type WordPressChecker struct {
	SSHClient *auth.SSHClient
}

// WordPressHealthResult contains WordPress health information
type WordPressHealthResult struct {
	Version       string
	SiteURL       string
	HomeURL       string
	DBReachable   bool
	CacheEnabled  bool
	DebugMode     bool
	PluginCount   int
	ThemeName     string
	ActivePlugins []string
	Error         error
}

// Check performs WordPress-specific health checks
func (c *WordPressChecker) Check(container string) *WordPressHealthResult {
	result := &WordPressHealthResult{
		ActivePlugins: []string{},
	}

	if container == "" {
		result.Error = fmt.Errorf("container name is required")
		return result
	}

	// Get WordPress version
	cmd := fmt.Sprintf(`docker exec -u 0 %s wp --allow-root core version`, container)
	stdout, _, err := c.SSHClient.ExecuteCommand(cmd)
	if err == nil {
		result.Version = strings.TrimSpace(stdout)
	}

	// Get site URL
	cmd = fmt.Sprintf(`docker exec -u 0 %s wp --allow-root option get siteurl`, container)
	stdout, _, err = c.SSHClient.ExecuteCommand(cmd)
	if err == nil {
		result.SiteURL = strings.TrimSpace(stdout)
	}

	// Get home URL
	cmd = fmt.Sprintf(`docker exec -u 0 %s wp --allow-root option get home`, container)
	stdout, _, err = c.SSHClient.ExecuteCommand(cmd)
	if err == nil {
		result.HomeURL = strings.TrimSpace(stdout)
	}

	// Check database connectivity
	cmd = fmt.Sprintf(`docker exec -u 0 %s wp --allow-root db check`, container)
	_, _, err = c.SSHClient.ExecuteCommand(cmd)
	result.DBReachable = (err == nil)

	// Check if debug mode is enabled
	cmd = fmt.Sprintf(`docker exec -u 0 %s wp --allow-root config get WP_DEBUG`, container)
	stdout, _, err = c.SSHClient.ExecuteCommand(cmd)
	if err == nil {
		result.DebugMode = strings.TrimSpace(stdout) == "true"
	}

	// Get active theme
	cmd = fmt.Sprintf(`docker exec -u 0 %s wp --allow-root theme list --status=active --field=name`, container)
	stdout, _, err = c.SSHClient.ExecuteCommand(cmd)
	if err == nil {
		result.ThemeName = strings.TrimSpace(stdout)
	}

	// Get plugin count and list
	cmd = fmt.Sprintf(`docker exec -u 0 %s wp --allow-root plugin list --status=active --format=json`, container)
	stdout, _, err = c.SSHClient.ExecuteCommand(cmd)
	if err == nil {
		// Simple parse - just count lines
		plugins := strings.Split(strings.TrimSpace(stdout), "\n")
		result.PluginCount = len(plugins)
		// Would parse JSON for detailed info in production
	}

	return result
}
