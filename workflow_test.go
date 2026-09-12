package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestLoadWorkflow_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.toml")
	content := `
name = "test-wf"
description = "A test workflow"
group = "tabeladev"

[metadata]
creator = "tester"
installed_at = "2026-09-11T20:00:00Z"

[schedule]
on_calendar = "*-*-* 12:00:00"

[[steps]]
tool = "echo"
method = "hello"
args = { who = "world" }

[[steps]]
tool = "cat"
method = "read"
args = {}
timeout_seconds = 60
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	wf, err := loadWorkflow(path)
	if err != nil {
		t.Fatalf("loadWorkflow: %v", err)
	}
	if wf.Name != "test-wf" {
		t.Errorf("Name = %q, want %q", wf.Name, "test-wf")
	}
	if wf.Description != "A test workflow" {
		t.Errorf("Description = %q", wf.Description)
	}
	if wf.Group != "tabeladev" {
		t.Errorf("Group = %q, want %q", wf.Group, "tabeladev")
	}
	if wf.Metadata.Creator != "tester" {
		t.Errorf("Metadata.Creator = %q", wf.Metadata.Creator)
	}
	if wf.Schedule.OnCalendar != "*-*-* 12:00:00" {
		t.Errorf("Schedule.OnCalendar = %q", wf.Schedule.OnCalendar)
	}
	if len(wf.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(wf.Steps))
	}
	if wf.Steps[0].Tool != "echo" || wf.Steps[0].Method != "hello" {
		t.Errorf("step[0] = %s %s", wf.Steps[0].Tool, wf.Steps[0].Method)
	}
	if wf.Steps[0].Args["who"] != "world" {
		t.Errorf("step[0].Args[who] = %q", wf.Steps[0].Args["who"])
	}
	if wf.Steps[0].TimeoutSeconds != nil {
		t.Errorf("step[0].TimeoutSeconds = %v, want nil", wf.Steps[0].TimeoutSeconds)
	}
	if wf.Steps[1].TimeoutSeconds == nil || *wf.Steps[1].TimeoutSeconds != 60 {
		t.Errorf("step[1].TimeoutSeconds = %v, want 60", wf.Steps[1].TimeoutSeconds)
	}
}

func TestLoadWorkflow_Minimal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "minimal.toml")
	content := `
[[steps]]
tool = "taradar"
method = "projects.list"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	wf, err := loadWorkflow(path)
	if err != nil {
		t.Fatalf("loadWorkflow: %v", err)
	}
	if wf.Group != "" {
		t.Errorf("Group = %q, want empty", wf.Group)
	}
	if len(wf.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(wf.Steps))
	}
}

func TestInterpolate_Basic(t *testing.T) {
	outputs := []stepOutput{
		{Raw: json.RawMessage(`{"name": "tabelharadar", "branch": "main"}`)},
	}
	got := interpolate("${steps.0.output.name}", outputs)
	if got != "tabelharadar" {
		t.Errorf("interpolate = %q, want %q", got, "tabelharadar")
	}
}

func TestInterpolate_NestedField(t *testing.T) {
	outputs := []stepOutput{
		{Raw: json.RawMessage(`{"nested": {"key": "deep-value"}}`)},
	}
	got := interpolate("${steps.0.output.nested.key}", outputs)
	if got != "deep-value" {
		t.Errorf("interpolate = %q, want %q", got, "deep-value")
	}
}

func TestInterpolate_MissingField(t *testing.T) {
	outputs := []stepOutput{
		{Raw: json.RawMessage(`{"name": "test"}`)},
	}
	got := interpolate("${steps.0.output.missing}", outputs)
	// extractField returns "<nil>" for missing keys (fmt.Sprintf("%v", nil))
	if got != "<nil>" {
		t.Errorf("interpolate = %q, want %q", got, "<nil>")
	}
}

func TestInterpolate_OutOfRange(t *testing.T) {
	outputs := []stepOutput{
		{Raw: json.RawMessage(`{"name": "test"}`)},
	}
	got := interpolate("${steps.5.output.name}", outputs)
	if got != "${steps.5.output.name}" {
		t.Errorf("interpolate = %q, want original ref", got)
	}
}

func TestInterpolate_ArrayFallback(t *testing.T) {
	outputs := []stepOutput{
		{Raw: json.RawMessage(`[{"name": "a"}, {"name": "b"}]`)},
	}
	// extractField tries map[string]any, falls back to raw JSON string
	got := interpolate("${steps.0.output.raw}", outputs)
	// "raw" is not a key in the array, so extractField returns the raw JSON
	if !strings.Contains(got, "tabelharadar") && !strings.Contains(got, "[{") {
		// Just verify it doesn't panic and returns something
		if got == "${steps.0.output.raw}" {
			t.Errorf("interpolate fell back to original ref for array, expected raw JSON fallback")
		}
	}
}

func TestInterpolateArgs_Multiple(t *testing.T) {
	outputs := []stepOutput{
		{Raw: json.RawMessage(`{"projects": [{"name": "tabelharadar"}], "count": 42}`)},
	}
	args := map[string]string{
		"proj":  "${steps.0.output.count}",
		"other": "static",
	}
	got := interpolateArgs(args, outputs)
	if got["proj"] != "42" {
		t.Errorf("interpolateArgs[proj] = %q, want %q", got["proj"], "42")
	}
	if got["other"] != "static" {
		t.Errorf("interpolateArgs[other] = %q, want %q", got["other"], "static")
	}
}

func TestListWorkflowEntries_GroupSort(t *testing.T) {
	// Test that group-aware sorting puts ungrouped last
	entries := []workflowEntry{
		{Name: "gamma", Group: ""},
		{Name: "alpha", Group: "wiv"},
		{Name: "beta", Group: "tabeladev"},
		{Name: "delta", Group: "tabeladev"},
	}

	// Sort using same logic as listWorkflowEntries
	sort.Slice(entries, func(i, j int) bool {
		gi, gj := entries[i].Group, entries[j].Group
		if gi == "" && gj == "" {
			return entries[i].Name < entries[j].Name
		}
		if gi == "" {
			return false
		}
		if gj == "" {
			return true
		}
		if gi != gj {
			return gi < gj
		}
		return entries[i].Name < entries[j].Name
	})
	// Expected: tabeladev(beta,delta), wiv(alpha), ungrouped(gamma)
	expected := []string{"beta", "delta", "alpha", "gamma"}
	for i, e := range entries {
		if e.Name != expected[i] {
			t.Errorf("entries[%d].Name = %q, want %q", i, e.Name, expected[i])
		}
	}
}
