package backup

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// BackupConfig represents the YAML configuration for custom backup operations
type BackupConfig struct {
	// Version of the config schema
	Version string `yaml:"version"`

	// Default settings applied to all containers unless overridden
	Defaults ConfigDefaults `yaml:"defaults,omitempty"`

	// List of containers to back up
	Containers []ContainerConfig `yaml:"containers"`
}

// ConfigDefaults contains default settings for all containers
type ConfigDefaults struct {
	DatabaseType      string `yaml:"database_type,omitempty"`
	ExportDir         string `yaml:"export_dir,omitempty"`
	DatabaseExportDir string `yaml:"database_export_dir,omitempty"`
	DatabaseName      string `yaml:"database_name,omitempty"`
	DatabaseUser      string `yaml:"database_user,omitempty"`
	// BucketPath is an optional prefix to use inside the Minio bucket
	// (e.g. "production/backups"). When set it will be applied to all
	// containers unless a container explicitly overrides it.
	BucketPath string            `yaml:"bucket_path,omitempty"`
	Env        map[string]string `yaml:"env,omitempty"`
}

// ContainerConfig defines the backup configuration for a single container or app
type ContainerConfig struct {
	// Name of the container (or working directory path)
	Name string `yaml:"name"`

	// Optional label for the backup object name (defaults to container name)
	Label string `yaml:"label,omitempty"`

	// Type of application: wordpress, custom, postgres, mysql, etc.
	Type string `yaml:"type"`

	// Database configuration
	Database DatabaseConfig `yaml:"database,omitempty"`

	// Paths configuration
	Paths PathsConfig `yaml:"paths,omitempty"`

	// Pre-backup commands to run
	PreBackupCommands []string `yaml:"pre_backup_commands,omitempty"`

	// Post-backup commands to run
	PostBackupCommands []string `yaml:"post_backup_commands,omitempty"`

	// Files/directories to exclude from backup
	Excludes []string `yaml:"excludes,omitempty"`

	// Additional environment variables
	Env map[string]string `yaml:"env,omitempty"`

	// Skip this container if true
	Skip bool `yaml:"skip,omitempty"`

	// Optional bucket path prefix for this container. If set this overrides
	// the top-level defaults.bucket_path value and will be used as the
	// prefix within the Minio bucket (e.g. "customer-a/backups").
	BucketPath string `yaml:"bucket_path,omitempty"`
}

// DatabaseConfig defines database-specific configuration
type DatabaseConfig struct {
	// Type: postgres, mysql, mariadb, mongodb, redis, etc.
	Type string `yaml:"type"`

	// Container name if database is in a separate container
	Container string `yaml:"container,omitempty"`

	// Database name
	Name string `yaml:"name,omitempty"`

	// Database user
	User string `yaml:"user,omitempty"`

	// Database password (or env var name with $ prefix)
	Password string `yaml:"password,omitempty"`

	// Database host
	Host string `yaml:"host,omitempty"`

	// Database port
	Port int `yaml:"port,omitempty"`

	// Export format: sql, dump, custom
	ExportFormat string `yaml:"export_format,omitempty"`

	// Custom export command (overrides auto-generated command)
	ExportCommand string `yaml:"export_command,omitempty"`

	// Path where database export should be saved (relative to working dir)
	ExportPath string `yaml:"export_path,omitempty"`
}

// PathsConfig defines custom paths for backup operations
type PathsConfig struct {
	// Working directory (overrides auto-detected path)
	WorkingDir string `yaml:"working_dir,omitempty"`

	// Directory containing application files to backup
	AppDir string `yaml:"app_dir,omitempty"`

	// Directory where database exports should be saved before backup
	DatabaseExportDir string `yaml:"database_export_dir,omitempty"`

	// Additional directories to include in backup
	Include []string `yaml:"include,omitempty"`
}

// LoadConfigFromFile loads and parses a backup configuration YAML file
func LoadConfigFromFile(path string) (*BackupConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config BackupConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// Set defaults
	if config.Version == "" {
		config.Version = "1"
	}

	// Validate
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &config, nil
}

// Validate checks if the configuration is valid
func (c *BackupConfig) Validate() error {
	if len(c.Containers) == 0 {
		return fmt.Errorf("at least one container must be defined")
	}

	for i, container := range c.Containers {
		if container.Name == "" {
			return fmt.Errorf("container[%d]: name is required", i)
		}
		if container.Type == "" {
			return fmt.Errorf("container[%d]: type is required", i)
		}
		// Validate database config if type requires it
		if container.Type == "postgres" || container.Type == "mysql" || container.Type == "mariadb" {
			if container.Database.Type == "" {
				container.Database.Type = container.Type
			}
		}
	}

	return nil
}

// ApplyDefaults applies default settings to a container config
func (c *BackupConfig) ApplyDefaults(container *ContainerConfig) {
	if container.Database.Type == "" && c.Defaults.DatabaseType != "" {
		container.Database.Type = c.Defaults.DatabaseType
	}
	if container.Database.Name == "" && c.Defaults.DatabaseName != "" {
		container.Database.Name = c.Defaults.DatabaseName
	}
	if container.Database.User == "" && c.Defaults.DatabaseUser != "" {
		container.Database.User = c.Defaults.DatabaseUser
	}
	if container.Paths.DatabaseExportDir == "" && c.Defaults.DatabaseExportDir != "" {
		container.Paths.DatabaseExportDir = c.Defaults.DatabaseExportDir
	}

	// Apply bucket path default
	if container.BucketPath == "" && c.Defaults.BucketPath != "" {
		container.BucketPath = c.Defaults.BucketPath
	}

	// Merge environment variables
	if container.Env == nil {
		container.Env = make(map[string]string)
	}
	for k, v := range c.Defaults.Env {
		if _, exists := container.Env[k]; !exists {
			container.Env[k] = v
		}
	}
}
