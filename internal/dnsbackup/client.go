package dnsbackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	cloudflare "github.com/cloudflare/cloudflare-go"
	"golang.org/x/net/publicsuffix"
)

// Client wraps the Cloudflare API client with higher-level helpers tailored for backups.
type Client struct {
	api *cloudflare.API
}

// NewClient instantiates a Client using an API token.
func NewClient(apiToken string) (*Client, error) {
	if strings.TrimSpace(apiToken) == "" {
		return nil, errors.New("cloudflare token is required")
	}
	api, err := cloudflare.NewWithAPIToken(apiToken)
	if err != nil {
		return nil, fmt.Errorf("init cloudflare client: %w", err)
	}
	return &Client{api: api}, nil
}

// Export retrieves all records for a zone and returns a snapshot.
func (c *Client) Export(ctx context.Context, zoneName string) (*ZoneSnapshot, error) {
	zoneID, err := c.api.ZoneIDByName(zoneName)
	if err != nil {
		return nil, fmt.Errorf("resolve zone %s: %w", zoneName, err)
	}
	records, err := c.fetchRecords(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	return &ZoneSnapshot{
		ZoneID:   zoneID,
		ZoneName: zoneName,
		Exported: time.Now().UTC(),
		Records:  records,
	}, nil
}

// Plan generates a change plan that would reconcile the target zone with the provided snapshot.
func (c *Client) Plan(ctx context.Context, zoneName string, snapshot *ZoneSnapshot, opts PlanOptions) (*Plan, error) {
	if snapshot == nil {
		return nil, errors.New("snapshot is required")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	zoneID, err := c.api.ZoneIDByName(zoneName)
	if err != nil {
		return nil, fmt.Errorf("resolve zone %s: %w", zoneName, err)
	}
	liveRecords, err := c.fetchRecords(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	plan := buildPlan(zoneID, zoneName, snapshot.Records, liveRecords, opts)
	return plan, nil
}

// Apply executes each change in the plan against Cloudflare unless DryRun is true.
func (c *Client) Apply(ctx context.Context, plan *Plan, opts ApplyOptions) error {
	if plan == nil {
		return errors.New("plan is required")
	}
	if len(plan.Changes) == 0 {
		return nil
	}
	if strings.TrimSpace(plan.ZoneID) == "" {
		return errors.New("plan is missing zone identifier")
	}
	rc := cloudflare.ZoneIdentifier(plan.ZoneID)
	for _, change := range plan.Changes {
		if opts.DryRun {
			continue
		}
		if err := c.applyChange(ctx, rc, change); err != nil {
			return err
		}
	}
	return nil
}

// ResolveZoneName attempts to find the Cloudflare zone that owns the provided host.
func (c *Client) ResolveZoneName(host string) (string, error) {
	clean := sanitizeCandidateHost(host)
	if clean == "" {
		return "", errors.New("host is required to resolve zone")
	}
	candidates := zoneCandidates(clean)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := c.api.ZoneIDByName(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no Cloudflare zone matches host %s", clean)
}

// VerifyToken checks that the configured token is valid and returns metadata.
func (c *Client) VerifyToken(ctx context.Context) (cloudflare.APITokenVerifyBody, error) {
	return c.api.VerifyAPIToken(ctx)
}

func (c *Client) applyChange(ctx context.Context, rc *cloudflare.ResourceContainer, change RecordChange) error {
	switch change.Type {
	case ChangeCreate:
		_, err := c.api.CreateDNSRecord(ctx, rc, toCreateParams(change.Desired))
		return err
	case ChangeUpdate:
		recordID := ""
		if change.Existing != nil {
			recordID = change.Existing.ID
		}
		if recordID == "" {
			return fmt.Errorf("cannot update record %s without identifier", change.Desired.Name)
		}
		params := toUpdateParams(recordID, change.Desired)
		_, err := c.api.UpdateDNSRecord(ctx, rc, params)
		return err
	case ChangeDelete:
		if change.Existing == nil || change.Existing.ID == "" {
			return fmt.Errorf("cannot delete record without identifier")
		}
		return c.api.DeleteDNSRecord(ctx, rc, change.Existing.ID)
	default:
		return fmt.Errorf("unsupported change type %s", change.Type)
	}
}

func (c *Client) fetchRecords(ctx context.Context, zoneID string) ([]Record, error) {
	rc := cloudflare.ZoneIdentifier(zoneID)
	params := cloudflare.ListDNSRecordsParams{}
	params.ResultInfo.PerPage = 500
	var all []Record
	for {
		records, info, err := c.api.ListDNSRecords(ctx, rc, params)
		if err != nil {
			return nil, fmt.Errorf("list dns records: %w", err)
		}
		for _, rec := range records {
			all = append(all, fromAPIRecord(rec))
		}
		if info == nil || info.Page >= info.TotalPages || info.TotalPages == 0 {
			break
		}
		params.ResultInfo.Page = info.Page + 1
		params.ResultInfo.PerPage = info.PerPage
	}
	return all, nil
}

func toCreateParams(rec Record) cloudflare.CreateDNSRecordParams {
	return cloudflare.CreateDNSRecordParams{
		Type:     rec.Type,
		Name:     rec.Name,
		Content:  rec.Content,
		Priority: rec.Priority,
		TTL:      rec.TTL,
		Proxied:  rec.Proxied,
		Comment:  rec.Comment,
		Tags:     rec.Tags,
		Data:     rec.Data,
	}
}

func toUpdateParams(id string, rec Record) cloudflare.UpdateDNSRecordParams {
	comment := &rec.Comment
	if rec.Comment == "" {
		comment = nil
	}
	return cloudflare.UpdateDNSRecordParams{
		ID:       id,
		Type:     rec.Type,
		Name:     rec.Name,
		Content:  rec.Content,
		Priority: rec.Priority,
		TTL:      rec.TTL,
		Proxied:  rec.Proxied,
		Comment:  comment,
		Tags:     rec.Tags,
		Data:     rec.Data,
	}
}

func fromAPIRecord(rec cloudflare.DNSRecord) Record {
	return Record{
		ID:       rec.ID,
		Type:     rec.Type,
		Name:     rec.Name,
		Content:  rec.Content,
		TTL:      rec.TTL,
		Priority: rec.Priority,
		Proxied:  rec.Proxied,
		Comment:  rec.Comment,
		Tags:     normalizeTags(rec.Tags),
		Data:     normalizeData(rec.Data),
	}
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	clone := append([]string{}, tags...)
	sort.Strings(clone)
	return clone
}

func normalizeData(data interface{}) map[string]any {
	if data == nil {
		return nil
	}
	switch typed := data.(type) {
	case map[string]interface{}:
		return typed
	default:
		b, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			return nil
		}
		return out
	}
}

func sanitizeCandidateHost(host string) string {
	value := strings.TrimSpace(strings.ToLower(host))
	value = strings.Trim(value, ".")
	value = strings.TrimPrefix(value, "www.")
	return value
}

func zoneCandidates(host string) []string {
	seen := make(map[string]struct{})
	var candidates []string

	if etld, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		addZoneCandidate(&candidates, seen, etld)
	}

	labels := strings.Split(host, ".")
	for i := 0; i <= len(labels)-2; i++ {
		candidate := strings.Join(labels[i:], ".")
		addZoneCandidate(&candidates, seen, candidate)
	}

	return candidates
}

func addZoneCandidate(list *[]string, seen map[string]struct{}, candidate string) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return
	}
	if _, exists := seen[candidate]; exists {
		return
	}
	seen[candidate] = struct{}{}
	*list = append(*list, candidate)
}
