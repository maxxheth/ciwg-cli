package dnsbackup

import (
	"errors"
	"fmt"
	"os"
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

func mustGetStringFlag(cmd *cobra.Command, name string) string {
	val, _ := cmd.Flags().GetString(name)
	return val
}

func mustGetBoolFlag(cmd *cobra.Command, name string) bool {
	val, _ := cmd.Flags().GetBool(name)
	return val
}

func mustGetStringSliceFlag(cmd *cobra.Command, name string) []string {
	val, _ := cmd.Flags().GetStringSlice(name)
	return val
}

func mustGetDurationFlag(cmd *cobra.Command, name string) time.Duration {
	val, _ := cmd.Flags().GetDuration(name)
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
