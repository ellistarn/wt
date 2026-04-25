package worktree

import (
	"testing"
	"time"
)

func TestSort_ActivityBeforeNoActivity(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{Name: "no-session", CreatedAt: now},                              // just created, no activity
		{Name: "active", CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-2 * time.Minute)}, // old but recently active
	}
	Sort(entries)
	if entries[0].Name != "active" {
		t.Errorf("expected entry with activity to sort first, got %q", entries[0].Name)
	}
}

func TestSort_RecentActivityFirst(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{Name: "older-activity", UpdatedAt: now.Add(-1 * time.Hour)},
		{Name: "recent-activity", UpdatedAt: now.Add(-5 * time.Minute)},
	}
	Sort(entries)
	if entries[0].Name != "recent-activity" {
		t.Errorf("expected most recent activity first, got %q", entries[0].Name)
	}
}

func TestSort_NoActivityByCreatedAt(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{Name: "old-create", CreatedAt: now.Add(-2 * time.Hour)},
		{Name: "new-create", CreatedAt: now},
	}
	Sort(entries)
	if entries[0].Name != "new-create" {
		t.Errorf("expected newest creation first among no-activity entries, got %q", entries[0].Name)
	}
}

func TestSort_NoTimestampsByName(t *testing.T) {
	entries := []Entry{
		{Name: "zebra"},
		{Name: "alpha"},
	}
	Sort(entries)
	if entries[0].Name != "alpha" {
		t.Errorf("expected alphabetical fallback, got %q", entries[0].Name)
	}
}

func TestSort_MixedEntries(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{Name: "no-session-new", CreatedAt: now},
		{Name: "no-session-old", CreatedAt: now.Add(-1 * time.Hour)},
		{Name: "active-recent", UpdatedAt: now.Add(-1 * time.Minute), CreatedAt: now.Add(-2 * time.Hour)},
		{Name: "active-stale", UpdatedAt: now.Add(-3 * time.Hour), CreatedAt: now.Add(-5 * time.Hour)},
		{Name: "no-timestamps"},
	}
	Sort(entries)

	want := []string{"active-recent", "active-stale", "no-session-new", "no-session-old", "no-timestamps"}
	for i, w := range want {
		if entries[i].Name != w {
			t.Errorf("position %d: expected %q, got %q", i, w, entries[i].Name)
		}
	}
}
