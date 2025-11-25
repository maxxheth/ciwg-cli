package dnsbackup

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotSaveAndLoad(t *testing.T) {
	snapshot := &ZoneSnapshot{
		ZoneID:   "zone-id",
		ZoneName: "example.com",
		Exported: time.Now().UTC(),
		Records: []Record{
			{ID: "rec", Type: "A", Name: "example.com", Content: "1.1.1.1", TTL: 120},
		},
	}
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "snapshot.json")
	if err := SaveSnapshot(snapshot, jsonPath, "json", true); err != nil {
		t.Fatalf("save json snapshot: %v", err)
	}
	reloaded, err := LoadSnapshot(jsonPath, "")
	if err != nil {
		t.Fatalf("load json snapshot: %v", err)
	}
	if reloaded.ZoneName != snapshot.ZoneName {
		t.Fatalf("expected zone name %s, got %s", snapshot.ZoneName, reloaded.ZoneName)
	}
	if len(reloaded.Records) != 1 || reloaded.Records[0].Content != "1.1.1.1" {
		t.Fatalf("snapshot content mismatch: %#v", reloaded.Records)
	}

	yamlPath := filepath.Join(dir, "snapshot.yaml")
	if err := SaveSnapshot(snapshot, yamlPath, "yaml", false); err != nil {
		t.Fatalf("save yaml snapshot: %v", err)
	}
	if _, err := LoadSnapshot(yamlPath, ""); err != nil {
		t.Fatalf("load yaml snapshot: %v", err)
	}
}

func TestEncodePlan(t *testing.T) {
	plan := &Plan{
		ZoneID:    "zone",
		ZoneName:  "example.com",
		Generated: time.Now().UTC(),
		Changes:   []RecordChange{{Type: ChangeCreate, Desired: Record{Name: "example.com", Type: "TXT", Content: "hello", TTL: 60}}},
	}
	if _, err := EncodePlan(plan, "json", true); err != nil {
		t.Fatalf("encode json plan: %v", err)
	}
	if _, err := EncodePlan(plan, "yaml", false); err != nil {
		t.Fatalf("encode yaml plan: %v", err)
	}
}
