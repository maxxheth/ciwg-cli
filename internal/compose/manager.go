package compose

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"ciwg-cli/internal/auth"
)

// Manager handles Docker Compose configuration operations
type Manager struct {
	sshClient       *auth.SSHClient
	container       string
	workDir         string
	workDirOverride string
}

// Config represents the structure for compose file operations
type ComposeConfig struct {
	Version  string                 `yaml:"version,omitempty"`
	Services map[string]Service     `yaml:"services,omitempty"`
	Networks map[string]interface{} `yaml:"networks,omitempty"`
	Volumes  map[string]interface{} `yaml:"volumes,omitempty"`
	Configs  map[string]interface{} `yaml:"configs,omitempty"`
	Secrets  map[string]interface{} `yaml:"secrets,omitempty"`
	Raw      map[string]interface{} `yaml:",inline"`
}

// Service represents a Docker Compose service
type Service struct {
	Image         string                 `yaml:"image,omitempty"`
	Build         interface{}            `yaml:"build,omitempty"`
	ContainerName string                 `yaml:"container_name,omitempty"`
	Command       interface{}            `yaml:"command,omitempty"`
	Entrypoint    interface{}            `yaml:"entrypoint,omitempty"`
	Environment   interface{}            `yaml:"environment,omitempty"`
	Ports         []interface{}          `yaml:"ports,omitempty"`
	Volumes       []interface{}          `yaml:"volumes,omitempty"`
	Networks      interface{}            `yaml:"networks,omitempty"`
	DependsOn     interface{}            `yaml:"depends_on,omitempty"`
	Restart       string                 `yaml:"restart,omitempty"`
	Labels        interface{}            `yaml:"labels,omitempty"`
	Healthcheck   interface{}            `yaml:"healthcheck,omitempty"`
	Deploy        interface{}            `yaml:"deploy,omitempty"`
	Raw           map[string]interface{} `yaml:",inline"`
}

// BackupInfo contains information about a backup
type BackupInfo struct {
	Path      string
	Timestamp time.Time
	Container string
}

// NewManager creates a new compose manager instance
func NewManager(sshClient *auth.SSHClient, container string) (*Manager, error) {
	return NewManagerWithOverride(sshClient, container, "")
}

// NewManagerWithOverride creates a new compose manager instance with optional working directory override
func NewManagerWithOverride(sshClient *auth.SSHClient, container string, workDirParent string) (*Manager, error) {
	m := &Manager{
		sshClient:       sshClient,
		container:       container,
		workDirOverride: workDirParent,
	}

	// Get the working directory for the container
	workDir, err := m.getContainerWorkDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get container working directory: %w", err)
	}
	m.workDir = workDir

	return m, nil
}

// getContainerWorkDir retrieves the working directory from container labels
func (m *Manager) getContainerWorkDir() (string, error) {
	// If override is provided, use it with the container name as subdirectory
	if m.workDirOverride != "" {
		// Extract just the directory name from the container working dir if possible
		cmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"'`, m.container)
		stdout, _, err := m.sshClient.ExecuteCommand(cmd)

		if err == nil {
			originalWorkDir := strings.TrimSpace(stdout)
			if originalWorkDir != "" && originalWorkDir != "null" {
				// Extract the base directory name (e.g., "lunaheatingair.com" from "/var/opt/lunaheatingair.com")
				parts := strings.Split(strings.TrimRight(originalWorkDir, "/"), "/")
				if len(parts) > 0 {
					baseName := parts[len(parts)-1]
					overridePath := strings.TrimRight(m.workDirOverride, "/") + "/" + baseName
					return overridePath, nil
				}
			}
		}

		// Fallback: if we can't get original path, try using container name
		containerBaseName := strings.TrimPrefix(m.container, "wp_")
		overridePath := strings.TrimRight(m.workDirOverride, "/") + "/" + containerBaseName
		return overridePath, nil
	}

	// Normal flow: get from container labels
	cmd := fmt.Sprintf(`docker inspect %s | jq -r '.[].Config.Labels."com.docker.compose.project.working_dir"'`, m.container)
	stdout, stderr, err := m.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container: %w (stderr: %s)", err, stderr)
	}

	workDir := strings.TrimSpace(stdout)
	if workDir == "" || workDir == "null" {
		return "", fmt.Errorf("container %s does not have a working directory label", m.container)
	}

	return workDir, nil
}

// GetComposePath returns the path to the docker-compose.yml file
func (m *Manager) GetComposePath() string {
	return fmt.Sprintf("%s/docker-compose.yml", m.workDir)
}

// Read reads and parses the docker-compose.yml file
func (m *Manager) Read() (*ComposeConfig, error) {
	composePath := m.GetComposePath()
	cmd := fmt.Sprintf("cat %s", composePath)

	stdout, stderr, err := m.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read compose file: %w (stderr: %s)", err, stderr)
	}

	var config ComposeConfig
	if err := yaml.Unmarshal([]byte(stdout), &config); err != nil {
		return nil, fmt.Errorf("failed to parse compose file: %w", err)
	}

	return &config, nil
}

// Write writes the compose configuration to the docker-compose.yml file
func (m *Manager) Write(config *ComposeConfig) error {
	// Marshal the configuration to YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal compose config: %w", err)
	}

	composePath := m.GetComposePath()
	// Use a here-doc to avoid shell escaping issues
	cmd := fmt.Sprintf("cat > %s << 'COMPOSE_EOF'\n%s\nCOMPOSE_EOF", composePath, string(data))

	_, stderr, err := m.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to write compose file: %w (stderr: %s)", err, stderr)
	}

	return nil
}

// Backup creates a timestamped backup of the docker-compose.yml file
func (m *Manager) Backup() (*BackupInfo, error) {
	timestamp := time.Now().Format("20060102-150405")
	composePath := m.GetComposePath()
	backupPath := fmt.Sprintf("%s.backup.%s", composePath, timestamp)

	cmd := fmt.Sprintf("cp -p %s %s", composePath, backupPath)
	_, stderr, err := m.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to create backup: %w (stderr: %s)", err, stderr)
	}

	return &BackupInfo{
		Path:      backupPath,
		Timestamp: time.Now(),
		Container: m.container,
	}, nil
}

// Restore restores a compose file from a backup
func (m *Manager) Restore(backupPath string) error {
	composePath := m.GetComposePath()
	cmd := fmt.Sprintf("cp -p %s %s", backupPath, composePath)

	_, stderr, err := m.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to restore backup: %w (stderr: %s)", err, stderr)
	}

	return nil
}

// DeleteBackup removes a backup file
func (m *Manager) DeleteBackup(backupPath string) error {
	cmd := fmt.Sprintf("rm -f %s", backupPath)
	_, stderr, err := m.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to delete backup: %w (stderr: %s)", err, stderr)
	}
	return nil
}

// ListBackups lists all backup files for the compose configuration
func (m *Manager) ListBackups() ([]BackupInfo, error) {
	composePath := m.GetComposePath()
	cmd := fmt.Sprintf("ls -1 %s.backup.* 2>/dev/null || true", composePath)

	stdout, _, err := m.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to list backups: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	backups := make([]BackupInfo, 0)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Extract timestamp from filename
		parts := strings.Split(line, ".backup.")
		if len(parts) != 2 {
			continue
		}

		timestamp, err := time.Parse("20060102-150405", parts[1])
		if err != nil {
			continue
		}

		backups = append(backups, BackupInfo{
			Path:      line,
			Timestamp: timestamp,
			Container: m.container,
		})
	}

	return backups, nil
}

// RestartContainer performs docker compose down and up
func (m *Manager) RestartContainer() error {
	// Change to working directory and restart
	downCmd := fmt.Sprintf("cd %s && docker compose down", m.workDir)
	_, stderr, err := m.sshClient.ExecuteCommand(downCmd)
	if err != nil {
		return fmt.Errorf("failed to stop container: %w (stderr: %s)", err, stderr)
	}

	upCmd := fmt.Sprintf("cd %s && docker compose up -d", m.workDir)
	_, stderr, err = m.sshClient.ExecuteCommand(upCmd)
	if err != nil {
		return fmt.Errorf("failed to start container: %w (stderr: %s)", err, stderr)
	}

	return nil
}

// HealthCheck performs an HTTP health check on the site
func (m *Manager) HealthCheck(siteURL string, timeout time.Duration) error {
	// Ensure URL has protocol
	if !strings.HasPrefix(siteURL, "http://") && !strings.HasPrefix(siteURL, "https://") {
		siteURL = "https://" + siteURL
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Allow redirects, they're usually fine
			return nil
		},
	}

	resp, err := client.Get(siteURL)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer resp.Body.Close()

	// Read body to fully consume response
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}

	return nil
}

// GetServiceValue retrieves a specific value from a service configuration
func (m *Manager) GetServiceValue(serviceName, key string) (interface{}, error) {
	config, err := m.Read()
	if err != nil {
		return nil, err
	}

	service, exists := config.Services[serviceName]
	if !exists {
		return nil, fmt.Errorf("service '%s' not found", serviceName)
	}

	// Use reflection to get the field value
	switch key {
	case "image":
		return service.Image, nil
	case "container_name":
		return service.ContainerName, nil
	case "command":
		return service.Command, nil
	case "entrypoint":
		return service.Entrypoint, nil
	case "environment":
		return service.Environment, nil
	case "ports":
		return service.Ports, nil
	case "volumes":
		return service.Volumes, nil
	case "networks":
		return service.Networks, nil
	case "depends_on":
		return service.DependsOn, nil
	case "restart":
		return service.Restart, nil
	case "labels":
		return service.Labels, nil
	case "healthcheck":
		return service.Healthcheck, nil
	case "deploy":
		return service.Deploy, nil
	default:
		// Check in raw data
		if val, ok := service.Raw[key]; ok {
			return val, nil
		}
		return nil, fmt.Errorf("key '%s' not found in service '%s'", key, serviceName)
	}
}

// SetServiceValue sets a specific value in a service configuration
func (m *Manager) SetServiceValue(serviceName, key string, value interface{}) error {
	config, err := m.Read()
	if err != nil {
		return err
	}

	service, exists := config.Services[serviceName]
	if !exists {
		return fmt.Errorf("service '%s' not found", serviceName)
	}

	// Set the field value
	switch key {
	case "image":
		if v, ok := value.(string); ok {
			service.Image = v
		}
	case "container_name":
		if v, ok := value.(string); ok {
			service.ContainerName = v
		}
	case "command":
		service.Command = value
	case "entrypoint":
		service.Entrypoint = value
	case "environment":
		service.Environment = value
	case "ports":
		if v, ok := value.([]interface{}); ok {
			service.Ports = v
		}
	case "volumes":
		if v, ok := value.([]interface{}); ok {
			service.Volumes = v
		}
	case "networks":
		service.Networks = value
	case "depends_on":
		service.DependsOn = value
	case "restart":
		if v, ok := value.(string); ok {
			service.Restart = v
		}
	case "labels":
		service.Labels = value
	case "healthcheck":
		service.Healthcheck = value
	case "deploy":
		service.Deploy = value
	default:
		// Set in raw data
		if service.Raw == nil {
			service.Raw = make(map[string]interface{})
		}
		service.Raw[key] = value
	}

	config.Services[serviceName] = service
	return m.Write(config)
}

// DeleteServiceKey removes a key from a service configuration
func (m *Manager) DeleteServiceKey(serviceName, key string) error {
	config, err := m.Read()
	if err != nil {
		return err
	}

	service, exists := config.Services[serviceName]
	if !exists {
		return fmt.Errorf("service '%s' not found", serviceName)
	}

	// Delete the field value by setting to zero value
	switch key {
	case "image":
		service.Image = ""
	case "container_name":
		service.ContainerName = ""
	case "command":
		service.Command = nil
	case "entrypoint":
		service.Entrypoint = nil
	case "environment":
		service.Environment = nil
	case "ports":
		service.Ports = nil
	case "volumes":
		service.Volumes = nil
	case "networks":
		service.Networks = nil
	case "depends_on":
		service.DependsOn = nil
	case "restart":
		service.Restart = ""
	case "labels":
		service.Labels = nil
	case "healthcheck":
		service.Healthcheck = nil
	case "deploy":
		service.Deploy = nil
	default:
		// Delete from raw data
		if service.Raw != nil {
			delete(service.Raw, key)
		}
	}

	config.Services[serviceName] = service
	return m.Write(config)
}

// AddService adds a new service to the compose configuration
func (m *Manager) AddService(serviceName string, service Service) error {
	config, err := m.Read()
	if err != nil {
		return err
	}

	if config.Services == nil {
		config.Services = make(map[string]Service)
	}

	if _, exists := config.Services[serviceName]; exists {
		return fmt.Errorf("service '%s' already exists", serviceName)
	}

	config.Services[serviceName] = service
	return m.Write(config)
}

// DeleteService removes a service from the compose configuration
func (m *Manager) DeleteService(serviceName string) error {
	config, err := m.Read()
	if err != nil {
		return err
	}

	if _, exists := config.Services[serviceName]; !exists {
		return fmt.Errorf("service '%s' not found", serviceName)
	}

	delete(config.Services, serviceName)
	return m.Write(config)
}

// AppendPlaceholders appends a YAML block containing placeholders to the compose file.
// The provided block should already be properly formatted YAML or comment lines.
func (m *Manager) AppendPlaceholders(block string) error {
	composePath := m.GetComposePath()
	// Use a here-doc to append to avoid shell escaping issues
	cmd := fmt.Sprintf("cat >> %s << 'PLACEHOLDER_EOF'\n%s\nPLACEHOLDER_EOF", composePath, block)
	_, stderr, err := m.sshClient.ExecuteCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to append placeholders: %w (stderr: %s)", err, stderr)
	}
	return nil
}
