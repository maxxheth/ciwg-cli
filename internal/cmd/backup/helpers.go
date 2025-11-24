package backup

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ciwg-cli/internal/auth"
)

// findEnvArg inspects argv for an explicit --env argument and returns
// the value if present. Supports `--env=path` and `--env path` forms.
func findEnvArg(argv []string) string {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "--env=") {
			return strings.TrimPrefix(a, "--env=")
		}
		if a == "--env" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// mustGetStringFlag gets a string flag value from a cobra command
func mustGetStringFlag(cmd *cobra.Command, name string) string {
	val, _ := cmd.Flags().GetString(name)
	return val
}

// mustGetBoolFlag gets a boolean flag value from a cobra command
func mustGetBoolFlag(cmd *cobra.Command, name string) bool {
	val, _ := cmd.Flags().GetBool(name)
	return val
}

// mustGetFloat64Flag gets a float64 flag value from a cobra command
func mustGetFloat64Flag(cmd *cobra.Command, name string) float64 {
	val, _ := cmd.Flags().GetFloat64(name)
	return val
}

// mustGetDurationFlag gets a duration flag value from a cobra command
func mustGetDurationFlag(cmd *cobra.Command, name string) time.Duration {
	val, _ := cmd.Flags().GetDuration(name)
	return val
}

// mustGetIntFlag gets an int flag value from a cobra command
func mustGetIntFlag(cmd *cobra.Command, name string) int {
	val, _ := cmd.Flags().GetInt(name)
	return val
}

// mustGetInt64Flag gets an int64 flag value from a cobra command
func mustGetInt64Flag(cmd *cobra.Command, name string) int64 {
	val, _ := cmd.Flags().GetInt64(name)
	return val
}

// mustGetCountFlag gets a count flag value from a cobra command
func mustGetCountFlag(cmd *cobra.Command, name string) int {
	val, _ := cmd.Flags().GetCount(name)
	return val
}

// getEnvWithDefault returns the environment variable value or a default
func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvBoolWithDefault returns the environment variable as a bool or a default
func getEnvBoolWithDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		return strings.ToLower(value) == "true" || value == "1"
	}
	return defaultValue
}

// getEnvDurationWithDefault returns the environment variable as a duration or a default
func getEnvDurationWithDefault(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

// getEnvFloat64WithDefault returns the environment variable as a float64 or a default
func getEnvFloat64WithDefault(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatVal, err := strconv.ParseFloat(value, 64); err == nil {
			return floatVal
		}
	}
	return defaultValue
}

// getEnvIntWithDefault returns the environment variable as an int or a default
func getEnvIntWithDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

// parseServerRange parses a server range pattern like "wp%d.ciwgserver.com:0-41"
// or with exclusions: "wp%d.ciwgserver.com:0-41:!10,15-17,22"
func parseServerRange(pattern string) (string, int, int, map[int]bool, error) {
	parts := strings.Split(pattern, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return "", 0, 0, nil, fmt.Errorf("invalid server range format, expected 'pattern:start-end' or 'pattern:start-end:!exclusions'")
	}

	// Parse main range
	rangeParts := strings.Split(parts[1], "-")
	if len(rangeParts) != 2 {
		return "", 0, 0, nil, fmt.Errorf("invalid range format, expected 'start-end'")
	}

	start, err := strconv.Atoi(rangeParts[0])
	if err != nil {
		return "", 0, 0, nil, fmt.Errorf("invalid start number: %w", err)
	}

	end, err := strconv.Atoi(rangeParts[1])
	if err != nil {
		return "", 0, 0, nil, fmt.Errorf("invalid end number: %w", err)
	}

	if start > end {
		return "", 0, 0, nil, fmt.Errorf("start of range cannot be greater than end")
	}

	// Parse exclusions
	exclusions := make(map[int]bool)
	if len(parts) == 3 {
		exclusionStr := parts[2]
		if !strings.HasPrefix(exclusionStr, "!") {
			return "", 0, 0, nil, fmt.Errorf("exclusions part must start with '!'")
		}
		exclusionStr = strings.TrimPrefix(exclusionStr, "!")

		for _, exPart := range strings.Split(exclusionStr, ",") {
			exPart = strings.TrimSpace(exPart)
			if exPart == "" {
				continue
			}

			if strings.Contains(exPart, "-") { // It's a range
				subRangeParts := strings.Split(exPart, "-")
				if len(subRangeParts) != 2 {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion range format: %s", exPart)
				}
				exStart, err := strconv.Atoi(subRangeParts[0])
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion start number in '%s': %w", exPart, err)
				}
				exEnd, err := strconv.Atoi(subRangeParts[1])
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion end number in '%s': %w", exPart, err)
				}
				if exStart > exEnd {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion range: start > end in %s", exPart)
				}
				for i := exStart; i <= exEnd; i++ {
					exclusions[i] = true
				}
			} else { // It's a single number
				exNum, err := strconv.Atoi(exPart)
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion number '%s': %w", exPart, err)
				}
				exclusions[exNum] = true
			}
		}
	}

	return parts[0], start, end, exclusions, nil
}

// createSSHClient creates an SSH client from command flags and target hostname
func createSSHClient(cmd *cobra.Command, target string) (*auth.SSHClient, error) {
	// Parse target into user@host format
	parts := strings.Split(target, "@")
	var username, hostname string

	if len(parts) == 2 {
		username = parts[0]
		hostname = parts[1]
	} else {
		username, _ = cmd.Flags().GetString("user")
		if username == "" {
			username = getCurrentUser()
		}
		hostname = target
	}

	port, _ := cmd.Flags().GetString("port")
	keyPath, _ := cmd.Flags().GetString("key")
	useAgent, _ := cmd.Flags().GetBool("agent")
	timeout, _ := cmd.Flags().GetDuration("timeout")

	config := auth.SSHConfig{
		Hostname:  hostname,
		Username:  username,
		Port:      port,
		KeyPath:   keyPath,
		UseAgent:  useAgent,
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}

	return auth.NewSSHClient(config)
}

// getCurrentUser returns the current user (defaults to "root")
func getCurrentUser() string {
	// In a real implementation, you'd get the current user
	// For now, return a default
	return "root"
}

// parseSize parses size strings like "125MB", "1.5GB" into bytes
func parseSize(sizeStr string) (int64, error) {
	sizeStr = strings.TrimSpace(strings.ToUpper(sizeStr))

	multiplier := int64(1)
	if strings.HasSuffix(sizeStr, "GB") {
		multiplier = 1024 * 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "GB")
	} else if strings.HasSuffix(sizeStr, "MB") {
		multiplier = 1024 * 1024
		sizeStr = strings.TrimSuffix(sizeStr, "MB")
	} else if strings.HasSuffix(sizeStr, "KB") {
		multiplier = 1024
		sizeStr = strings.TrimSuffix(sizeStr, "KB")
	} else if strings.HasSuffix(sizeStr, "B") {
		sizeStr = strings.TrimSuffix(sizeStr, "B")
	}

	value, err := strconv.ParseFloat(sizeStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size value: %s", sizeStr)
	}

	return int64(value * float64(multiplier)), nil
}
