package prompress

import (
	"fmt"
	"strings"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/compose"
)

// PrometheusManager handles Prometheus configuration updates
type PrometheusManager struct {
	sshClient                      *auth.SSHClient
	prometheusHost                 string
	prometheusManager              *compose.Manager
	prometheusYmlPath              string // Direct path to prometheus.yml if provided
	prometheusDockerComposeYmlPath string // Direct path to docker-compose.yml if provided
}

// ScrapeConfig represents a Prometheus scrape configuration for a WordPress site
type ScrapeConfig struct {
	JobName     string
	SiteURL     string
	MetricsPath string
	Token       string
	Interval    string
	Timeout     string
}

// NewPrometheusManager creates a new Prometheus configuration manager
func NewPrometheusManager(sshClient *auth.SSHClient, prometheusHost string, manager *compose.Manager) *PrometheusManager {
	return &PrometheusManager{
		sshClient:         sshClient,
		prometheusHost:    prometheusHost,
		prometheusManager: manager,
	}
}

// SetDirectPaths sets direct file paths for Prometheus configuration files
func (pm *PrometheusManager) SetDirectPaths(ymlPath, dockerComposeYmlPath string) {
	pm.prometheusYmlPath = ymlPath
	pm.prometheusDockerComposeYmlPath = dockerComposeYmlPath
}

// GenerateScrapeConfig generates a Prometheus scrape configuration block
func (pm *PrometheusManager) GenerateScrapeConfig(config ScrapeConfig) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("  - job_name: '%s'\n", config.JobName))

	if config.Interval != "" {
		sb.WriteString(fmt.Sprintf("    scrape_interval: %s\n", config.Interval))
	}

	if config.Timeout != "" {
		sb.WriteString(fmt.Sprintf("    scrape_timeout: %s\n", config.Timeout))
	}

	sb.WriteString(fmt.Sprintf("    metrics_path: '%s'\n", config.MetricsPath))

	if config.Token != "" {
		sb.WriteString("    authorization:\n")
		sb.WriteString(fmt.Sprintf("      credentials: '%s'\n", config.Token))
	}

	sb.WriteString("    static_configs:\n")
	sb.WriteString(fmt.Sprintf("      - targets: ['%s']\n", config.SiteURL))
	sb.WriteString("        labels:\n")
	sb.WriteString(fmt.Sprintf("          site: '%s'\n", config.JobName))
	sb.WriteString("          environment: 'production'\n")

	return sb.String()
}

// AddScrapeConfig adds a scrape configuration to prometheus.yml
func (pm *PrometheusManager) AddScrapeConfig(config ScrapeConfig) error {
	// Read current prometheus.yml
	prometheusYmlPath := pm.getPrometheusYmlPath()
	cmd := fmt.Sprintf("cat %s", prometheusYmlPath)

	stdout, stderr, err := pm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to read prometheus.yml: %w (stderr: %s)", err, stderr)
	}

	currentConfig := stdout
	scrapeBlock := pm.GenerateScrapeConfig(config)

	// Check if job already exists
	if strings.Contains(currentConfig, fmt.Sprintf("job_name: '%s'", config.JobName)) {
		return fmt.Errorf("scrape config for job '%s' already exists", config.JobName)
	}

	// Find the scrape_configs section and append
	if !strings.Contains(currentConfig, "scrape_configs:") {
		return fmt.Errorf("prometheus.yml does not contain scrape_configs section")
	}

	// Append the new scrape config before the end of scrape_configs
	newConfig := strings.Replace(currentConfig, "scrape_configs:", "scrape_configs:\n"+scrapeBlock, 1)

	// Write updated config
	writeCmd := fmt.Sprintf("cat > %s << 'PROMETHEUS_EOF'\n%s\nPROMETHEUS_EOF", prometheusYmlPath, newConfig)
	_, stderr, err = pm.sshClient.ExecuteCommand(writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write prometheus.yml: %w (stderr: %s)", err, stderr)
	}

	return nil
}

// RemoveScrapeConfig removes a scrape configuration from prometheus.yml
func (pm *PrometheusManager) RemoveScrapeConfig(jobName string) error {
	prometheusYmlPath := pm.getPrometheusYmlPath()
	cmd := fmt.Sprintf("cat %s", prometheusYmlPath)

	stdout, stderr, err := pm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to read prometheus.yml: %w (stderr: %s)", err, stderr)
	}

	// Remove the job configuration block
	lines := strings.Split(stdout, "\n")
	var newLines []string
	skipUntilNextJob := false

	for i, line := range lines {
		if strings.Contains(line, fmt.Sprintf("job_name: '%s'", jobName)) {
			skipUntilNextJob = true
			continue
		}

		if skipUntilNextJob {
			// Check if we've reached the next job or end of scrape_configs
			if strings.HasPrefix(strings.TrimSpace(line), "- job_name:") ||
				(strings.HasPrefix(line, "  ") && i > 0 && !strings.HasPrefix(lines[i-1], "  ")) {
				skipUntilNextJob = false
				newLines = append(newLines, line)
			}
			continue
		}

		newLines = append(newLines, line)
	}

	newConfig := strings.Join(newLines, "\n")
	writeCmd := fmt.Sprintf("cat > %s << 'PROMETHEUS_EOF'\n%s\nPROMETHEUS_EOF", prometheusYmlPath, newConfig)
	_, stderr, err = pm.sshClient.ExecuteCommand(writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write prometheus.yml: %w (stderr: %s)", err, stderr)
	}

	return nil
}

// ValidateConfig validates Prometheus configuration
func (pm *PrometheusManager) ValidateConfig() error {
	workDir, err := pm.getPrometheusWorkDir()
	if err != nil {
		return err
	}

	cmd := fmt.Sprintf("cd %s && docker compose config -q", workDir)
	_, stderr, err := pm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("prometheus config validation failed: %w (stderr: %s)", err, stderr)
	}
	return nil
}

// RestartPrometheus restarts the Prometheus container
func (pm *PrometheusManager) RestartPrometheus() error {
	return pm.prometheusManager.RestartContainer()
}

// ReloadConfig sends a reload signal to Prometheus
func (pm *PrometheusManager) ReloadConfig() error {
	// Find Prometheus container
	cmd := `docker ps --format '{{.Names}}' | grep prometheus`
	stdout, stderr, err := pm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to find prometheus container: %w (stderr: %s)", err, stderr)
	}

	prometheusContainer := strings.TrimSpace(stdout)
	if prometheusContainer == "" {
		return fmt.Errorf("prometheus container not found")
	}

	// Send SIGHUP to reload config
	reloadCmd := fmt.Sprintf("docker exec %s kill -HUP 1", prometheusContainer)
	_, stderr, err = pm.sshClient.ExecuteCommand(reloadCmd)
	if err != nil {
		return fmt.Errorf("failed to reload prometheus: %w (stderr: %s)", err, stderr)
	}

	return nil
}

// BackupConfig creates a backup of prometheus.yml and optionally docker-compose.yml
func (pm *PrometheusManager) BackupConfig() (string, error) {
	timestampCmd, _, _ := pm.sshClient.ExecuteCommand("date +%s")
	timestamp := strings.TrimSpace(timestampCmd)
	var backupPaths []string

	// If direct paths are provided, back them up individually
	if pm.prometheusYmlPath != "" || pm.prometheusDockerComposeYmlPath != "" {
		if pm.prometheusYmlPath != "" {
			backupPath := fmt.Sprintf("%s.backup.%s", pm.prometheusYmlPath, timestamp)
			cmd := fmt.Sprintf("cp -p %s %s", pm.prometheusYmlPath, backupPath)
			_, stderr, err := pm.sshClient.ExecuteCommand(cmd)
			if err != nil {
				return "", fmt.Errorf("failed to backup prometheus.yml: %w (stderr: %s)", err, stderr)
			}
			backupPaths = append(backupPaths, backupPath)
		}

		if pm.prometheusDockerComposeYmlPath != "" {
			backupPath := fmt.Sprintf("%s.backup.%s", pm.prometheusDockerComposeYmlPath, timestamp)
			cmd := fmt.Sprintf("cp -p %s %s", pm.prometheusDockerComposeYmlPath, backupPath)
			_, stderr, err := pm.sshClient.ExecuteCommand(cmd)
			if err != nil {
				return "", fmt.Errorf("failed to backup docker-compose.yml: %w (stderr: %s)", err, stderr)
			}
			backupPaths = append(backupPaths, backupPath)
		}

		// Return comma-separated backup paths
		return strings.Join(backupPaths, ","), nil
	}

	// Fall back to compose manager backup
	backup, err := pm.prometheusManager.Backup()
	if err != nil {
		return "", err
	}
	return backup.Path, nil
}

// RestoreConfig restores prometheus.yml from backup
func (pm *PrometheusManager) RestoreConfig(backupPath string) error {
	return pm.prometheusManager.Restore(backupPath)
}

// getPrometheusYmlPath returns the path to prometheus.yml
func (pm *PrometheusManager) getPrometheusYmlPath() string {
	// Use direct path if provided
	if pm.prometheusYmlPath != "" {
		return pm.prometheusYmlPath
	}
	// Fall back to inferring from compose path
	return pm.prometheusManager.GetComposePath() + "/../prometheus.yml"
}

// getPrometheusWorkDir returns the Prometheus working directory
func (pm *PrometheusManager) getPrometheusWorkDir() (string, error) {
	cmd := `docker ps --format '{{.Names}}' | grep prometheus`
	stdout, _, err := pm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to find prometheus container: %w", err)
	}

	prometheusContainer := strings.TrimSpace(stdout)
	if prometheusContainer == "" {
		return "", fmt.Errorf("prometheus container not found")
	}

	// Get working directory from container labels
	cmd = fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"'`, prometheusContainer)
	stdout, _, err = pm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get prometheus working directory: %w", err)
	}

	return strings.TrimSpace(stdout), nil
}

// CheckPrometheusHealth checks if Prometheus is healthy
func (pm *PrometheusManager) CheckPrometheusHealth() error {
	cmd := `docker ps --format '{{.Names}}' | grep prometheus`
	stdout, _, err := pm.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("prometheus container not running: %w", err)
	}

	prometheusContainer := strings.TrimSpace(stdout)

	// Check if container is healthy
	healthCmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].State.Health.Status // "healthy"'`, prometheusContainer)
	stdout, _, err = pm.sshClient.ExecuteCommand(healthCmd)
	if err == nil {
		status := strings.TrimSpace(stdout)
		if status == "unhealthy" {
			return fmt.Errorf("prometheus container is unhealthy")
		}
	}

	// Check if Prometheus API is responding
	apiCheckCmd := `curl -s -o /dev/null -w "%{http_code}" http://localhost:9090/-/healthy`
	stdout, _, err = pm.sshClient.ExecuteCommand(apiCheckCmd)
	if err == nil {
		statusCode := strings.TrimSpace(stdout)
		if statusCode != "200" {
			return fmt.Errorf("prometheus API check failed with status %s", statusCode)
		}
	}

	return nil
}
