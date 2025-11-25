package dnsbackup

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SavePlan persists a plan in the requested format.
func SavePlan(plan *Plan, path, format string, pretty bool) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}
	if format == "" {
		format = detectFormatFromPath(path)
	}
	content, err := encodePlan(plan, format, pretty)
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}

// EncodePlan serializes the plan to either JSON or YAML.
func EncodePlan(plan *Plan, format string, pretty bool) ([]byte, error) {
	return encodePlan(plan, format, pretty)
}

func encodePlan(plan *Plan, format string, pretty bool) ([]byte, error) {
	switch strings.ToLower(format) {
	case "yaml", "yml":
		return yaml.Marshal(plan)
	default:
		if pretty {
			return json.MarshalIndent(plan, "", "  ")
		}
		return json.Marshal(plan)
	}
}
