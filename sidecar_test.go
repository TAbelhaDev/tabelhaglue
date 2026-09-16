package main

import (
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSidecarRoundTrip(t *testing.T) {
	// Create temp sidecar
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.toml")

	// Write a schedule
	s := Schedule{
		Kind:   "daily",
		Hour:   21,
		Minute: 0,
	}
	if err := writeSidecar(path, map[string]Schedule{"test-wf": s}); err != nil {
		t.Fatalf("writeSidecar: %v", err)
	}

	// Read it back
	m := make(map[string]Schedule)
	if _, err := toml.DecodeFile(path, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m))
	}
	got := m["test-wf"]
	if got.Kind != "daily" || got.Hour != 21 || got.Minute != 0 {
		t.Errorf("round-trip: got %+v, want kind=daily hour=21 minute=0", got)
	}
}

func TestSidecarPreservesEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.toml")

	// Write two entries
	m := map[string]Schedule{
		"wf1": {Kind: "daily", Hour: 7, Minute: 0},
		"wf2": {Kind: "weekly", Hour: 9, Minute: 0, Weekdays: []string{"Mon", "Fri"}},
	}
	if err := writeSidecar(path, m); err != nil {
		t.Fatalf("writeSidecar: %v", err)
	}

	// Add a third entry via saveSchedule (read-modify-write)
	// We can't call saveSchedule directly since it uses tuiui.ConfigDir()
	// But we can test writeSidecar preserves entries
	m["wf3"] = Schedule{Kind: "monthly", Hour: 14, Minute: 30, DayOfMonth: 15}
	if err := writeSidecar(path, m); err != nil {
		t.Fatalf("writeSidecar: %v", err)
	}

	// Read back all three
	got := make(map[string]Schedule)
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	if got["wf1"].Hour != 7 {
		t.Errorf("wf1 hour: got %d, want 7", got["wf1"].Hour)
	}
	if got["wf2"].Kind != "weekly" {
		t.Errorf("wf2 kind: got %s, want weekly", got["wf2"].Kind)
	}
	if got["wf3"].DayOfMonth != 15 {
		t.Errorf("wf3 day_of_month: got %d, want 15", got["wf3"].DayOfMonth)
	}
}

func TestSidecarEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.toml")

	// Write empty map
	if err := writeSidecar(path, map[string]Schedule{}); err != nil {
		t.Fatalf("writeSidecar: %v", err)
	}

	// Read back
	m := make(map[string]Schedule)
	if _, err := toml.DecodeFile(path, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected 0 entries, got %d", len(m))
	}
}

func TestScheduleToSchedule(t *testing.T) {
	tests := []struct {
		input    Schedule
		wantKind string
	}{
		{Schedule{Kind: "daily", Hour: 21, Minute: 0}, "daily"},
		{Schedule{Kind: "weekly", Hour: 9, Minute: 0, Weekdays: []string{"Mon", "Tue"}}, "weekly"},
		{Schedule{Kind: "monthly", Hour: 8, Minute: 30, DayOfMonth: 15}, "monthly"},
		{Schedule{Kind: "oneshot", Hour: 14, Minute: 0, DOM: 12, Month: 9}, "oneshot"},
		{Schedule{Kind: "cycle", Cycle: []int{2, 4, 5}}, "cycle"},
		{Schedule{Kind: "manual"}, "manual"},
	}
	for _, tt := range tests {
		s := scheduleToSchedule(tt.input)
		if s.String() == "" {
			t.Errorf("scheduleToSchedule(%+v).String() is empty", tt.input)
		}
	}
}

func TestScheduleString(t *testing.T) {
	tests := []struct {
		input Schedule
		want  string
	}{
		// Raw OnCalendar only (no Kind): show raw
		{Schedule{OnCalendar: "*-*-* 21:00:00"}, "*-*-* 21:00:00"},
		// Structured Kind: always prefer structured, even with raw OnCalendar present
		{Schedule{Kind: "daily", Hour: 21, Minute: 0}, "diário 21:00"},
		{Schedule{OnCalendar: "*-*-* 21:00:00", Kind: "daily", Hour: 21, Minute: 0}, "diário 21:00"},
		{Schedule{Kind: "manual"}, "manual"},
	}
	for _, tt := range tests {
		got := scheduleString(tt.input)
		if got != tt.want {
			t.Errorf("scheduleString(%+v): got %q, want %q", tt.input, got, tt.want)
		}
	}
}
