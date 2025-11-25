package dnsbackup

import "testing"

func TestBuildPlanCreatesUpdatesDeletes(t *testing.T) {
	snapshotRecords := []Record{
		{ID: "rec-1", Type: "A", Name: "example.com", Content: "1.2.3.4", TTL: 300},
		{ID: "rec-2", Type: "TXT", Name: "example.com", Content: "hello", TTL: 120},
	}
	liveRecords := []Record{
		{ID: "rec-1", Type: "A", Name: "example.com", Content: "1.2.3.5", TTL: 120},
		{ID: "rec-3", Type: "MX", Name: "example.com", Content: "mail.example.com", TTL: 120},
	}
	plan := buildPlan("zone-id", "example.com", snapshotRecords, liveRecords, PlanOptions{DeleteExtraneous: true})

	if got, want := len(plan.Changes), 3; got != want {
		t.Fatalf("expected %d changes, got %d", want, got)
	}

	if plan.Changes[0].Type != ChangeUpdate {
		t.Fatalf("first change should be update, got %s", plan.Changes[0].Type)
	}
	if _, ok := plan.Changes[0].Differences["content"]; !ok {
		t.Fatalf("expected content difference in update change")
	}

	if plan.Changes[1].Type != ChangeCreate {
		t.Fatalf("second change should be create, got %s", plan.Changes[1].Type)
	}
	if plan.Changes[1].Desired.ID != "rec-2" {
		t.Fatalf("unexpected record created: %s", plan.Changes[1].Desired.ID)
	}

	if plan.Changes[2].Type != ChangeDelete {
		t.Fatalf("third change should be delete, got %s", plan.Changes[2].Type)
	}
	if plan.Changes[2].Existing == nil || plan.Changes[2].Existing.ID != "rec-3" {
		t.Fatalf("delete change missing record metadata")
	}
}

func TestBuildPlanSkipDeleteWhenDisabled(t *testing.T) {
	plan := buildPlan("zone", "example.com", []Record{}, []Record{{ID: "rec"}}, PlanOptions{DeleteExtraneous: false})
	if len(plan.Changes) != 0 {
		t.Fatalf("expected no changes when deletions disabled, got %d", len(plan.Changes))
	}
}
