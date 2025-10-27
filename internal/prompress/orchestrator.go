package prompress

import (
	"fmt"
	"strings"
	"time"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/compose"
)

// Orchestrator coordinates the full PromPress installation workflow
type Orchestrator struct {
	sshClient      *auth.SSHClient
	wpContainer    string
	wpManager      *compose.Manager
	installer      *Installer
	promManager    *PrometheusManager
	prometheusHost string
}

// WorkflowConfig contains configuration for the installation workflow
type WorkflowConfig struct {
	PluginSlug                     string
	MetricsPath                    string
	MetricsToken                   string
	EnableAuth                     bool
	CollectionInterval             int
	ScrapeInterval                 string
	ScrapeTimeout                  string
	HealthCheckTimeout             time.Duration
	PrometheusHost                 string
	PrometheusContainer            string
	PrometheusWorkingDir           string
	PrometheusYmlPath              string // Direct path to prometheus.yml
	PrometheusDockerComposeYmlPath string // Direct path to docker-compose.yml
	PrometheusSSHUser              string
	PrometheusSSHPort              string
	DryRun                         bool
	SkipPrometheus                 bool
}

// WorkflowResult contains the results of the installation workflow
type WorkflowResult struct {
	Success          bool
	WordPressBackup  string
	PrometheusBackup string
	SiteURL          string
	MetricsURL       string
	Errors           []string
	Steps            []WorkflowStep
}

// WorkflowStep represents a step in the workflow
type WorkflowStep struct {
	Name      string
	Status    string // "success", "failed", "skipped", "rollback"
	Error     string
	Timestamp time.Time
}

// NewOrchestrator creates a new workflow orchestrator
func NewOrchestrator(sshClient *auth.SSHClient, wpContainer string, wpManager *compose.Manager, prometheusHost string) *Orchestrator {
	return &Orchestrator{
		sshClient:      sshClient,
		wpContainer:    wpContainer,
		wpManager:      wpManager,
		prometheusHost: prometheusHost,
		installer:      NewInstaller(sshClient, wpContainer, wpManager),
	}
}

// Execute runs the complete PromPress installation workflow
func (o *Orchestrator) Execute(config WorkflowConfig) (*WorkflowResult, error) {
	result := &WorkflowResult{
		Steps: []WorkflowStep{},
	}

	// Step 1: Backup current WordPress compose config
	step := o.recordStep("Backup WordPress configuration")
	wpBackup, err := o.wpManager.Backup()
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
		result.Steps = append(result.Steps, step)
		result.Errors = append(result.Errors, fmt.Sprintf("backup failed: %v", err))
		return result, err
	}
	result.WordPressBackup = wpBackup.Path
	step.Status = "success"
	result.Steps = append(result.Steps, step)
	fmt.Printf("✓ WordPress backup created: %s\n", wpBackup.Path)

	// Step 2: Get site URL
	step = o.recordStep("Get WordPress site URL")
	siteURL, err := o.installer.GetSiteURL()
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
		result.Steps = append(result.Steps, step)
		result.Errors = append(result.Errors, fmt.Sprintf("get site URL failed: %v", err))
		return result, err
	}
	result.SiteURL = siteURL
	step.Status = "success"
	result.Steps = append(result.Steps, step)
	fmt.Printf("✓ Site URL: %s\n", siteURL)

	// Step 3: Check if plugin is already installed
	step = o.recordStep("Check PromPress plugin status")
	installed, err := o.installer.IsPluginInstalled(config.PluginSlug)
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
		result.Steps = append(result.Steps, step)
		result.Errors = append(result.Errors, fmt.Sprintf("plugin check failed: %v", err))
		return result, err
	}
	step.Status = "success"
	result.Steps = append(result.Steps, step)

	// Step 4: Install plugin if not already installed
	if !installed {
		step = o.recordStep("Install PromPress plugin")
		if config.DryRun {
			step.Status = "skipped"
			fmt.Println("⊘ Dry run: Would install PromPress plugin")
		} else {
			if err := o.installer.InstallPlugin(config.PluginSlug); err != nil {
				step.Status = "failed"
				step.Error = err.Error()
				result.Steps = append(result.Steps, step)
				result.Errors = append(result.Errors, fmt.Sprintf("plugin installation failed: %v", err))
				o.rollbackWordPress(wpBackup.Path)
				return result, err
			}
			step.Status = "success"
			fmt.Println("✓ PromPress plugin installed and activated")
		}
		result.Steps = append(result.Steps, step)
	} else {
		fmt.Println("✓ PromPress plugin already installed")
	}

	// Step 5: Configure plugin
	step = o.recordStep("Configure PromPress settings")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would configure PromPress settings")
	} else {
		installConfig := InstallConfig{
			MetricsPath:        config.MetricsPath,
			MetricsToken:       config.MetricsToken,
			EnableAuth:         config.EnableAuth,
			CollectionInterval: config.CollectionInterval,
		}
		if err := o.installer.ConfigurePlugin(installConfig); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("plugin configuration failed: %v", err))
			o.rollbackWordPress(wpBackup.Path)
			return result, err
		}
		step.Status = "success"
		fmt.Println("✓ PromPress settings configured")
	}
	result.Steps = append(result.Steps, step)

	// Step 6: Add Prometheus labels to compose config
	step = o.recordStep("Add Prometheus labels to compose")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would add Prometheus labels")
	} else {
		if err := o.installer.AddPrometheusLabel(config.MetricsPath, config.MetricsToken); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("add labels failed: %v", err))
			o.rollbackWordPress(wpBackup.Path)
			return result, err
		}
		step.Status = "success"
		fmt.Println("✓ Prometheus labels added to compose")
	}
	result.Steps = append(result.Steps, step)

	// Step 7: Validate WordPress compose config
	step = o.recordStep("Validate WordPress compose configuration")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would validate compose config")
	} else {
		if err := o.validateComposeConfig(o.wpManager); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("compose validation failed: %v", err))
			o.rollbackWordPress(wpBackup.Path)
			return result, err
		}
		step.Status = "success"
		fmt.Println("✓ Compose configuration validated")
	}
	result.Steps = append(result.Steps, step)

	// Step 8: Restart WordPress container
	step = o.recordStep("Restart WordPress container")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would restart WordPress container")
	} else {
		fmt.Println("Restarting WordPress container...")
		if err := o.installer.RestartWordPress(); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("restart failed: %v", err))
			o.rollbackWordPress(wpBackup.Path)
			return result, err
		}
		step.Status = "success"
		fmt.Println("✓ WordPress container restarted")
	}
	result.Steps = append(result.Steps, step)

	// Step 9: WordPress health check
	step = o.recordStep("WordPress site health check")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would perform health check")
	} else {
		fmt.Println("Waiting for site to be ready...")
		time.Sleep(5 * time.Second)
		if err := o.installer.HealthCheck(siteURL, config.HealthCheckTimeout); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("health check failed: %v", err))
			fmt.Println("✗ Health check failed, rolling back...")
			o.rollbackWordPress(wpBackup.Path)
			return result, err
		}
		step.Status = "success"
		fmt.Println("✓ WordPress site is healthy")
	}
	result.Steps = append(result.Steps, step)

	// Step 10: Verify metrics endpoint
	step = o.recordStep("Verify metrics endpoint")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would verify metrics endpoint")
	} else {
		metricsURL := fmt.Sprintf("%s/%s", siteURL, config.MetricsPath)
		result.MetricsURL = metricsURL
		if err := o.installer.VerifyMetricsEndpoint(siteURL, config.MetricsPath, config.MetricsToken); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("metrics verification failed: %v", err))
			fmt.Printf("⚠ Warning: Metrics endpoint not accessible: %v\n", err)
			// Don't rollback, this might be a firewall/network issue
		} else {
			step.Status = "success"
			fmt.Printf("✓ Metrics endpoint verified: %s\n", metricsURL)
		}
	}
	result.Steps = append(result.Steps, step)

	// Skip Prometheus steps if requested
	if config.SkipPrometheus {
		fmt.Println("\n⊘ Skipping Prometheus configuration")
		result.Success = true
		return result, nil
	}

	// Step 11: Initialize Prometheus manager
	step = o.recordStep("Initialize Prometheus manager")
	promSSHClient, err := o.createPrometheusSSHClient(config)
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
		result.Steps = append(result.Steps, step)
		result.Errors = append(result.Errors, fmt.Sprintf("prometheus SSH failed: %v", err))
		fmt.Printf("⚠ Warning: Could not connect to Prometheus host: %v\n", err)
		result.Success = true // WordPress part succeeded
		return result, nil
	}
	defer promSSHClient.Close()

	// Find and create Prometheus manager
	promManager, err := o.createPrometheusManager(promSSHClient, config)
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
		result.Steps = append(result.Steps, step)
		result.Errors = append(result.Errors, fmt.Sprintf("prometheus manager creation failed: %v", err))
		fmt.Printf("⚠ Warning: Could not create Prometheus manager: %v\n", err)
		result.Success = true
		return result, nil
	}
	o.promManager = promManager
	step.Status = "success"
	result.Steps = append(result.Steps, step)
	fmt.Println("✓ Prometheus manager initialized")

	// Step 12: Backup Prometheus config
	step = o.recordStep("Backup Prometheus configuration")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would backup Prometheus config")
	} else {
		promBackup, err := o.promManager.BackupConfig()
		if err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("prometheus backup failed: %v", err))
			fmt.Printf("⚠ Warning: Could not backup Prometheus config: %v\n", err)
			result.Success = true
			return result, nil
		}
		result.PrometheusBackup = promBackup
		step.Status = "success"
		fmt.Printf("✓ Prometheus backup created: %s\n", promBackup)
	}
	result.Steps = append(result.Steps, step)

	// Step 13: Add scrape config to Prometheus
	step = o.recordStep("Add scrape config to Prometheus")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would add scrape config")
	} else {
		jobName := o.generateJobName(siteURL)
		scrapeConfig := ScrapeConfig{
			JobName:     jobName,
			SiteURL:     siteURL,
			MetricsPath: "/" + config.MetricsPath,
			Token:       config.MetricsToken,
			Interval:    config.ScrapeInterval,
			Timeout:     config.ScrapeTimeout,
		}

		if err := o.promManager.AddScrapeConfig(scrapeConfig); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("add scrape config failed: %v", err))
			fmt.Printf("⚠ Warning: Could not add scrape config: %v\n", err)
			result.Success = true
			return result, nil
		}
		step.Status = "success"
		fmt.Println("✓ Scrape config added to Prometheus")
	}
	result.Steps = append(result.Steps, step)

	// Step 14: Validate Prometheus config
	step = o.recordStep("Validate Prometheus configuration")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would validate Prometheus config")
	} else {
		if err := o.promManager.ValidateConfig(); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("prometheus validation failed: %v", err))
			fmt.Println("✗ Prometheus config validation failed, rolling back...")
			if result.PrometheusBackup != "" {
				o.rollbackPrometheus(result.PrometheusBackup)
			}
			result.Success = true // WordPress part still succeeded
			return result, nil
		}
		step.Status = "success"
		fmt.Println("✓ Prometheus configuration validated")
	}
	result.Steps = append(result.Steps, step)

	// Step 15: Restart Prometheus
	step = o.recordStep("Restart Prometheus")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would restart Prometheus")
	} else {
		fmt.Println("Restarting Prometheus...")
		if err := o.promManager.RestartPrometheus(); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("prometheus restart failed: %v", err))
			fmt.Println("✗ Prometheus restart failed, rolling back...")
			if result.PrometheusBackup != "" {
				o.rollbackPrometheus(result.PrometheusBackup)
			}
			result.Success = true
			return result, nil
		}
		step.Status = "success"
		fmt.Println("✓ Prometheus restarted")
	}
	result.Steps = append(result.Steps, step)

	// Step 16: Prometheus health check
	step = o.recordStep("Prometheus health check")
	if config.DryRun {
		step.Status = "skipped"
		fmt.Println("⊘ Dry run: Would check Prometheus health")
	} else {
		fmt.Println("Waiting for Prometheus to be ready...")
		time.Sleep(5 * time.Second)
		if err := o.promManager.CheckPrometheusHealth(); err != nil {
			step.Status = "failed"
			step.Error = err.Error()
			result.Steps = append(result.Steps, step)
			result.Errors = append(result.Errors, fmt.Sprintf("prometheus health check failed: %v", err))
			fmt.Println("✗ Prometheus health check failed, rolling back...")
			if result.PrometheusBackup != "" {
				o.rollbackPrometheus(result.PrometheusBackup)
			}
			result.Success = true
			return result, nil
		}
		step.Status = "success"
		fmt.Println("✓ Prometheus is healthy")
	}
	result.Steps = append(result.Steps, step)

	result.Success = true
	return result, nil
}

// Helper methods

func (o *Orchestrator) recordStep(name string) WorkflowStep {
	return WorkflowStep{
		Name:      name,
		Timestamp: time.Now(),
	}
}

func (o *Orchestrator) validateComposeConfig(manager *compose.Manager) error {
	// Get the working directory
	composePath := manager.GetComposePath()
	workDir := composePath[:len(composePath)-len("/docker-compose.yml")]

	cmd := fmt.Sprintf("cd %s && docker compose config -q", workDir)
	_, stderr, err := o.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("validation failed: %w (stderr: %s)", err, stderr)
	}
	return nil
}

func (o *Orchestrator) rollbackWordPress(backupPath string) {
	fmt.Println("\n=== Rolling back WordPress configuration ===")
	if err := o.wpManager.Restore(backupPath); err != nil {
		fmt.Printf("✗ Failed to restore backup: %v\n", err)
		return
	}
	fmt.Println("✓ Configuration restored")

	fmt.Println("Restarting container...")
	if err := o.installer.RestartWordPress(); err != nil {
		fmt.Printf("✗ Failed to restart container: %v\n", err)
		return
	}
	fmt.Println("✓ Container restarted")
}

func (o *Orchestrator) rollbackPrometheus(backupPath string) {
	fmt.Println("\n=== Rolling back Prometheus configuration ===")
	if err := o.promManager.RestoreConfig(backupPath); err != nil {
		fmt.Printf("✗ Failed to restore Prometheus backup: %v\n", err)
		return
	}
	fmt.Println("✓ Prometheus configuration restored")

	fmt.Println("Restarting Prometheus...")
	if err := o.promManager.RestartPrometheus(); err != nil {
		fmt.Printf("✗ Failed to restart Prometheus: %v\n", err)
		return
	}
	fmt.Println("✓ Prometheus restarted")
}

func (o *Orchestrator) createPrometheusSSHClient(config WorkflowConfig) (*auth.SSHClient, error) {
	// Reuse existing SSH client if same host and no custom SSH config
	if config.PrometheusHost == "" || (config.PrometheusHost == o.prometheusHost && config.PrometheusSSHUser == "" && config.PrometheusSSHPort == "22") {
		return o.sshClient, nil
	}

	// Create new SSH client for Prometheus host with custom config
	sshConfig := auth.SSHConfig{
		Hostname:  config.PrometheusHost,
		Port:      config.PrometheusSSHPort,
		Username:  config.PrometheusSSHUser,
		UseAgent:  true, // Default to using agent
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	if sshConfig.Port == "" {
		sshConfig.Port = "22"
	}

	if sshConfig.Username == "" {
		return nil, fmt.Errorf("prometheus SSH user must be specified when using a separate prometheus host")
	}

	return auth.NewSSHClient(sshConfig)
}

func (o *Orchestrator) createPrometheusManager(sshClient *auth.SSHClient, config WorkflowConfig) (*PrometheusManager, error) {
	prometheusContainer := config.PrometheusContainer
	if prometheusContainer == "" {
		// Fall back to discovery if not specified
		cmd := `docker ps --format '{{.Names}}' | grep prometheus`
		stdout, _, err := sshClient.ExecuteCommand(cmd)
		if err != nil {
			return nil, fmt.Errorf("prometheus container not found (use --prometheus-container to specify): %w", err)
		}
		prometheusContainer = strings.TrimSpace(stdout)
		if prometheusContainer == "" {
			return nil, fmt.Errorf("prometheus container not running")
		}
	}

	// Create compose manager for Prometheus
	var promManager *compose.Manager
	var err error

	if config.PrometheusWorkingDir != "" {
		// Use explicit working directory
		promManager, err = compose.NewManagerWithOverride(sshClient, prometheusContainer, config.PrometheusWorkingDir)
	} else {
		// Use standard discovery
		promManager, err = compose.NewManager(sshClient, prometheusContainer)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create prometheus manager: %w", err)
	}

	pm := NewPrometheusManager(sshClient, o.prometheusHost, promManager)

	// Set direct paths if provided
	if config.PrometheusYmlPath != "" || config.PrometheusDockerComposeYmlPath != "" {
		pm.SetDirectPaths(config.PrometheusYmlPath, config.PrometheusDockerComposeYmlPath)
	}

	return pm, nil
}

func (o *Orchestrator) generateJobName(siteURL string) string {
	// Extract domain from URL for job name
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(siteURL, "https://"), "http://"), "/")
	domain := parts[0]
	// Remove www. prefix
	domain = strings.TrimPrefix(domain, "www.")
	// Replace dots and dashes with underscores
	jobName := strings.ReplaceAll(strings.ReplaceAll(domain, ".", "_"), "-", "_")
	return "wordpress_" + jobName
}
