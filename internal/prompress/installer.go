package prompress

import (
	"fmt"
	"strings"
	"time"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/compose"
)

// Installer handles PromPress plugin installation and configuration
type Installer struct {
	sshClient *auth.SSHClient
	container string
	manager   *compose.Manager
}

// InstallConfig contains configuration for PromPress installation
type InstallConfig struct {
	PrometheusHost     string
	PrometheusPort     string
	MetricsPath        string
	MetricsToken       string
	EnableAuth         bool
	CollectionInterval int // seconds
}

// NewInstaller creates a new PromPress installer instance
func NewInstaller(sshClient *auth.SSHClient, container string, manager *compose.Manager) *Installer {
	return &Installer{
		sshClient: sshClient,
		container: container,
		manager:   manager,
	}
}

// InstallPlugin installs the PromPress plugin via WP-CLI
func (i *Installer) InstallPlugin(pluginSlug string) error {
	cmd := fmt.Sprintf(`docker exec -u 0 %s wp plugin install %s --activate --allow-root`, i.container, pluginSlug)
	_, stderr, err := i.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to install plugin: %w (stderr: %s)", err, stderr)
	}
	return nil
}

// IsPluginInstalled checks if PromPress is already installed
func (i *Installer) IsPluginInstalled(pluginSlug string) (bool, error) {
	cmd := fmt.Sprintf(`docker exec -u 0 %s wp plugin is-installed %s --allow-root`, i.container, pluginSlug)
	_, _, err := i.sshClient.ExecuteCommand(cmd)
	return err == nil, nil
}

// ConfigurePlugin configures PromPress settings via WP-CLI
func (i *Installer) ConfigurePlugin(config InstallConfig) error {
	// Set metrics endpoint path
	if config.MetricsPath != "" {
		cmd := fmt.Sprintf(`docker exec -u 0 %s wp option update prompress_metrics_path '%s' --allow-root`,
			i.container, config.MetricsPath)
		if _, stderr, err := i.sshClient.ExecuteCommand(cmd); err != nil {
			return fmt.Errorf("failed to set metrics path: %w (stderr: %s)", err, stderr)
		}
	}

	// Set authentication token
	if config.EnableAuth && config.MetricsToken != "" {
		cmd := fmt.Sprintf(`docker exec -u 0 %s wp option update prompress_auth_token '%s' --allow-root`,
			i.container, config.MetricsToken)
		if _, stderr, err := i.sshClient.ExecuteCommand(cmd); err != nil {
			return fmt.Errorf("failed to set auth token: %w (stderr: %s)", err, stderr)
		}
	}

	// Enable/disable authentication
	authValue := "0"
	if config.EnableAuth {
		authValue = "1"
	}
	cmd := fmt.Sprintf(`docker exec -u 0 %s wp option update prompress_enable_auth '%s' --allow-root`,
		i.container, authValue)
	if _, stderr, err := i.sshClient.ExecuteCommand(cmd); err != nil {
		return fmt.Errorf("failed to set auth setting: %w (stderr: %s)", err, stderr)
	}

	// Set collection interval
	if config.CollectionInterval > 0 {
		cmd := fmt.Sprintf(`docker exec -u 0 %s wp option update prompress_collection_interval '%d' --allow-root`,
			i.container, config.CollectionInterval)
		if _, stderr, err := i.sshClient.ExecuteCommand(cmd); err != nil {
			return fmt.Errorf("failed to set collection interval: %w (stderr: %s)", err, stderr)
		}
	}

	return nil
}

// VerifyMetricsEndpoint checks if the metrics endpoint is accessible
func (i *Installer) VerifyMetricsEndpoint(siteURL, metricsPath, token string) error {
	// Build metrics URL
	metricsURL := strings.TrimRight(siteURL, "/") + "/" + strings.TrimLeft(metricsPath, "/")

	// Build curl command with optional authentication
	curlCmd := fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}"`)
	if token != "" {
		curlCmd = fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}" -H "Authorization: Bearer %s"`, token)
	}

	cmd := fmt.Sprintf(`%s %s`, curlCmd, metricsURL)

	stdout, stderr, err := i.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to verify metrics endpoint: %w (stderr: %s)", err, stderr)
	}

	statusCode := strings.TrimSpace(stdout)
	if statusCode != "200" {
		// Provide detailed troubleshooting information
		return fmt.Errorf("metrics endpoint returned status %s\n"+
			"  URL tested: %s\n"+
			"  Troubleshooting:\n"+
			"    1. Check if PromPress plugin is active: wp plugin is-active prompress --allow-root\n"+
			"    2. Check WordPress rewrite rules: wp rewrite flush --allow-root\n"+
			"    3. Test endpoint manually: curl -I %s\n"+
			"    4. Check .htaccess or nginx config for URL rewrite issues\n"+
			"    5. Verify metrics path in PromPress settings matches '%s'",
			statusCode, metricsURL, metricsURL, metricsPath)
	}

	return nil
}

// AddPrometheusLabel adds the prompress label to the WordPress service
func (i *Installer) AddPrometheusLabel(metricsPath, token string) error {
	config, err := i.manager.Read()
	if err != nil {
		return fmt.Errorf("failed to read compose config: %w", err)
	}

	// Find the WordPress service (typically matches container name without wp_ prefix)
	var serviceName string
	for name := range config.Services {
		if strings.Contains(name, strings.TrimPrefix(i.container, "wp_")) {
			serviceName = name
			break
		}
	}

	if serviceName == "" {
		return fmt.Errorf("could not find service for container %s", i.container)
	}

	service := config.Services[serviceName]

	// Ensure labels exist
	var labels []string
	switch v := service.Labels.(type) {
	case []interface{}:
		for _, label := range v {
			if str, ok := label.(string); ok {
				labels = append(labels, str)
			}
		}
	case []string:
		labels = v
	case map[string]interface{}:
		// Convert map format to array format
		for key, val := range v {
			labels = append(labels, fmt.Sprintf("%s=%v", key, val))
		}
	case map[string]string:
		for key, val := range v {
			labels = append(labels, fmt.Sprintf("%s=%s", key, val))
		}
	default:
		labels = []string{}
	}

	// Add PromPress labels
	labels = append(labels, "prompress.enabled=true")
	labels = append(labels, fmt.Sprintf("prompress.path=%s", metricsPath))
	if token != "" {
		labels = append(labels, fmt.Sprintf("prompress.token=%s", token))
	}

	service.Labels = labels
	config.Services[serviceName] = service

	return i.manager.Write(config)
}

// GetSiteURL retrieves the WordPress site URL
func (i *Installer) GetSiteURL() (string, error) {
	cmd := fmt.Sprintf(`docker exec -u 0 %s wp option get siteurl --allow-root`, i.container)
	stdout, stderr, err := i.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get site URL: %w (stderr: %s)", err, stderr)
	}
	return strings.TrimSpace(stdout), nil
}

// RestartWordPress restarts the WordPress container
func (i *Installer) RestartWordPress() error {
	return i.manager.RestartContainer()
}

// HealthCheck performs a health check on the WordPress site
func (i *Installer) HealthCheck(siteURL string, timeout time.Duration) error {
	return i.manager.HealthCheck(siteURL, timeout)
}
