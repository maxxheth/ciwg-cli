package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	outputFile  string
	serverRange string
)

func parseServerRange(pattern string) (string, int, int, map[int]bool, error) {
	// Expected format: "wp%d.ciwgserver.com:0-41"
	// Or with exclusions: "wp%d.ciwgserver.com:0-41:!10,15-17,22"
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

// Helper functions for environment variable support
func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvBoolWithDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		return strings.ToLower(value) == "true" || value == "1"
	}
	return defaultValue
}

func getEnvDurationWithDefault(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

func getEnvFloat64WithDefault(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatVal, err := strconv.ParseFloat(value, 64); err == nil {
			return floatVal
		}
	}
	return defaultValue
}

func getEnvIntWithDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func runLocalCommand(cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput()
	return string(out), err
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
