package backup

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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

	// Retention policy rulesets for backup lifecycle management
	Rulesets map[string]RetentionRuleset `yaml:"rulesets,omitempty"`
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

// RetentionRuleset defines a single retention policy rule
type RetentionRuleset struct {
	// OlderThan specifies the age threshold (e.g., "7 days", "6 months", "1 year")
	OlderThan string `yaml:"older_than"`

	// Exclude specifies days to exclude from this rule (e.g., "Sunday", "every 7 days", "last day of month")
	Exclude string `yaml:"exclude,omitempty"`

	// Action specifies what to do with matching backups: "delete", "migrate_to_glacier", "keep"
	Action string `yaml:"action,omitempty"`

	// TargetStorage specifies the storage backend this rule applies to: "s3", "minio", "both"
	TargetStorage string `yaml:"target_storage,omitempty"`
}

// LoadRetentionPolicyFromFile loads retention policy from a YAML file
func LoadRetentionPolicyFromFile(path string) (map[string]RetentionRuleset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read retention policy file: %w", err)
	}

	var config struct {
		Rulesets map[string]RetentionRuleset `yaml:"rulesets"`
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse retention policy YAML: %w", err)
	}

	// Validate rulesets
	if err := ValidateRetentionRulesets(config.Rulesets); err != nil {
		return nil, fmt.Errorf("invalid retention policy: %w", err)
	}

	return config.Rulesets, nil
}

// ValidateRetentionRulesets validates retention policy rulesets
func ValidateRetentionRulesets(rulesets map[string]RetentionRuleset) error {
	for name, ruleset := range rulesets {
		if ruleset.OlderThan == "" {
			return fmt.Errorf("ruleset '%s': older_than is required", name)
		}

		// Parse the older_than to ensure it's valid
		if _, err := ParseHumanDuration(ruleset.OlderThan); err != nil {
			return fmt.Errorf("ruleset '%s': invalid older_than value: %w", name, err)
		}

		// Validate action if specified
		if ruleset.Action != "" {
			validActions := map[string]bool{"delete": true, "migrate_to_glacier": true, "keep": true}
			if !validActions[ruleset.Action] {
				return fmt.Errorf("ruleset '%s': invalid action '%s' (must be 'delete', 'migrate_to_glacier', or 'keep')", name, ruleset.Action)
			}
		}

		// Validate target_storage if specified
		if ruleset.TargetStorage != "" {
			validTargets := map[string]bool{"s3": true, "minio": true, "both": true}
			if !validTargets[ruleset.TargetStorage] {
				return fmt.Errorf("ruleset '%s': invalid target_storage '%s' (must be 's3', 'minio', or 'both')", name, ruleset.TargetStorage)
			}
		}
	}

	return nil
}

// ParseHumanDuration parses human-readable duration strings like "7 days", "6 months", "1 year"
// Similar to Carbon PHP library functionality
func ParseHumanDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))

	// Split into number and unit
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid duration format: expected 'number unit' (e.g., '7 days')")
	}

	value, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid number in duration: %w", err)
	}

	unit := parts[1]
	// Handle both singular and plural forms
	switch unit {
	case "day", "days":
		return time.Duration(value) * 24 * time.Hour, nil
	case "week", "weeks":
		return time.Duration(value) * 7 * 24 * time.Hour, nil
	case "month", "months":
		// Approximate: 30 days per month
		return time.Duration(value) * 30 * 24 * time.Hour, nil
	case "year", "years":
		// Approximate: 365 days per year
		return time.Duration(value) * 365 * 24 * time.Hour, nil
	case "hour", "hours":
		return time.Duration(value) * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported time unit: %s (supported: days, weeks, months, years, hours)", unit)
	}
}

// EvaluateExclusion checks if a given time should be excluded based on the exclusion rule
// Supports patterns like "Sunday", "every 7 days", "last day of month"
func EvaluateExclusion(t time.Time, exclude string) bool {
	if exclude == "" {
		return false
	}

	exclude = strings.TrimSpace(strings.ToLower(exclude))

	// Check for day of week (e.g., "sunday", "monday")
	weekdays := map[string]time.Weekday{
		"sunday":    time.Sunday,
		"monday":    time.Monday,
		"tuesday":   time.Tuesday,
		"wednesday": time.Wednesday,
		"thursday":  time.Thursday,
		"friday":    time.Friday,
		"saturday":  time.Saturday,
	}

	if weekday, ok := weekdays[exclude]; ok {
		return t.Weekday() == weekday
	}

	// Check for "every X days" pattern
	if strings.HasPrefix(exclude, "every ") && strings.HasSuffix(exclude, " days") {
		numStr := strings.TrimPrefix(exclude, "every ")
		numStr = strings.TrimSuffix(numStr, " days")
		numStr = strings.TrimSpace(numStr)

		if num, err := strconv.Atoi(numStr); err == nil && num > 0 {
			// Calculate if this is a multiple of num days from epoch
			daysSinceEpoch := int(t.Unix() / (24 * 3600))
			return daysSinceEpoch%num == 0
		}
	}

	// Check for "last day of month" pattern
	if exclude == "last day of month" || exclude == "last day of the month" {
		// Check if tomorrow is a new month
		tomorrow := t.AddDate(0, 0, 1)
		return tomorrow.Month() != t.Month()
	}

	// Check for specific day of month (e.g., "1st", "15th", "day 1", "day 15")
	if strings.HasPrefix(exclude, "day ") {
		dayStr := strings.TrimPrefix(exclude, "day ")
		if day, err := strconv.Atoi(dayStr); err == nil {
			return t.Day() == day
		}
	}

	return false
}
