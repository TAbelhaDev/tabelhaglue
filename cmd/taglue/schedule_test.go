package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnitName(t *testing.T) {
	got := unitName("project-selfdoc")
	want := "project-selfdoc"
	if got != want {
		t.Errorf("unitName = %q, want %q", got, want)
	}
}

func TestTimerPath(t *testing.T) {
	got := timerPath("project-selfdoc")
	if !filepath.IsAbs(got) {
		t.Errorf("timerPath is not absolute: %q", got)
	}
	if filepath.Ext(got) != ".timer" {
		t.Errorf("timerPath ext = %q, want .timer", got)
	}
	if filepath.Base(got) != "project-selfdoc.timer" {
		t.Errorf("timerPath base = %q, want project-selfdoc.timer", filepath.Base(got))
	}
}

func TestServicePath(t *testing.T) {
	got := servicePath("project-selfdoc")
	if filepath.Ext(got) != ".service" {
		t.Errorf("servicePath ext = %q, want .service", got)
	}
}

func TestJobsDir(t *testing.T) {
	got := jobsDir("project-selfdoc")
	if !filepath.IsAbs(got) {
		t.Errorf("jobsDir is not absolute: %q", got)
	}
	if filepath.Base(got) != "project-selfdoc" {
		t.Errorf("jobsDir base = %q, want project-selfdoc", filepath.Base(got))
	}
}

func TestLogPath(t *testing.T) {
	got := logPath("project-selfdoc")
	if !filepath.IsAbs(got) {
		t.Errorf("logPath is not absolute: %q", got)
	}
	if filepath.Ext(got) != ".log" {
		t.Errorf("logPath ext = %q, want .log", got)
	}
	// Log should be inside ~/jobs/<name>/
	if filepath.Dir(got) != filepath.Dir(filepath.Join(jobsDir("project-selfdoc"), "project-selfdoc.log")) {
		t.Errorf("logPath dir = %q, want jobs dir", filepath.Dir(got))
	}
}

func TestScriptPath(t *testing.T) {
	got := scriptPath("project-selfdoc")
	if !filepath.IsAbs(got) {
		t.Errorf("scriptPath is not absolute: %q", got)
	}
	if filepath.Ext(got) != ".sh" {
		t.Errorf("scriptPath ext = %q, want .sh", got)
	}
	if filepath.Dir(got) != jobsDir("project-selfdoc") {
		t.Errorf("scriptPath dir = %q, want jobsDir", filepath.Dir(got))
	}
}

func TestIsScheduled_NoUnits(t *testing.T) {
	// Use a temp dir to avoid interference with real units
	// IsScheduled uses the real systemd path, so we just test that
	// a nonexistent name returns false
	got := IsScheduled("nonexistent-workflow-xyz-12345")
	if got {
		t.Error("IsScheduled returned true for nonexistent workflow")
	}
}

func TestIsScheduled_WithUnits(t *testing.T) {
	// Create a temp timer file in the real systemd dir (risky but fast)
	// IsScheduled uses the real systemd path, so we just test that
	// the path convention is correct
	timer := timerPath("test-roundtrip")
	service := servicePath("test-roundtrip")

	// Clean up after ourselves
	defer func() {
		os.Remove(timer)
		os.Remove(service)
	}()

	// No units yet
	if IsScheduled("test-roundtrip") {
		t.Error("IsScheduled returned true before creating units")
	}

	// Create the timer file
	if err := os.MkdirAll(filepath.Dir(timer), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(timer, []byte("[Timer]\nOnCalendar=*-*-* 12:00:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Now it should be scheduled
	if !IsScheduled("test-roundtrip") {
		t.Error("IsScheduled returned false after creating timer file")
	}

	// Remove the timer file
	os.Remove(timer)
	if IsScheduled("test-roundtrip") {
		t.Error("IsScheduled returned true after removing timer file")
	}
}

func TestValidateCalendar_Valid(t *testing.T) {
	err := validateCalendar("*-*-* 12:00:00")
	if err != nil {
		t.Errorf("validateCalendar returned error for valid expr: %v", err)
	}
}

func TestValidateCalendar_Invalid(t *testing.T) {
	err := validateCalendar("not-a-calendar")
	if err == nil {
		t.Error("validateCalendar returned nil for invalid expr")
	}
}
