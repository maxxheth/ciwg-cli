package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ciwg-cli/internal/prompress"
)

var prompressCmd = &cobra.Command{
	Use:   "prompress",
	Short: "Install and configure PromPress WordPress metrics plugin",
	Long: `Install and configure PromPress plugin for WordPress sites and integrate with Prometheus.
Handles the complete workflow: backup → install → configure → validate → restart → integrate.`,
}

var prompressInstallCmd = &cobra.Command{
	Use:   "install [hostname]",
	Short: "Install PromPress on WordPress site and configure Prometheus",
	Long: `Complete PromPress installation workflow:
1. Backup WordPress compose configuration
2. Install PromPress plugin (if not already installed)
3. Configure PromPress settings
4. Add Prometheus labels to compose
5. Validate and restart WordPress
6. Verify metrics endpoint
7. Backup Prometheus configuration
8. Add scrape config to Prometheus
9. Validate and restart Prometheus
10. Verify Prometheus health

Automatic rollback on failure.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPrompressInstall,
}

var prompressTestMetricsCmd = &cobra.Command{
	Use:   "test-metrics [hostname]",
	Short: "Test metrics endpoint accessibility",
	Long: `Test if the PromPress metrics endpoint is accessible and responding correctly.
This command helps diagnose 404 errors and other metrics endpoint issues.`,
	Args: cobra.ExactArgs(1),
	RunE: runPrompressTestMetrics,
}

func init() {
	rootCmd.AddCommand(prompressCmd)
	prompressCmd.AddCommand(prompressInstallCmd)
	prompressCmd.AddCommand(prompressTestMetricsCmd)

	// Add standard SSH flags
	addSSHFlags(prompressInstallCmd)
	addSSHFlags(prompressTestMetricsCmd)

	// Container/server flags
	prompressInstallCmd.Flags().String("container", "", "WordPress container name (omit if using --all-containers)")
	prompressInstallCmd.Flags().String("server-range", getEnvWithDefault("SERVER_RANGE", ""), "Server range pattern")
	prompressInstallCmd.Flags().String("working-dir-parent", "", "Override parent directory")
	prompressInstallCmd.Flags().Bool("all-containers", false, "Install on all containers matching prefix")
	prompressInstallCmd.Flags().String("prefix", "wp_", "Container name prefix")

	// PromPress configuration
	prompressInstallCmd.Flags().String("plugin", getEnvWithDefault("PROMPRESS_PLUGIN", "prompress"), "Plugin slug to install")
	prompressInstallCmd.Flags().String("metrics-path", getEnvWithDefault("PROMPRESS_METRICS_PATH", "metrics"), "Metrics endpoint path")
	prompressInstallCmd.Flags().String("metrics-token", getEnvWithDefault("PROMPRESS_TOKEN", ""), "Metrics authentication token")
	prompressInstallCmd.Flags().Bool("enable-auth", getEnvBoolWithDefault("PROMPRESS_ENABLE_AUTH", true), "Enable metrics authentication")
	prompressInstallCmd.Flags().Int("collection-interval", 60, "Metrics collection interval (seconds)")

	// Prometheus configuration
	prompressInstallCmd.Flags().String("prometheus-host", getEnvWithDefault("PROMETHEUS_HOST", ""), "Prometheus server hostname (defaults to WordPress host, env: PROMETHEUS_HOST)")
	prompressInstallCmd.Flags().String("prometheus-container", getEnvWithDefault("PROMETHEUS_CONTAINER", "prometheus"), "Prometheus container name (env: PROMETHEUS_CONTAINER)")
	prompressInstallCmd.Flags().String("prometheus-working-dir", getEnvWithDefault("PROMETHEUS_WORKING_DIR", ""), "Prometheus compose working directory (env: PROMETHEUS_WORKING_DIR)")
	prompressInstallCmd.Flags().String("prom-yml", getEnvWithDefault("PROMETHEUS_YML", ""), "Direct path to prometheus.yml file (env: PROMETHEUS_YML)")
	prompressInstallCmd.Flags().String("prom-dc-yml", getEnvWithDefault("PROMETHEUS_DOCKER_COMPOSE_YML", ""), "Direct path to Prometheus docker-compose.yml file (env: PROMETHEUS_DOCKER_COMPOSE_YML)")
	prompressInstallCmd.Flags().String("prometheus-user", getEnvWithDefault("PROMETHEUS_SSH_USER", ""), "SSH user for Prometheus server (optional, env: PROMETHEUS_SSH_USER)")
	prompressInstallCmd.Flags().String("prometheus-port", getEnvWithDefault("PROMETHEUS_SSH_PORT", "22"), "SSH port for Prometheus server (env: PROMETHEUS_SSH_PORT)")
	prompressInstallCmd.Flags().String("scrape-interval", "15s", "Prometheus scrape interval")
	prompressInstallCmd.Flags().String("scrape-timeout", "10s", "Prometheus scrape timeout")
	prompressInstallCmd.Flags().Bool("skip-prometheus", false, "Skip Prometheus configuration")

	// Workflow options
	prompressInstallCmd.Flags().Duration("health-timeout", 30*time.Second, "Health check timeout")
	prompressInstallCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	prompressInstallCmd.Flags().Bool("no-backup", false, "Skip backup creation (not recommended)")

	// Test metrics command flags
	prompressTestMetricsCmd.Flags().String("container", "", "WordPress container name")
	prompressTestMetricsCmd.Flags().String("metrics-path", getEnvWithDefault("PROMPRESS_METRICS_PATH", "metrics"), "Metrics endpoint path")
	prompressTestMetricsCmd.Flags().String("metrics-token", getEnvWithDefault("PROMPRESS_TOKEN", ""), "Metrics authentication token")
	prompressTestMetricsCmd.Flags().Bool("verbose", false, "Show detailed debugging information")
}

func runPrompressInstall(cmd *cobra.Command, args []string) error {
	if err := validateContainerFlags(cmd); err != nil {
		return err
	}

	allContainers, _ := cmd.Flags().GetBool("all-containers")
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processPrompressInstallForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	if allContainers {
		return processPrompressInstallForAllContainers(cmd, args[0])
	}

	return processPrompressInstall(cmd, args[0])
}

func processPrompressInstall(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")

	// Get configuration
	config := prompress.WorkflowConfig{
		PluginSlug:                     cmd.Flag("plugin").Value.String(),
		MetricsPath:                    cmd.Flag("metrics-path").Value.String(),
		MetricsToken:                   cmd.Flag("metrics-token").Value.String(),
		EnableAuth:                     cmd.Flag("enable-auth").Value.String() == "true",
		CollectionInterval:             60,
		ScrapeInterval:                 cmd.Flag("scrape-interval").Value.String(),
		ScrapeTimeout:                  cmd.Flag("scrape-timeout").Value.String(),
		HealthCheckTimeout:             30 * time.Second,
		PrometheusHost:                 cmd.Flag("prometheus-host").Value.String(),
		PrometheusContainer:            cmd.Flag("prometheus-container").Value.String(),
		PrometheusWorkingDir:           cmd.Flag("prometheus-working-dir").Value.String(),
		PrometheusYmlPath:              cmd.Flag("prom-yml").Value.String(),
		PrometheusDockerComposeYmlPath: cmd.Flag("prom-dc-yml").Value.String(),
		PrometheusSSHUser:              cmd.Flag("prometheus-user").Value.String(),
		PrometheusSSHPort:              cmd.Flag("prometheus-port").Value.String(),
		DryRun:                         cmd.Flag("dry-run").Value.String() == "true",
		SkipPrometheus:                 cmd.Flag("skip-prometheus").Value.String() == "true",
	}

	if interval, err := cmd.Flags().GetInt("collection-interval"); err == nil {
		config.CollectionInterval = interval
	}
	if timeout, err := cmd.Flags().GetDuration("health-timeout"); err == nil {
		config.HealthCheckTimeout = timeout
	}

	// If no Prometheus host specified, use WordPress host
	if config.PrometheusHost == "" {
		config.PrometheusHost = hostname
	}

	fmt.Printf("\n=== Installing PromPress on %s (%s) ===\n\n", container, hostname)

	// Create SSH client
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	// Create compose manager
	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	// Create orchestrator
	orchestrator := prompress.NewOrchestrator(sshClient, container, manager, config.PrometheusHost)

	// Execute workflow
	result, err := orchestrator.Execute(config)

	// Print results
	fmt.Println("\n=== Installation Summary ===")
	for _, step := range result.Steps {
		statusIcon := "✓"
		if step.Status == "failed" {
			statusIcon = "✗"
		} else if step.Status == "skipped" {
			statusIcon = "⊘"
		} else if step.Status == "rollback" {
			statusIcon = "↶"
		}
		fmt.Printf("%s %s\n", statusIcon, step.Name)
		if step.Error != "" {
			fmt.Printf("  Error: %s\n", step.Error)
		}
	}

	if result.Success {
		fmt.Println("\n✓ PromPress installation completed successfully!")
		if result.SiteURL != "" {
			fmt.Printf("\nSite URL: %s\n", result.SiteURL)
		}
		if result.MetricsURL != "" {
			fmt.Printf("Metrics URL: %s\n", result.MetricsURL)
		}
		if result.WordPressBackup != "" {
			fmt.Printf("\nBackups created:\n")
			fmt.Printf("  WordPress: %s\n", result.WordPressBackup)
			if result.PrometheusBackup != "" {
				fmt.Printf("  Prometheus: %s\n", result.PrometheusBackup)
			}
		}
	} else {
		fmt.Println("\n✗ PromPress installation failed")
		if len(result.Errors) > 0 {
			fmt.Println("\nErrors:")
			for _, e := range result.Errors {
				fmt.Printf("  • %s\n", e)
			}
		}
	}

	if err != nil {
		return err
	}

	return nil
}

func runPrompressTestMetrics(cmd *cobra.Command, args []string) error {
	hostname := args[0]
	container, _ := cmd.Flags().GetString("container")
	metricsPath, _ := cmd.Flags().GetString("metrics-path")
	metricsToken, _ := cmd.Flags().GetString("metrics-token")
	verbose, _ := cmd.Flags().GetBool("verbose")

	if container == "" {
		return fmt.Errorf("container name is required (use --container flag)")
	}

	fmt.Printf("\n=== Testing PromPress Metrics Endpoint ===\n")
	fmt.Printf("Host: %s\n", hostname)
	fmt.Printf("Container: %s\n", container)
	fmt.Printf("Metrics Path: /%s\n\n", metricsPath)

	// Create SSH client
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer sshClient.Close()

	// Step 1: Get site URL
	fmt.Printf("1. Getting site URL...\n")
	urlCmd := fmt.Sprintf(`docker exec %s wp --allow-root option get siteurl`, container)
	stdout, stderr, err := sshClient.ExecuteCommand(urlCmd)
	if err != nil || stderr != "" {
		fmt.Printf("   ✗ Failed to get site URL: %s\n", stderr)
		return fmt.Errorf("cannot determine site URL")
	}
	siteURL := strings.TrimSpace(stdout)
	metricsURL := fmt.Sprintf("%s/%s", siteURL, metricsPath)
	fmt.Printf("   ✓ Site URL: %s\n", siteURL)
	fmt.Printf("   ✓ Metrics URL: %s\n\n", metricsURL)

	// Step 2: Check plugin activation
	fmt.Printf("2. Checking PromPress plugin status...\n")
	pluginCmd := fmt.Sprintf(`docker exec %s wp --allow-root plugin list --format=json --field=name,status | jq -r '.[] | select(.name == "prompress") | .status'`, container)
	stdout, stderr, err = sshClient.ExecuteCommand(pluginCmd)
	if err != nil || stderr != "" {
		fmt.Printf("   ⚠ Could not check plugin status: %s\n", stderr)
	} else {
		status := strings.TrimSpace(stdout)
		if status == "active" {
			fmt.Printf("   ✓ PromPress plugin is active\n\n")
		} else if status == "inactive" {
			fmt.Printf("   ✗ PromPress plugin is INACTIVE\n")
			fmt.Printf("   → Run: docker exec %s wp --allow-root plugin activate prompress\n\n", container)
		} else {
			fmt.Printf("   ✗ PromPress plugin not found\n")
			fmt.Printf("   → Install the plugin first\n\n")
		}
	}

	// Step 3: Test endpoint without authentication
	fmt.Printf("3. Testing metrics endpoint (no authentication)...\n")
	curlCmd := fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}" "%s"`, metricsURL)
	stdout, _, err = sshClient.ExecuteCommand(curlCmd)
	if err != nil {
		fmt.Printf("   ✗ Request failed: %v\n\n", err)
	} else {
		statusCode := strings.TrimSpace(stdout)
		if verbose {
			fmt.Printf("   Response status: %s\n", statusCode)
		}
		switch statusCode {
		case "200":
			fmt.Printf("   ✓ Endpoint accessible (no auth required)\n\n")
		case "401", "403":
			fmt.Printf("   ✓ Endpoint exists but requires authentication\n\n")
		case "404":
			fmt.Printf("   ✗ Endpoint not found (404)\n")
			fmt.Printf("   → This usually means:\n")
			fmt.Printf("      • PromPress plugin is not active\n")
			fmt.Printf("      • Rewrite rules need to be flushed\n")
			fmt.Printf("      • Wrong metrics path configured\n\n")
		default:
			fmt.Printf("   ⚠ Unexpected status: %s\n\n", statusCode)
		}
	}

	// Step 4: Test with authentication if token provided
	if metricsToken != "" {
		fmt.Printf("4. Testing metrics endpoint (with authentication)...\n")
		authCurlCmd := fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}" -H "Authorization: Bearer %s" "%s"`, metricsToken, metricsURL)
		stdout, _, err = sshClient.ExecuteCommand(authCurlCmd)
		if err != nil {
			fmt.Printf("   ✗ Request failed: %v\n\n", err)
		} else {
			statusCode := strings.TrimSpace(stdout)
			if verbose {
				fmt.Printf("   Response status: %s\n", statusCode)
			}
			switch statusCode {
			case "200":
				fmt.Printf("   ✓ Endpoint accessible with authentication\n\n")
			case "401", "403":
				fmt.Printf("   ✗ Authentication failed\n")
				fmt.Printf("   → Check that the token matches PromPress settings\n\n")
			case "404":
				fmt.Printf("   ✗ Endpoint not found (404)\n\n")
			default:
				fmt.Printf("   ⚠ Unexpected status: %s\n\n", statusCode)
			}
		}
	} else {
		fmt.Printf("4. Authentication test skipped (no --metrics-token provided)\n\n")
	}

	// Step 5: Check PromPress settings
	fmt.Printf("5. Checking PromPress configuration...\n")
	settingsCmd := fmt.Sprintf(`docker exec %s wp --allow-root option get prompress_settings --format=json`, container)
	stdout, stderr, err = sshClient.ExecuteCommand(settingsCmd)
	if err != nil || stderr != "" {
		fmt.Printf("   ⚠ Could not retrieve settings: %s\n\n", stderr)
	} else {
		fmt.Printf("   ✓ PromPress settings found\n")
		if verbose {
			fmt.Printf("   Settings:\n%s\n\n", stdout)
		} else {
			fmt.Printf("   (use --verbose to see full settings)\n\n")
		}
	}

	// Step 6: Try flushing rewrite rules
	fmt.Printf("6. Troubleshooting suggestions:\n")
	fmt.Printf("   → Flush rewrite rules:\n")
	fmt.Printf("     docker exec %s wp --allow-root rewrite flush\n\n", container)
	fmt.Printf("   → Verify .htaccess doesn't block /metrics:\n")
	fmt.Printf("     Check for rules blocking the metrics path\n\n")
	fmt.Printf("   → Test manually with curl:\n")
	fmt.Printf("     curl -v %s\n\n", metricsURL)
	if metricsToken != "" {
		fmt.Printf("   → Test with authentication:\n")
		fmt.Printf("     curl -v -H \"Authorization: Bearer %s\" %s\n\n", metricsToken, metricsURL)
	}

	return nil
}

func processPrompressInstallForAllContainers(cmd *cobra.Command, hostname string) error {
	return processForAllContainers(cmd, hostname, func(c *cobra.Command, h string) error {
		return processPrompressInstall(c, h)
	})
}

func processPrompressInstallForServerRange(cmd *cobra.Command, serverRange string) error {
	allContainers, _ := cmd.Flags().GetBool("all-containers")

	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return err
	}

	var lastErr error
	successCount := 0
	failCount := 0

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("\n=== Processing %s ===\n", hostname)

		var err error
		if allContainers {
			err = processPrompressInstallForAllContainers(cmd, hostname)
		} else {
			err = processPrompressInstall(cmd, hostname)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ Error on %s: %v\n", hostname, err)
			lastErr = err
			failCount++
			continue
		}
		successCount++
	}

	fmt.Printf("\n=== Summary: %d succeeded, %d failed ===\n", successCount, failCount)
	return lastErr
}
