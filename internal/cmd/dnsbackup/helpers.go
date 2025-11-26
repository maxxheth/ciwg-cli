package dnsbackup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	dnsbackup "ciwg-cli/internal/dnsbackup"
)

func findEnvArg(argv []string) string {
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if strings.HasPrefix(arg, "--env=") {
			return strings.TrimPrefix(arg, "--env=")
		}
		if arg == "--env" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

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
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func getEnvFloat64WithDefault(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return defaultValue
}

// mustGetStringFlag retrieves a string flag value.
// Errors are ignored because cobra guarantees flags exist if they're defined.
func mustGetStringFlag(cmd *cobra.Command, name string) string {
	val, _ := cmd.Flags().GetString(name)
	return val
}

// mustGetBoolFlag retrieves a bool flag value.
// Errors are ignored because cobra guarantees flags exist if they're defined.
func mustGetBoolFlag(cmd *cobra.Command, name string) bool {
	val, _ := cmd.Flags().GetBool(name)
	return val
}

// mustGetStringSliceFlag retrieves a string slice flag value.
// Errors are ignored because cobra guarantees flags exist if they're defined.
func mustGetStringSliceFlag(cmd *cobra.Command, name string) []string {
	val, _ := cmd.Flags().GetStringSlice(name)
	return val
}

// mustGetDurationFlag retrieves a duration flag value.
// Errors are ignored because cobra guarantees flags exist if they're defined.
func mustGetDurationFlag(cmd *cobra.Command, name string) time.Duration {
	val, _ := cmd.Flags().GetDuration(name)
	return val
}

// mustGetIntFlag retrieves an int flag value.
// Errors are ignored because cobra guarantees flags exist if they're defined.
func mustGetIntFlag(cmd *cobra.Command, name string) int {
	val, _ := cmd.Flags().GetInt(name)
	return val
}

// mustGetFloat64Flag retrieves a float64 flag value.
// Errors are ignored because cobra guarantees flags exist if they're defined.
func mustGetFloat64Flag(cmd *cobra.Command, name string) float64 {
	val, _ := cmd.Flags().GetFloat64(name)
	return val
}

func parseMetadata(values []string) (map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	meta := make(map[string]any)
	for _, entry := range values {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid metadata entry %q, expected key=value", entry)
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			return nil, fmt.Errorf("metadata key cannot be empty (%q)", entry)
		}
		meta[key] = strings.TrimSpace(parts[1])
	}
	return meta, nil
}

func requireToken(token string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", errors.New("Cloudflare API token is required (set --token or CLOUDFLARE_DNS_BACKUP_TOKEN)")
	}
	return token, nil
}

func loadEnvFromFlag(cmd *cobra.Command) error {
	path := mustGetStringFlag(cmd, "env")
	if path == "" {
		return nil
	}
	if err := godotenv.Overload(path); err != nil {
		return fmt.Errorf("load env file: %w", err)
	}
	return nil
}

func parseServerRange(pattern string) (string, int, int, map[int]bool, error) {
	parts := strings.Split(pattern, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return "", 0, 0, nil, fmt.Errorf("invalid server range format, expected 'pattern:start-end' or 'pattern:start-end:!exclusions'")
	}

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

	exclusions := make(map[int]bool)
	if len(parts) == 3 {
		exclusionStr := parts[2]
		if !strings.HasPrefix(exclusionStr, "!") {
			return "", 0, 0, nil, fmt.Errorf("exclusions part must start with '!'")
		}
		exclusionStr = strings.TrimPrefix(exclusionStr, "!")

		for _, segment := range strings.Split(exclusionStr, ",") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}
			if strings.Contains(segment, "-") {
				sub := strings.Split(segment, "-")
				if len(sub) != 2 {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion range format: %s", segment)
				}
				subStart, err := strconv.Atoi(sub[0])
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion start number in '%s': %w", segment, err)
				}
				subEnd, err := strconv.Atoi(sub[1])
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion end number in '%s': %w", segment, err)
				}
				if subStart > subEnd {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion range: start > end in %s", segment)
				}
				for i := subStart; i <= subEnd; i++ {
					exclusions[i] = true
				}
			} else {
				exNum, err := strconv.Atoi(segment)
				if err != nil {
					return "", 0, 0, nil, fmt.Errorf("invalid exclusion number '%s': %w", segment, err)
				}
				exclusions[exNum] = true
			}
		}
	}

	return parts[0], start, end, exclusions, nil
}

func sanitizeZoneForFilename(zone string) string {
	zone = strings.ToLower(strings.TrimSpace(zone))
	if zone == "" {
		return "zone"
	}
	var b strings.Builder
	for _, r := range zone {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	clean := strings.Trim(b.String(), "-")
	if clean == "" {
		return "zone"
	}
	return clean
}

func resolveZoneFilePath(base, zone, defaultPattern, flagName string, multi bool, allowEmpty bool) (string, bool, error) {
	sanitized := sanitizeZoneForFilename(zone)
	sanitizedPattern := defaultPattern
	if strings.Contains(defaultPattern, "%s") {
		sanitizedPattern = fmt.Sprintf(defaultPattern, sanitized)
	}
	if !multi {
		switch {
		case base == "" && allowEmpty:
			return "", true, nil
		case strings.Contains(base, "%s"):
			return fmt.Sprintf(base, sanitized), false, nil
		default:
			return base, false, nil
		}
	}
	if base == "" {
		if !allowEmpty {
			return "", false, fmt.Errorf("%s must be provided when multiple zones are discovered; include '%%s' placeholder or point to a directory", flagName)
		}
		if defaultPattern == "" {
			return "", false, fmt.Errorf("no default pattern available for %s", flagName)
		}
		return sanitizedPattern, false, nil
	}
	if strings.Contains(base, "%s") {
		return fmt.Sprintf(base, sanitized), false, nil
	}
	if info, err := os.Stat(base); err == nil && info.IsDir() {
		if defaultPattern == "" {
			return "", false, fmt.Errorf("when multiple zones are discovered, %s must include '%%s' placeholder", flagName)
		}
		return filepath.Join(base, sanitizedPattern), false, nil
	}
	if strings.HasSuffix(base, string(os.PathSeparator)) {
		if defaultPattern == "" {
			return "", false, fmt.Errorf("when multiple zones are discovered, %s must include '%%s' placeholder", flagName)
		}
		dir := strings.TrimRight(base, string(os.PathSeparator))
		if dir == "" {
			dir = "."
		}
		return filepath.Join(dir, sanitizedPattern), false, nil
	}
	return "", false, fmt.Errorf("when --zone-lookup yields multiple zones, %s must include '%%s' placeholder or point to a directory", flagName)
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func formatExtension(format string) string {
	switch strings.ToLower(format) {
	case "yaml", "yml":
		return "yaml"
	default:
		return "json"
	}
}

func currentUsername() string {
	if fallback := os.Getenv("SSH_DEFAULT_USER"); fallback != "" {
		return fallback
	}
	return "root"
}

func summarizePlan(plan *dnsbackup.Plan) string {
	if plan == nil {
		return "no plan"
	}
	var creates, updates, deletes int
	for _, change := range plan.Changes {
		switch change.Type {
		case dnsbackup.ChangeCreate:
			creates++
		case dnsbackup.ChangeUpdate:
			updates++
		case dnsbackup.ChangeDelete:
			deletes++
		}
	}
	return fmt.Sprintf("Plan includes %d change(s): %d create, %d update, %d delete", len(plan.Changes), creates, updates, deletes)
}

func describeChange(change dnsbackup.RecordChange) string {
	var targetName string
	if change.Desired.Name != "" {
		targetName = change.Desired.Name
	} else if change.Existing != nil {
		targetName = change.Existing.Name
	}
	typeName := change.Desired.Type
	if typeName == "" && change.Existing != nil {
		typeName = change.Existing.Type
	}
	switch change.Type {
	case dnsbackup.ChangeCreate:
		return fmt.Sprintf("create %s %s -> %s", typeName, targetName, change.Desired.Content)
	case dnsbackup.ChangeUpdate:
		return fmt.Sprintf("update %s %s (%d field(s))", typeName, targetName, len(change.Differences))
	case dnsbackup.ChangeDelete:
		return fmt.Sprintf("delete %s %s", typeName, targetName)
	default:
		return fmt.Sprintf("%s %s %s", change.Type, typeName, targetName)
	}
}
