package cmd

import (
	"fmt"
	"os"
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

func init() {
	rootCmd.AddCommand(prompressCmd)
	prompressCmd.AddCommand(prompressInstallCmd)

	// Add standard SSH flags
	addSSHFlags(prompressInstallCmd)

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
	prompressInstallCmd.Flags().String("prometheus-user", getEnvWithDefault("PROMETHEUS_SSH_USER", ""), "SSH user for Prometheus server (optional, env: PROMETHEUS_SSH_USER)")
	prompressInstallCmd.Flags().String("prometheus-port", getEnvWithDefault("PROMETHEUS_SSH_PORT", "22"), "SSH port for Prometheus server (env: PROMETHEUS_SSH_PORT)")
	prompressInstallCmd.Flags().String("scrape-interval", "15s", "Prometheus scrape interval")
	prompressInstallCmd.Flags().String("scrape-timeout", "10s", "Prometheus scrape timeout")
	prompressInstallCmd.Flags().Bool("skip-prometheus", false, "Skip Prometheus configuration")

	// Workflow options
	prompressInstallCmd.Flags().Duration("health-timeout", 30*time.Second, "Health check timeout")
	prompressInstallCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	prompressInstallCmd.Flags().Bool("no-backup", false, "Skip backup creation (not recommended)")
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
		PluginSlug:           cmd.Flag("plugin").Value.String(),
		MetricsPath:          cmd.Flag("metrics-path").Value.String(),
		MetricsToken:         cmd.Flag("metrics-token").Value.String(),
		EnableAuth:           cmd.Flag("enable-auth").Value.String() == "true",
		CollectionInterval:   60,
		ScrapeInterval:       cmd.Flag("scrape-interval").Value.String(),
		ScrapeTimeout:        cmd.Flag("scrape-timeout").Value.String(),
		HealthCheckTimeout:   30 * time.Second,
		PrometheusHost:       cmd.Flag("prometheus-host").Value.String(),
		PrometheusContainer:  cmd.Flag("prometheus-container").Value.String(),
		PrometheusWorkingDir: cmd.Flag("prometheus-working-dir").Value.String(),
		PrometheusSSHUser:    cmd.Flag("prometheus-user").Value.String(),
		PrometheusSSHPort:    cmd.Flag("prometheus-port").Value.String(),
		DryRun:               cmd.Flag("dry-run").Value.String() == "true",
		SkipPrometheus:       cmd.Flag("skip-prometheus").Value.String() == "true",
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
