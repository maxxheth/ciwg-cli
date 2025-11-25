package dnsbackup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	errEmptyZoneName = errors.New("zone name is required")
	errNoRecords     = errors.New("snapshot does not contain any DNS records")
)

// Validate performs basic sanity checks before a snapshot is persisted or applied.
func (s *ZoneSnapshot) Validate() error {
	if s == nil {
		return errors.New("nil snapshot")
	}
	if strings.TrimSpace(s.ZoneName) == "" {
		return errEmptyZoneName
	}
	if len(s.Records) == 0 {
		return errNoRecords
	}
	if s.Exported.IsZero() {
		s.Exported = time.Now().UTC()
	}
	return nil
}

// SaveSnapshot writes the snapshot to disk using the requested serialization format.
func SaveSnapshot(snapshot *ZoneSnapshot, path, format string, pretty bool) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if format == "" {
		format = detectFormatFromPath(path)
	}
	content, err := encodeSnapshot(snapshot, format, pretty)
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}

// EncodeSnapshot serializes the snapshot to JSON or YAML.
func EncodeSnapshot(snapshot *ZoneSnapshot, format string, pretty bool) ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return encodeSnapshot(snapshot, format, pretty)
}

// LoadSnapshot loads a snapshot from disk. Format is inferred from the extension when empty.
func LoadSnapshot(path, format string) (*ZoneSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	if format == "" {
		format = detectFormatFromPath(path)
	}
	return decodeSnapshot(data, format)
}

func encodeSnapshot(snapshot *ZoneSnapshot, format string, pretty bool) ([]byte, error) {
	switch strings.ToLower(format) {
	case "yaml", "yml":
		return yaml.Marshal(snapshot)
	default:
		if pretty {
			return json.MarshalIndent(snapshot, "", "  ")
		}
		return json.Marshal(snapshot)
	}
}

func decodeSnapshot(data []byte, format string) (*ZoneSnapshot, error) {
	s := &ZoneSnapshot{}
	switch strings.ToLower(format) {
	case "yaml", "yml":
		if err := yaml.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("decode yaml snapshot: %w", err)
		}
	default:
		if err := json.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("decode json snapshot: %w", err)
		}
	}
	return s, s.Validate()
}

func detectFormatFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return "yaml"
	default:
		return "json"
	}
}
