package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var envTestCmd = &cobra.Command{
	Use:   "envtest [hostname]",
	Short: "Test environment variable configuration",
	Long:  `Test and validate environment variable configuration on local or remote servers.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runEnvTest,
}

func init() {
	rootCmd.AddCommand(envTestCmd)

	// Add server-range support
	envTestCmd.Flags().String("server-range", getEnvWithDefault("SERVER_RANGE", ""), "Server range pattern (env: SERVER_RANGE)")

	// SSH connection flags with environment variable support
	envTestCmd.Flags().StringP("user", "u", getEnvWithDefault("SSH_USER", ""), "SSH username (env: SSH_USER, default: current user)")
	envTestCmd.Flags().StringP("port", "p", getEnvWithDefault("SSH_PORT", "22"), "SSH port (env: SSH_PORT)")
	envTestCmd.Flags().StringP("key", "k", getEnvWithDefault("SSH_KEY", ""), "Path to SSH private key (env: SSH_KEY)")
	envTestCmd.Flags().BoolP("agent", "a", getEnvBoolWithDefault("SSH_AGENT", true), "Use SSH agent (env: SSH_AGENT)")
	envTestCmd.Flags().DurationP("timeout", "t", getEnvDurationWithDefault("SSH_TIMEOUT", 30*time.Second), "Connection timeout (env: SSH_TIMEOUT)")

	// Test options
	envTestCmd.Flags().Bool("local", false, "Test environment variables locally instead of via SSH")
	envTestCmd.Flags().Bool("verbose", getEnvBoolWithDefault("ENVTEST_VERBOSE", false), "Show verbose output including values (env: ENVTEST_VERBOSE)")
}

func runEnvTest(cmd *cobra.Command, args []string) error {
	serverRange, _ := cmd.Flags().GetString("server-range")
	isLocal, _ := cmd.Flags().GetBool("local")

	if serverRange != "" {
		return processEnvTestForServerRange(cmd, serverRange)
	}

	if isLocal || len(args) == 0 {
		fmt.Println("Testing environment variables locally...")
		return testEnvironmentVariablesLocal(cmd)
	}

	hostname := args[0]
	fmt.Printf("Testing environment variables on %s...\n", hostname)
	return testEnvironmentVariablesRemote(cmd, hostname)
}

func processEnvTestForServerRange(cmd *cobra.Command, serverRange string) error {
	pattern, start, end, exclusions, err := parseServerRange(serverRange)
	if err != nil {
		return fmt.Errorf("error parsing server range: %w", err)
	}

	for i := start; i <= end; i++ {
		if exclusions[i] {
			fmt.Printf("Skipping excluded server: %s\n", fmt.Sprintf(pattern, i))
			continue
		}
		hostname := fmt.Sprintf(pattern, i)
		fmt.Printf("--- Processing server: %s ---\n", hostname)
		err := testEnvironmentVariablesRemote(cmd, hostname)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing %s: %v\n", hostname, err)
		}
		fmt.Println()
	}

	return nil
}

func testEnvironmentVariablesLocal(cmd *cobra.Command) error {
	verbose, _ := cmd.Flags().GetBool("verbose")

	envVars := []string{
		"CIWG_DOMAIN_TOKEN",
		"CLOUDFLARE_EMAIL",
		"CLOUDFLARE_API_KEY",
		"NS1",
		"NS2",
		"MYSQL_ROOT_USER",
		"MYSQL_ROOT_PASSWD",
		"BLUEPRINT_DB_DATABASE",
		"BLUEPRINT_DB_USERNAME",
		"BLUEPRINT_DB_PASSWORD",
		"BLUEPRINT_DB_ROOT_PASSWORD",
	}

	fmt.Println("Environment Variable Test Results:")
	fmt.Println("==================================")

	var foundCount, missingCount int
	var errors []string

	for _, envVar := range envVars {
		value := os.Getenv(envVar)
		if value == "" {
			fmt.Printf("✗ %s: NOT SET\n", envVar)
			errors = append(errors, fmt.Sprintf("%s is not set", envVar))
			missingCount++
		} else {
			if verbose {
				// Show first few characters for security
				displayValue := value
				if len(value) > 10 {
					displayValue = value[:10] + "..."
				}
				fmt.Printf("✓ %s: %s\n", envVar, displayValue)
			} else {
				fmt.Printf("✓ %s: SET\n", envVar)
			}
			foundCount++
		}
	}

	fmt.Println()
	fmt.Printf("Summary: %d found, %d missing\n", foundCount, missingCount)

	if len(errors) > 0 {
		fmt.Println("\nErrors found:")
		for _, err := range errors {
			fmt.Printf("  - %s\n", err)
		}
	}

	return nil
}

func testEnvironmentVariablesRemote(cmd *cobra.Command, hostname string) error {
	sshClient, err := createSSHClient(cmd, hostname)
	if err != nil {
		return fmt.Errorf("failed to create SSH client: %w", err)
	}
	defer sshClient.Close()

	verbose, _ := cmd.Flags().GetBool("verbose")

	envVars := []string{
		"CIWG_DOMAIN_TOKEN",
		"CLOUDFLARE_EMAIL",
		"CLOUDFLARE_API_KEY",
		"NS1",
		"NS2",
		"MYSQL_ROOT_USER",
		"MYSQL_ROOT_PASSWD",
		"BLUEPRINT_DB_DATABASE",
		"BLUEPRINT_DB_USERNAME",
		"BLUEPRINT_DB_PASSWORD",
		"BLUEPRINT_DB_ROOT_PASSWORD",
	}

	fmt.Printf("Environment Variable Test Results for %s:\n", hostname)
	fmt.Println("================================================")

	var foundCount, missingCount int
	var errors []string

	for _, envVar := range envVars {
		// Use printenv to check if variable is set and get its value
		cmd := fmt.Sprintf("printenv %s", envVar)
		stdout, stderr, err := sshClient.ExecuteCommand(cmd)

		if err != nil || strings.TrimSpace(stdout) == "" {
			fmt.Printf("✗ %s: NOT SET\n", envVar)
			if stderr != "" {
				errors = append(errors, fmt.Sprintf("%s error: %s", envVar, strings.TrimSpace(stderr)))
			} else {
				errors = append(errors, fmt.Sprintf("%s is not set", envVar))
			}
			missingCount++
		} else {
			value := strings.TrimSpace(stdout)
			if verbose {
				// Show first few characters for security
				displayValue := value
				if len(value) > 10 {
					displayValue = value[:10] + "..."
				}
				fmt.Printf("✓ %s: %s\n", envVar, displayValue)
			} else {
				fmt.Printf("✓ %s: SET\n", envVar)
			}
			foundCount++
		}
	}

	fmt.Println()
	fmt.Printf("Summary: %d found, %d missing\n", foundCount, missingCount)

	if len(errors) > 0 {
		fmt.Println("\nErrors found:")
		for _, err := range errors {
			fmt.Printf("  - %s\n", err)
		}
	}

	return nil
}
