package dnsbackup

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type liveEntry struct {
	record  Record
	matched bool
}

func buildPlan(zoneID, zoneName string, desired, live []Record, opts PlanOptions) *Plan {
	plan := &Plan{
		ZoneID:    zoneID,
		ZoneName:  zoneName,
		Generated: time.Now().UTC(),
	}

	idIndex := make(map[string]*liveEntry)
	identityIndex := make(map[string][]*liveEntry)
	var entries []*liveEntry

	for _, rec := range live {
		entry := &liveEntry{record: cloneRecord(rec)}
		entries = append(entries, entry)
		if entry.record.ID != "" {
			idIndex[entry.record.ID] = entry
		}
		key := recordIdentity(entry.record)
		identityIndex[key] = append(identityIndex[key], entry)
	}

	for _, rec := range desired {
		desiredRecord := cloneRecord(rec)
		var entry *liveEntry
		if desiredRecord.ID != "" {
			entry = idIndex[desiredRecord.ID]
		}
		if entry == nil {
			key := recordIdentity(desiredRecord)
			if list := identityIndex[key]; len(list) > 0 {
				entry = list[0]
				identityIndex[key] = list[1:]
			}
		}
		if entry == nil {
			plan.Changes = append(plan.Changes, RecordChange{Type: ChangeCreate, Desired: desiredRecord})
			continue
		}
		entry.matched = true
		diffs := diffRecords(desiredRecord, entry.record)
		if len(diffs) == 0 {
			continue
		}
		existingCopy := cloneRecord(entry.record)
		plan.Changes = append(plan.Changes, RecordChange{
			Type:        ChangeUpdate,
			Desired:     desiredRecord,
			Existing:    &existingCopy,
			Differences: diffs,
		})
	}

	if opts.DeleteExtraneous {
		for _, entry := range entries {
			if entry.matched {
				continue
			}
			existingCopy := cloneRecord(entry.record)
			plan.Changes = append(plan.Changes, RecordChange{
				Type:     ChangeDelete,
				Existing: &existingCopy,
			})
		}
	}

	return plan
}

func recordIdentity(rec Record) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(rec.Type))
	b.WriteRune('|')
	b.WriteString(normalizeName(rec.Name))
	b.WriteRune('|')
	b.WriteString(rec.Content)
	b.WriteRune('|')
	b.WriteString(priorityKey(rec.Priority))
	b.WriteRune('|')
	b.WriteString(dataSignature(rec.Data))
	return b.String()
}

func normalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), ".")
}

func priorityKey(priority *uint16) string {
	if priority == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*priority), 10)
}

func dataSignature(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	return string(bytes)
}

func diffRecords(desired, existing Record) map[string]Difference {
	diffs := make(map[string]Difference)
	if desired.Content != existing.Content {
		diffs["content"] = Difference{From: existing.Content, To: desired.Content}
	}
	if desired.TTL != existing.TTL {
		diffs["ttl"] = Difference{From: existing.TTL, To: desired.TTL}
	}
	if !equalPriority(desired.Priority, existing.Priority) {
		diffs["priority"] = Difference{From: copyPriority(existing.Priority), To: copyPriority(desired.Priority)}
	}
	if !equalBoolPtr(desired.Proxied, existing.Proxied) {
		diffs["proxied"] = Difference{From: copyBool(existing.Proxied), To: copyBool(desired.Proxied)}
	}
	if desired.Comment != existing.Comment {
		diffs["comment"] = Difference{From: existing.Comment, To: desired.Comment}
	}
	if !tagsEqual(desired.Tags, existing.Tags) {
		diffs["tags"] = Difference{From: sortTags(existing.Tags), To: sortTags(desired.Tags)}
	}
	if !mapsEqual(desired.Data, existing.Data) {
		diffs["data"] = Difference{From: existing.Data, To: desired.Data}
	}
	return diffs
}

func tagsEqual(a, b []string) bool {
	aSorted := sortTags(a)
	bSorted := sortTags(b)
	if len(aSorted) != len(bSorted) {
		return false
	}
	for i := range aSorted {
		if aSorted[i] != bSorted[i] {
			return false
		}
	}
	return true
}

func sortTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	clone := append([]string{}, tags...)
	sort.Strings(clone)
	return clone
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func equalPriority(a, b *uint16) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func equalBoolPtr(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func copyPriority(val *uint16) *uint16 {
	if val == nil {
		return nil
	}
	copy := *val
	return &copy
}

func copyBool(val *bool) *bool {
	if val == nil {
		return nil
	}
	copy := *val
	return &copy
}

func cloneRecord(rec Record) Record {
	clone := rec
	if len(rec.Tags) > 0 {
		clone.Tags = append([]string{}, rec.Tags...)
	}
	if len(rec.Data) > 0 {
		clone.Data = make(map[string]any, len(rec.Data))
		for k, v := range rec.Data {
			clone.Data[k] = v
		}
	}
	return clone
}
