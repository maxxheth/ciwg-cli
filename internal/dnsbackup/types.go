package dnsbackup

import "time"

// ZoneSnapshot captures the state of a Cloudflare zone's DNS records at a point in time.
type ZoneSnapshot struct {
	ZoneID   string         `json:"zone_id" yaml:"zone_id"`
	ZoneName string         `json:"zone_name" yaml:"zone_name"`
	Exported time.Time      `json:"exported_at" yaml:"exported_at"`
	Records  []Record       `json:"records" yaml:"records"`
	Metadata map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// Record represents the subset of DNS record fields that we care about backing up.
type Record struct {
	ID       string         `json:"id,omitempty" yaml:"id,omitempty"`
	Type     string         `json:"type" yaml:"type"`
	Name     string         `json:"name" yaml:"name"`
	Content  string         `json:"content" yaml:"content"`
	TTL      int            `json:"ttl" yaml:"ttl"`
	Priority *uint16        `json:"priority,omitempty" yaml:"priority,omitempty"`
	Proxied  *bool          `json:"proxied,omitempty" yaml:"proxied,omitempty"`
	Comment  string         `json:"comment,omitempty" yaml:"comment,omitempty"`
	Tags     []string       `json:"tags,omitempty" yaml:"tags,omitempty"`
	Data     map[string]any `json:"data,omitempty" yaml:"data,omitempty"`
}

// ChangeType indicates what action is required to reconcile DNS state.
type ChangeType string

const (
	ChangeCreate ChangeType = "create"
	ChangeUpdate ChangeType = "update"
	ChangeDelete ChangeType = "delete"
)

// Difference captures what will change for a field during an update.
type Difference struct {
	From any `json:"from" yaml:"from"`
	To   any `json:"to" yaml:"to"`
}

// RecordChange is a single entry in a reconciliation plan.
type RecordChange struct {
	Type        ChangeType            `json:"type" yaml:"type"`
	Desired     Record                `json:"desired" yaml:"desired"`
	Existing    *Record               `json:"existing,omitempty" yaml:"existing,omitempty"`
	Differences map[string]Difference `json:"differences,omitempty" yaml:"differences,omitempty"`
}

// PlanOptions controls how diffs are generated.
type PlanOptions struct {
	DeleteExtraneous bool
}

// Plan represents the work necessary to reconcile a zone with a snapshot.
type Plan struct {
	ZoneID    string         `json:"zone_id" yaml:"zone_id"`
	ZoneName  string         `json:"zone_name" yaml:"zone_name"`
	Generated time.Time      `json:"generated_at" yaml:"generated_at"`
	Changes   []RecordChange `json:"changes" yaml:"changes"`
}

// ApplyOptions tweak how a plan is applied.
type ApplyOptions struct {
	DryRun bool
}
