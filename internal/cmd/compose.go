package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"ciwg-cli/internal/auth"
	"ciwg-cli/internal/compose"
)

var composeCmd = &cobra.Command{
	Use:   "compose",
	Short: "Manage Docker Compose configurations",
	Long: `Manage Docker Compose configurations with full CRUD operations.
Supports interactive and non-interactive modes with backup and rollback capabilities.`,
}

var composeReadCmd = &cobra.Command{
	Use:   "read [hostname]",
	Short: "Read and display docker-compose.yml configuration",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeRead,
}

var composeGetCmd = &cobra.Command{
	Use:   "get [hostname]",
	Short: "Get a specific value from docker-compose.yml",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeGet,
}

var composeSetCmd = &cobra.Command{
	Use:   "set [hostname]",
	Short: "Set a specific value in docker-compose.yml",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeSet,
}

var composeDeleteCmd = &cobra.Command{
	Use:   "delete [hostname]",
	Short: "Delete a key or service from docker-compose.yml",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeDelete,
}

var composeListCmd = &cobra.Command{
	Use:   "list [hostname]",
	Short: "List services in docker-compose.yml",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeList,
}

var composeBackupCmd = &cobra.Command{
	Use:   "backup [hostname]",
	Short: "Create a backup of docker-compose.yml",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeBackup,
}

var composeRestoreCmd = &cobra.Command{
	Use:   "restore [hostname]",
	Short: "Restore docker-compose.yml from backup",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeRestore,
}

var composeEditCmd = &cobra.Command{
	Use:   "edit [hostname]",
	Short: "Edit docker-compose.yml with full YAML manipulation",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeEdit,
}

func init() {
	rootCmd.AddCommand(composeCmd)
	composeCmd.AddCommand(composeReadCmd)
	composeCmd.AddCommand(composeGetCmd)
	composeCmd.AddCommand(composeSetCmd)
	composeCmd.AddCommand(composeDeleteCmd)
	composeCmd.AddCommand(composeListCmd)
	composeCmd.AddCommand(composeBackupCmd)
	composeCmd.AddCommand(composeRestoreCmd)
	composeCmd.AddCommand(composeEditCmd)

	// Add standard SSH flags to all commands
	for _, cmd := range []*cobra.Command{
		composeReadCmd, composeGetCmd, composeSetCmd, composeDeleteCmd,
		composeListCmd, composeBackupCmd, composeRestoreCmd, composeEditCmd,
	} {
		addSSHFlags(cmd)
		cmd.Flags().String("container", "", "Container name (omit if using --all-containers)")
		cmd.Flags().String("server-range", getEnvWithDefault("SERVER_RANGE", ""), "Server range pattern (e.g., wp%d.example.com:0-41:!10,15-17)")
		cmd.Flags().String("working-dir-parent", "", "Override parent directory (e.g., /var/opt) while preserving subdirectory name")
		cmd.Flags().Bool("all-containers", false, "Apply operation to all containers matching prefix")
		cmd.Flags().String("prefix", "wp_", "Container name prefix to match (used with --all-containers)")
	}

	// Command-specific flags
	composeGetCmd.Flags().String("service", "", "Service name (required)")
	composeGetCmd.Flags().String("config-key", "", "Configuration key to retrieve (required)")
	composeGetCmd.MarkFlagRequired("service")
	composeGetCmd.MarkFlagRequired("config-key")

	composeSetCmd.Flags().String("service", "", "Service name (required)")
	composeSetCmd.Flags().String("config-key", "", "Configuration key to set (required)")
	composeSetCmd.Flags().String("value", "", "Value to set (required for non-interactive)")
	composeSetCmd.Flags().String("value-file", "", "File containing YAML value")
	composeSetCmd.Flags().Bool("interactive", false, "Interactive mode for complex values")
	composeSetCmd.Flags().Bool("no-backup", false, "Skip backup creation")
	composeSetCmd.Flags().Bool("no-confirm", false, "Skip confirmation prompt")
	composeSetCmd.Flags().Bool("no-restart", false, "Skip container restart")
	composeSetCmd.Flags().Bool("no-health-check", false, "Skip health check after restart")
	composeSetCmd.Flags().Bool("no-rollback", false, "Disable automatic rollback on failure")
	composeSetCmd.Flags().String("health-url", "", "URL for health check (defaults to container domain)")
	composeSetCmd.Flags().Duration("health-timeout", 30*time.Second, "Health check timeout")
	composeSetCmd.MarkFlagRequired("service")
	composeSetCmd.MarkFlagRequired("key")

	composeDeleteCmd.Flags().String("service", "", "Service name")
	composeDeleteCmd.Flags().String("config-key", "", "Configuration key to delete (omit to delete entire service)")
	composeDeleteCmd.Flags().Bool("no-backup", false, "Skip backup creation")
	composeDeleteCmd.Flags().Bool("no-confirm", false, "Skip confirmation prompt")
	composeDeleteCmd.Flags().Bool("no-restart", false, "Skip container restart")
	composeDeleteCmd.Flags().Bool("no-health-check", false, "Skip health check after restart")
	composeDeleteCmd.Flags().Bool("no-rollback", false, "Disable automatic rollback on failure")
	composeDeleteCmd.Flags().String("health-url", "", "URL for health check")
	composeDeleteCmd.Flags().Duration("health-timeout", 30*time.Second, "Health check timeout")
	composeDeleteCmd.MarkFlagRequired("service")

	composeRestoreCmd.Flags().String("backup-path", "", "Backup file path (required)")
	composeRestoreCmd.Flags().Bool("no-confirm", false, "Skip confirmation prompt")
	composeRestoreCmd.Flags().Bool("no-restart", false, "Skip container restart")
	composeRestoreCmd.MarkFlagRequired("backup-path")

	composeEditCmd.Flags().Bool("no-backup", false, "Skip backup creation")
	composeEditCmd.Flags().Bool("no-confirm", false, "Skip confirmation prompt")
	composeEditCmd.Flags().Bool("no-restart", false, "Skip container restart")
	composeEditCmd.Flags().Bool("no-health-check", false, "Skip health check after restart")
	composeEditCmd.Flags().Bool("no-rollback", false, "Disable automatic rollback on failure")
	composeEditCmd.Flags().String("health-url", "", "URL for health check")
	composeEditCmd.Flags().Duration("health-timeout", 30*time.Second, "Health check timeout")
	composeEditCmd.Flags().String("yaml-file", "", "YAML file to apply (non-interactive)")
	composeEditCmd.Flags().String("yaml", "", "YAML content to apply (non-interactive)")

	// Export command
	composeCmd.AddCommand(composeExportCmd)
	composeExportCmd.Flags().StringP("service", "s", "", "Service name (required)")
	composeExportCmd.Flags().String("keys", "", "Comma-separated service keys to export (e.g., ports,environment) (required)")
	composeExportCmd.Flags().String("placeholder-prefix", "", "Prefix for placeholder names (default: SERVICE_KEY)")
	composeExportCmd.Flags().String("out-file", "", "Write placeholders to local file (optional)")
	composeExportCmd.Flags().Bool("remote-append", false, "Append placeholder block to remote compose file")
	composeExportCmd.Flags().String("remote-file", "", "Remote file path to append placeholders (defaults to compose path)")
	composeExportCmd.Flags().String("callback", "", "Local callback script path to execute with placeholders exported as env vars")
	composeExportCmd.Flags().Bool("yaml-format", true, "Serialize complex values as YAML strings for env and files")
	composeExportCmd.Flags().String("container", "", "Container name (omit if using --all-containers)")
	composeExportCmd.Flags().String("server-range", getEnvWithDefault("SERVER_RANGE", ""), "Server range pattern")
	composeExportCmd.Flags().String("working-dir-parent", "", "Override parent directory (e.g., /var/opt) while preserving subdirectory name")
	composeExportCmd.Flags().Bool("all-containers", false, "Apply operation to all containers matching prefix")
	composeExportCmd.Flags().String("prefix", "wp_", "Container name prefix to match (used with --all-containers)")
	addSSHFlags(composeExportCmd)
	composeExportCmd.MarkFlagRequired("service")
	composeExportCmd.MarkFlagRequired("keys")
}

var composeExportCmd = &cobra.Command{
	Use:   "export [hostname]",
	Short: "Export compose properties as placeholders for callbacks or appended YAML",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runComposeExport,
}

func addSSHFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("user", "u", getEnvWithDefault("SSH_USER", ""), "SSH username")
	cmd.Flags().StringP("port", "p", getEnvWithDefault("SSH_PORT", "22"), "SSH port")
	cmd.Flags().StringP("key", "k", getEnvWithDefault("SSH_KEY", ""), "SSH private key path")
	cmd.Flags().BoolP("agent", "a", getEnvBoolWithDefault("SSH_AGENT", true), "Use SSH agent")
	cmd.Flags().DurationP("timeout", "t", 30*time.Second, "Connection timeout")
}

// createComposeManager creates a compose manager with optional working directory override
func createComposeManager(cmd *cobra.Command, sshClient *auth.SSHClient, container string) (*compose.Manager, error) {
	workDirParent, _ := cmd.Flags().GetString("working-dir-parent")
	if workDirParent != "" {
		return compose.NewManagerWithOverride(sshClient, container, workDirParent)
	}
	return compose.NewManager(sshClient, container)
}

// discoverContainers discovers all containers matching a prefix on the remote host
func discoverContainers(sshClient *auth.SSHClient, prefix string) ([]string, error) {
	cmd := fmt.Sprintf("docker ps --format '{{.Names}}' | grep '^%s'", prefix)
	stdout, stderr, err := sshClient.ExecuteCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to discover containers: %w (stderr: %s)", err, stderr)
	}

	containers := []string{}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			containers = append(containers, line)
		}
	}

	return containers, nil
}

// validateContainerFlags checks that either --container or --all-containers is provided
func validateContainerFlags(cmd *cobra.Command) error {
	allContainers, _ := cmd.Flags().GetBool("all-containers")
	container, _ := cmd.Flags().GetString("container")

	if !allContainers && container == "" {
		return fmt.Errorf("either --container or --all-containers is required")
	}
	if allContainers && container != "" {
		return fmt.Errorf("cannot use both --container and --all-containers")
	}
	return nil
}

// processForAllContainers is a generic handler that processes a command for all discovered containers
func processForAllContainers(cmd *cobra.Command, hostname string, processFn func(*cobra.Command, string) error) error {
	prefix, _ := cmd.Flags().GetString("prefix")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	containers, err := discoverContainers(sshClient, prefix)
	if err != nil {
		return err
	}

	if len(containers) == 0 {
		fmt.Printf("No containers found matching prefix '%s' on %s\n", prefix, hostname)
		return nil
	}

	fmt.Printf("Found %d containers matching '%s' on %s\n\n", len(containers), prefix, hostname)

	var lastErr error
	successCount := 0
	failCount := 0

	for _, container := range containers {
		fmt.Printf("\n=== Container: %s ===\n", container)
		// Temporarily set container flag for processing
		cmd.Flags().Set("container", container)
		if err := processFn(cmd, hostname); err != nil {
			fmt.Printf("Error processing %s: %v\n", container, err)
			lastErr = err
			failCount++
		} else {
			successCount++
		}
	}

	if len(containers) > 1 {
		fmt.Printf("\n=== Summary: %d succeeded, %d failed ===\n", successCount, failCount)
	}

	return lastErr
}

func runComposeRead(cmd *cobra.Command, args []string) error {
	if err := validateContainerFlags(cmd); err != nil {
		return err
	}

	allContainers, _ := cmd.Flags().GetBool("all-containers")
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processComposeReadForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	// Handle all-containers mode
	if allContainers {
		return processComposeReadForAllContainers(cmd, args[0])
	}

	return processComposeRead(cmd, args[0])
}

func processComposeReadForAllContainers(cmd *cobra.Command, hostname string) error {
	return processForAllContainers(cmd, hostname, processComposeRead)
}

func processComposeRead(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	config, err := manager.Read()
	if err != nil {
		return err
	}

	// Display the configuration
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	fmt.Printf("=== Docker Compose Configuration for %s (%s) ===\n", container, hostname)
	fmt.Printf("Path: %s\n\n", manager.GetComposePath())
	fmt.Println(string(data))

	return nil
}

func processComposeReadForServerRange(cmd *cobra.Command, serverRange string) error {
	allContainers, _ := cmd.Flags().GetBool("all-containers")

	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return err
	}

	var lastErr error

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("\n=== Processing %s ===\n", hostname)

		if allContainers {
			if err := processComposeReadForAllContainers(cmd, hostname); err != nil {
				fmt.Fprintf(os.Stderr, "Error on %s: %v\n", hostname, err)
				lastErr = err
				continue
			}
		} else {
			if err := processComposeRead(cmd, hostname); err != nil {
				fmt.Fprintf(os.Stderr, "Error on %s: %v\n", hostname, err)
				lastErr = err
				continue
			}
		}
	}

	return lastErr
}

func runComposeExport(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	if serverRange != "" {
		return processComposeExportForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	return processComposeExport(cmd, args[0])
}

func processComposeExportForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return err
	}

	var lastErr error

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("\n=== Processing %s ===\n", hostname)

		if err := processComposeExport(cmd, hostname); err != nil {
			fmt.Fprintf(os.Stderr, "Error on %s: %v\n", hostname, err)
			lastErr = err
			continue
		}
	}

	return lastErr
}

func processComposeExport(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")
	service, _ := cmd.Flags().GetString("service")
	keysStr, _ := cmd.Flags().GetString("keys")
	prefix, _ := cmd.Flags().GetString("placeholder-prefix")
	outFile, _ := cmd.Flags().GetString("out-file")
	remoteAppend, _ := cmd.Flags().GetBool("remote-append")
	remoteFile, _ := cmd.Flags().GetString("remote-file")
	callback, _ := cmd.Flags().GetString("callback")
	yamlFormat, _ := cmd.Flags().GetBool("yaml-format")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	keys := strings.Split(keysStr, ",")
	placeholders := make(map[string]string)

	for _, k := range keys {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		val, err := manager.GetServiceValue(service, key)
		if err != nil {
			return fmt.Errorf("failed to get %s.%s: %w", service, key, err)
		}

		// Default placeholder name
		placeholderName := strings.ToUpper(fmt.Sprintf("%s_%s", service, key))
		if prefix != "" {
			placeholderName = strings.ToUpper(fmt.Sprintf("%s_%s", prefix, key))
		}
		// Normalize non-alnum to underscore
		placeholderName = sanitizePlaceholder(placeholderName)

		var outVal string
		if yamlFormat {
			b, err := yaml.Marshal(val)
			if err != nil {
				// Fallback to fmt
				outVal = fmt.Sprintf("%v", val)
			} else {
				outVal = strings.TrimSpace(string(b))
			}
		} else {
			outVal = fmt.Sprintf("%v", val)
		}

		placeholders[placeholderName] = outVal
	}

	// Optionally write local out file
	if outFile != "" {
		var builder strings.Builder
		for name, val := range placeholders {
			builder.WriteString(fmt.Sprintf("%s=%s\n", name, escapeNewlines(val)))
		}
		if err := os.WriteFile(outFile, []byte(builder.String()), 0644); err != nil {
			return fmt.Errorf("failed to write out-file: %w", err)
		}
		fmt.Printf("✓ Wrote placeholders to %s\n", outFile)
	}

	// Optionally append placeholder block to remote compose file
	if remoteAppend {
		// Build YAML block with placeholders tokens like %%NAME%%
		var blockBuilder strings.Builder
		blockBuilder.WriteString("\n# %%PLACEHOLDERS%% - exported placeholders\n")
		blockBuilder.WriteString("placeholders:\n")
		for name := range placeholders {
			blockBuilder.WriteString(fmt.Sprintf("  %s: '%%%%%s%%%%'\n", name, name))
		}

		block := blockBuilder.String()
		// If remoteFile provided, temporarily override compose path by writing to it
		if remoteFile != "" {
			// Append to remoteFile
			cmdStr := fmt.Sprintf("cat >> %s << 'PLACEHOLDER_EOF'\n%s\nPLACEHOLDER_EOF", remoteFile, block)
			if _, stderr, err := sshClient.ExecuteCommand(cmdStr); err != nil {
				return fmt.Errorf("failed to append placeholders to remote file %s: %w (stderr: %s)", remoteFile, err, stderr)
			}
			fmt.Printf("✓ Appended placeholders to remote file: %s\n", remoteFile)
		} else {
			if err := manager.AppendPlaceholders(block); err != nil {
				return fmt.Errorf("failed to append placeholders: %w", err)
			}
			fmt.Printf("✓ Appended placeholders to compose file: %s\n", manager.GetComposePath())
		}
	}

	// Optionally execute callback script with placeholders as env vars
	if callback != "" {
		// Build environment variables
		env := os.Environ()
		for name, val := range placeholders {
			env = append(env, fmt.Sprintf("%s=%s", name, val))
		}
		if err := executeLocalCallback(callback, env); err != nil {
			return fmt.Errorf("callback failed: %w", err)
		}
		fmt.Printf("✓ Callback executed: %s\n", callback)
	}

	// Print placeholders to stdout for convenience
	fmt.Println("=== Placeholders ===")
	for name, val := range placeholders {
		fmt.Printf("%s=\n%s\n---\n", name, val)
	}

	return nil
}

func sanitizePlaceholder(name string) string {
	// Replace any non-alphanumeric with underscore
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func escapeNewlines(s string) string {
	return strings.ReplaceAll(s, "\n", "\\n")
}

func executeLocalCallback(path string, env []string) error {
	// Use os/exec to run script
	cmdExec := exec.Command(path)
	cmdExec.Env = env
	cmdExec.Stdout = os.Stdout
	cmdExec.Stderr = os.Stderr
	return cmdExec.Run()
}

func runComposeGet(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	if serverRange != "" {
		return processComposeGetForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	return processComposeGet(cmd, args[0])
}

func processComposeGet(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")
	service, _ := cmd.Flags().GetString("service")
	key, _ := cmd.Flags().GetString("config-key")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	value, err := manager.GetServiceValue(service, key)
	if err != nil {
		return err
	}

	fmt.Printf("=== %s > %s.%s ===\n", hostname, service, key)
	data, _ := yaml.Marshal(value)
	fmt.Println(string(data))

	return nil
}

func processComposeGetForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return err
	}

	var lastErr error

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)

		if err := processComposeGet(cmd, hostname); err != nil {
			fmt.Fprintf(os.Stderr, "Error on %s: %v\n", hostname, err)
			lastErr = err
			continue
		}
	}

	return lastErr
}

func runComposeSet(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	if serverRange != "" {
		return processComposeSetForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	return processComposeSet(cmd, args[0])
}

func processComposeSet(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")
	service, _ := cmd.Flags().GetString("service")
	key, _ := cmd.Flags().GetString("config-key")
	valueStr, _ := cmd.Flags().GetString("value")
	valueFile, _ := cmd.Flags().GetString("value-file")
	interactive, _ := cmd.Flags().GetBool("interactive")
	noBackup, _ := cmd.Flags().GetBool("no-backup")
	noConfirm, _ := cmd.Flags().GetBool("no-confirm")
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	noHealthCheck, _ := cmd.Flags().GetBool("no-health-check")
	noRollback, _ := cmd.Flags().GetBool("no-rollback")
	healthURL, _ := cmd.Flags().GetString("health-url")
	healthTimeout, _ := cmd.Flags().GetDuration("health-timeout")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	// Get the value to set
	var value interface{}
	if interactive {
		value, err = promptForValue()
		if err != nil {
			return err
		}
	} else if valueFile != "" {
		data, err := os.ReadFile(valueFile)
		if err != nil {
			return fmt.Errorf("failed to read value file: %w", err)
		}
		if err := yaml.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("failed to parse value file: %w", err)
		}
	} else if valueStr != "" {
		// Try to parse as YAML first
		if err := yaml.Unmarshal([]byte(valueStr), &value); err != nil {
			// If parsing fails, treat as string
			value = valueStr
		}
	} else {
		return fmt.Errorf("value, value-file, or interactive mode required")
	}

	// Show what will be changed
	fmt.Printf("=== Setting %s.%s on %s (%s) ===\n", service, key, container, hostname)
	valueData, _ := yaml.Marshal(value)
	fmt.Printf("New value:\n%s\n", string(valueData))

	// Confirmation
	if !noConfirm {
		if !confirmAction("Apply this change?") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Create backup
	var backup *compose.BackupInfo
	if !noBackup {
		backup, err = manager.Backup()
		if err != nil {
			return fmt.Errorf("failed to create backup: %w", err)
		}
		fmt.Printf("✓ Backup created: %s\n", backup.Path)
	}

	// Apply the change
	if err := manager.SetServiceValue(service, key, value); err != nil {
		return fmt.Errorf("failed to set value: %w", err)
	}
	fmt.Println("✓ Configuration updated")

	// Restart container
	if !noRestart {
		fmt.Println("Restarting container...")
		if err := manager.RestartContainer(); err != nil {
			if !noRollback && backup != nil {
				fmt.Println("⚠ Restart failed, rolling back...")
				if err := rollbackAndRestart(manager, backup.Path); err != nil {
					return fmt.Errorf("rollback failed: %w", err)
				}
				return fmt.Errorf("restart failed, changes rolled back: %w", err)
			}
			return fmt.Errorf("failed to restart container: %w", err)
		}
		fmt.Println("✓ Container restarted")

		// Health check
		if !noHealthCheck {
			if healthURL == "" {
				healthURL = inferHealthURL(hostname)
			}
			fmt.Printf("Checking site health: %s\n", healthURL)
			time.Sleep(5 * time.Second) // Give the container time to start

			if err := manager.HealthCheck(healthURL, healthTimeout); err != nil {
				if !noRollback && backup != nil {
					fmt.Println("⚠ Health check failed, rolling back...")
					if err := rollbackAndRestart(manager, backup.Path); err != nil {
						return fmt.Errorf("rollback failed: %w", err)
					}
					return fmt.Errorf("health check failed, changes rolled back: %w", err)
				}
				return fmt.Errorf("health check failed: %w", err)
			}
			fmt.Println("✓ Health check passed")
		}
	}

	// Clean up backup on success
	if backup != nil && !noRestart && !noHealthCheck {
		fmt.Printf("Keeping backup at: %s\n", backup.Path)
	}

	fmt.Println("✓ Operation completed successfully")
	return nil
}

func processComposeSetForServerRange(cmd *cobra.Command, serverRange string) error {
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

		if err := processComposeSet(cmd, hostname); err != nil {
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

func runComposeDelete(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	if serverRange != "" {
		return processComposeDeleteForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	return processComposeDelete(cmd, args[0])
}

func processComposeDelete(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")
	service, _ := cmd.Flags().GetString("service")
	key, _ := cmd.Flags().GetString("config-key")
	noBackup, _ := cmd.Flags().GetBool("no-backup")
	noConfirm, _ := cmd.Flags().GetBool("no-confirm")
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	noHealthCheck, _ := cmd.Flags().GetBool("no-health-check")
	noRollback, _ := cmd.Flags().GetBool("no-rollback")
	healthURL, _ := cmd.Flags().GetString("health-url")
	healthTimeout, _ := cmd.Flags().GetDuration("health-timeout")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	// Determine what to delete
	var operation string
	if key == "" {
		operation = fmt.Sprintf("Delete entire service '%s'", service)
	} else {
		operation = fmt.Sprintf("Delete key '%s' from service '%s'", key, service)
	}

	fmt.Printf("=== %s on %s (%s) ===\n", operation, container, hostname)

	// Confirmation
	if !noConfirm {
		if !confirmAction(fmt.Sprintf("%s?", operation)) {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Create backup
	var backup *compose.BackupInfo
	if !noBackup {
		backup, err = manager.Backup()
		if err != nil {
			return fmt.Errorf("failed to create backup: %w", err)
		}
		fmt.Printf("✓ Backup created: %s\n", backup.Path)
	}

	// Perform deletion
	if key == "" {
		if err := manager.DeleteService(service); err != nil {
			return fmt.Errorf("failed to delete service: %w", err)
		}
	} else {
		if err := manager.DeleteServiceKey(service, key); err != nil {
			return fmt.Errorf("failed to delete key: %w", err)
		}
	}
	fmt.Println("✓ Configuration updated")

	// Restart and health check (same logic as set)
	if !noRestart {
		fmt.Println("Restarting container...")
		if err := manager.RestartContainer(); err != nil {
			if !noRollback && backup != nil {
				fmt.Println("⚠ Restart failed, rolling back...")
				if err := rollbackAndRestart(manager, backup.Path); err != nil {
					return fmt.Errorf("rollback failed: %w", err)
				}
				return fmt.Errorf("restart failed, changes rolled back: %w", err)
			}
			return fmt.Errorf("failed to restart container: %w", err)
		}
		fmt.Println("✓ Container restarted")

		if !noHealthCheck {
			if healthURL == "" {
				healthURL = inferHealthURL(hostname)
			}
			fmt.Printf("Checking site health: %s\n", healthURL)
			time.Sleep(5 * time.Second)

			if err := manager.HealthCheck(healthURL, healthTimeout); err != nil {
				if !noRollback && backup != nil {
					fmt.Println("⚠ Health check failed, rolling back...")
					if err := rollbackAndRestart(manager, backup.Path); err != nil {
						return fmt.Errorf("rollback failed: %w", err)
					}
					return fmt.Errorf("health check failed, changes rolled back: %w", err)
				}
				return fmt.Errorf("health check failed: %w", err)
			}
			fmt.Println("✓ Health check passed")
		}
	}

	fmt.Println("✓ Operation completed successfully")
	return nil
}

func processComposeDeleteForServerRange(cmd *cobra.Command, serverRange string) error {
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

		if err := processComposeDelete(cmd, hostname); err != nil {
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

func runComposeList(cmd *cobra.Command, args []string) error {
	if err := validateContainerFlags(cmd); err != nil {
		return err
	}

	allContainers, _ := cmd.Flags().GetBool("all-containers")
	serverRange, _ := cmd.Flags().GetString("server-range")

	if serverRange != "" {
		return processComposeListForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	if allContainers {
		return processForAllContainers(cmd, args[0], processComposeList)
	}

	return processComposeList(cmd, args[0])
}

func processComposeList(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	config, err := manager.Read()
	if err != nil {
		return err
	}

	fmt.Printf("=== Services in %s (%s) ===\n", container, hostname)
	for name, service := range config.Services {
		fmt.Printf("\n%s:\n", name)
		fmt.Printf("  Image: %s\n", service.Image)
		if service.ContainerName != "" {
			fmt.Printf("  Container Name: %s\n", service.ContainerName)
		}
		if len(service.Ports) > 0 {
			fmt.Printf("  Ports: %v\n", service.Ports)
		}
		if service.Restart != "" {
			fmt.Printf("  Restart: %s\n", service.Restart)
		}
	}

	return nil
}

func processComposeListForServerRange(cmd *cobra.Command, serverRange string) error {
	allContainers, _ := cmd.Flags().GetBool("all-containers")

	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return err
	}

	var lastErr error

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("\n=== Processing %s ===\n", hostname)

		if allContainers {
			if err := processForAllContainers(cmd, hostname, processComposeList); err != nil {
				fmt.Fprintf(os.Stderr, "Error on %s: %v\n", hostname, err)
				lastErr = err
				continue
			}
		} else {
			if err := processComposeList(cmd, hostname); err != nil {
				fmt.Fprintf(os.Stderr, "Error on %s: %v\n", hostname, err)
				lastErr = err
				continue
			}
		}
	}

	return lastErr
}

func runComposeBackup(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	if serverRange != "" {
		return processComposeBackupForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	return processComposeBackup(cmd, args[0])
}

func processComposeBackup(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	backup, err := manager.Backup()
	if err != nil {
		return err
	}

	fmt.Printf("✓ Backup created on %s\n", hostname)
	fmt.Printf("  Path: %s\n", backup.Path)
	fmt.Printf("  Timestamp: %s\n", backup.Timestamp.Format(time.RFC3339))

	// List all backups
	backups, err := manager.ListBackups()
	if err == nil && len(backups) > 1 {
		fmt.Printf("\nAll backups (%d):\n", len(backups))
		for _, b := range backups {
			fmt.Printf("  - %s (%s)\n", b.Path, b.Timestamp.Format(time.RFC3339))
		}
	}

	return nil
}

func processComposeBackupForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return err
	}

	var lastErr error

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("\n=== Processing %s ===\n", hostname)

		if err := processComposeBackup(cmd, hostname); err != nil {
			fmt.Fprintf(os.Stderr, "Error on %s: %v\n", hostname, err)
			lastErr = err
			continue
		}
	}

	return lastErr
}

func runComposeRestore(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	if serverRange != "" {
		return processComposeRestoreForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	return processComposeRestore(cmd, args[0])
}

func processComposeRestore(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")
	backupPath, _ := cmd.Flags().GetString("backup-path")
	noConfirm, _ := cmd.Flags().GetBool("no-confirm")
	noRestart, _ := cmd.Flags().GetBool("no-restart")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	fmt.Printf("=== Restoring %s on %s from backup ===\n", container, hostname)
	fmt.Printf("Backup: %s\n", backupPath)

	if !noConfirm {
		if !confirmAction("Restore from this backup?") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if err := manager.Restore(backupPath); err != nil {
		return err
	}
	fmt.Println("✓ Configuration restored")

	if !noRestart {
		fmt.Println("Restarting container...")
		if err := manager.RestartContainer(); err != nil {
			return fmt.Errorf("failed to restart container: %w", err)
		}
		fmt.Println("✓ Container restarted")
	}

	fmt.Println("✓ Restore completed successfully")
	return nil
}

func processComposeRestoreForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return err
	}

	var lastErr error

	for i := start; i <= end; i++ {
		if exclusions[i] {
			continue
		}

		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("\n=== Processing %s ===\n", hostname)

		if err := processComposeRestore(cmd, hostname); err != nil {
			fmt.Fprintf(os.Stderr, "Error on %s: %v\n", hostname, err)
			lastErr = err
			continue
		}
	}

	return lastErr
}

func runComposeEdit(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	if serverRange != "" {
		return processComposeEditForServerRange(cmd, serverRange)
	}

	if len(args) == 0 {
		return fmt.Errorf("hostname is required")
	}

	return processComposeEdit(cmd, args[0])
}

func processComposeEdit(cmd *cobra.Command, hostname string) error {
	container, _ := cmd.Flags().GetString("container")
	yamlFile, _ := cmd.Flags().GetString("yaml-file")
	yamlContent, _ := cmd.Flags().GetString("yaml")
	noBackup, _ := cmd.Flags().GetBool("no-backup")
	noConfirm, _ := cmd.Flags().GetBool("no-confirm")
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	noHealthCheck, _ := cmd.Flags().GetBool("no-health-check")
	noRollback, _ := cmd.Flags().GetBool("no-rollback")
	healthURL, _ := cmd.Flags().GetString("health-url")
	healthTimeout, _ := cmd.Flags().GetDuration("health-timeout")

	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return err
	}
	defer sshClient.Close()

	manager, err := createComposeManager(cmd, sshClient, container)
	if err != nil {
		return err
	}

	// Get new configuration
	var newConfig compose.ComposeConfig
	if yamlFile != "" {
		data, err := os.ReadFile(yamlFile)
		if err != nil {
			return fmt.Errorf("failed to read YAML file: %w", err)
		}
		if err := yaml.Unmarshal(data, &newConfig); err != nil {
			return fmt.Errorf("failed to parse YAML file: %w", err)
		}
	} else if yamlContent != "" {
		if err := yaml.Unmarshal([]byte(yamlContent), &newConfig); err != nil {
			return fmt.Errorf("failed to parse YAML content: %w", err)
		}
	} else {
		return fmt.Errorf("yaml-file or yaml content required")
	}

	fmt.Printf("=== Editing %s on %s (%s) ===\n", container, hostname, manager.GetComposePath())

	// Show diff (simplified)
	currentConfig, _ := manager.Read()
	currentData, _ := yaml.Marshal(currentConfig)
	newData, _ := yaml.Marshal(&newConfig)
	fmt.Println("\nCurrent configuration length:", len(currentData), "bytes")
	fmt.Println("New configuration length:", len(newData), "bytes")

	if !noConfirm {
		if !confirmAction("Apply this configuration?") {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Create backup
	var backup *compose.BackupInfo
	if !noBackup {
		backup, err = manager.Backup()
		if err != nil {
			return fmt.Errorf("failed to create backup: %w", err)
		}
		fmt.Printf("✓ Backup created: %s\n", backup.Path)
	}

	// Write new configuration
	if err := manager.Write(&newConfig); err != nil {
		return fmt.Errorf("failed to write configuration: %w", err)
	}
	fmt.Println("✓ Configuration updated")

	// Restart and health check
	if !noRestart {
		fmt.Println("Restarting container...")
		if err := manager.RestartContainer(); err != nil {
			if !noRollback && backup != nil {
				fmt.Println("⚠ Restart failed, rolling back...")
				if err := rollbackAndRestart(manager, backup.Path); err != nil {
					return fmt.Errorf("rollback failed: %w", err)
				}
				return fmt.Errorf("restart failed, changes rolled back: %w", err)
			}
			return fmt.Errorf("failed to restart container: %w", err)
		}
		fmt.Println("✓ Container restarted")

		if !noHealthCheck {
			if healthURL == "" {
				healthURL = inferHealthURL(hostname)
			}
			fmt.Printf("Checking site health: %s\n", healthURL)
			time.Sleep(5 * time.Second)

			if err := manager.HealthCheck(healthURL, healthTimeout); err != nil {
				if !noRollback && backup != nil {
					fmt.Println("⚠ Health check failed, rolling back...")
					if err := rollbackAndRestart(manager, backup.Path); err != nil {
						return fmt.Errorf("rollback failed: %w", err)
					}
					return fmt.Errorf("health check failed, changes rolled back: %w", err)
				}
				return fmt.Errorf("health check failed: %w", err)
			}
			fmt.Println("✓ Health check passed")
		}
	}

	fmt.Println("✓ Operation completed successfully")
	return nil
}

func processComposeEditForServerRange(cmd *cobra.Command, serverRange string) error {
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

		if err := processComposeEdit(cmd, hostname); err != nil {
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

// Helper functions

func confirmAction(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", prompt)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}

func rollbackAndRestart(manager *compose.Manager, backupPath string) error {
	if err := manager.Restore(backupPath); err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}
	fmt.Println("✓ Configuration restored")

	if err := manager.RestartContainer(); err != nil {
		return fmt.Errorf("restart after rollback failed: %w", err)
	}
	fmt.Println("✓ Container restarted")

	return nil
}

func inferHealthURL(hostname string) string {
	// Try to infer site URL from hostname
	// This is a simple heuristic - customize based on your naming conventions
	if strings.Contains(hostname, "wp") {
		// Extract site name from hostname like wp12.example.com
		parts := strings.Split(hostname, ".")
		if len(parts) > 0 {
			return "https://" + hostname
		}
	}
	return "https://" + hostname
}

func promptForValue() (interface{}, error) {
	fmt.Println("\nEnter the YAML value (end with Ctrl+D on a new line):")
	reader := bufio.NewReader(os.Stdin)
	var lines []string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		lines = append(lines, line)
	}

	yamlStr := strings.Join(lines, "")
	var value interface{}
	if err := yaml.Unmarshal([]byte(yamlStr), &value); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return value, nil
}
