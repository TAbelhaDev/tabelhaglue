package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunWorkflow_SingleStep(t *testing.T) {
	// Create a fake tool script
	dir := t.TempDir()
	fakeTool := filepath.Join(dir, "fake-tool")
	script := `#!/bin/sh
echo '{"result": "ok", "args": "'"$*"'" }'
`
	os.WriteFile(fakeTool, []byte(script), 0o755)

	wf := &Workflow{
		Name: "test",
		Steps: []Step{
			{Tool: fakeTool, Method: "test", Args: map[string]string{"key": "value"}},
		},
	}

	var lines []string
	hooks := runHooks{
		StepStart: func(i, n int, step Step) {
			lines = append(lines, "start")
		},
		StepDone: func(i int, out []byte) {
			lines = append(lines, "done")
		},
	}

	out, err := runWorkflow(context.Background(), wf, hooks)
	if err != nil {
		t.Fatalf("runWorkflow: %v", err)
	}
	if len(out) == 0 {
		t.Error("output is empty")
	}
	if len(lines) != 2 {
		t.Errorf("hooks called %d times, want 2", len(lines))
	}
}

func TestRunWorkflow_MultiStep(t *testing.T) {
	dir := t.TempDir()
	fakeTool := filepath.Join(dir, "fake-tool")
	script := `#!/bin/sh
echo '{"value": "42"}'
`
	os.WriteFile(fakeTool, []byte(script), 0o755)

	wf := &Workflow{
		Name: "test",
		Steps: []Step{
			{Tool: fakeTool, Method: "step1"},
			{Tool: fakeTool, Method: "step2"},
			{Tool: fakeTool, Method: "step3"},
		},
	}

	var stepCount int
	hooks := runHooks{
		StepStart: func(i, n int, step Step) {
			stepCount++
			if stepCount == 1 && step.Method != "step1" {
				t.Errorf("first step method = %q, want step1", step.Method)
			}
		},
	}

	_, err := runWorkflow(context.Background(), wf, hooks)
	if err != nil {
		t.Fatalf("runWorkflow: %v", err)
	}
	if stepCount != 3 {
		t.Errorf("stepCount = %d, want 3", stepCount)
	}
}

func TestRunWorkflow_Timeout(t *testing.T) {
	dir := t.TempDir()
	slowTool := filepath.Join(dir, "slow-tool")
	script := `#!/bin/sh
sleep 10
echo '{"result": "too late"}'
`
	os.WriteFile(slowTool, []byte(script), 0o755)

	// Set a very short timeout
	shortTimeout := 1
	wf := &Workflow{
		Name: "test-timeout",
		Steps: []Step{
			{Tool: slowTool, Method: "slow", TimeoutSeconds: &shortTimeout},
		},
	}

	_, err := runWorkflow(context.Background(), wf, runHooks{})
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestRunWorkflow_DefaultTimeout(t *testing.T) {
	dir := t.TempDir()
	fastTool := filepath.Join(dir, "fast-tool")
	script := `#!/bin/sh
echo '{"result": "fast"}'
`
	os.WriteFile(fastTool, []byte(script), 0o755)

	// No timeout_seconds set, should use default (30s)
	wf := &Workflow{
		Name: "test-default-timeout",
		Steps: []Step{
			{Tool: fastTool, Method: "fast"},
		},
	}

	_, err := runWorkflow(context.Background(), wf, runHooks{})
	if err != nil {
		t.Fatalf("runWorkflow: %v", err)
	}
}

func TestRunWorkflow_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	slowTool := filepath.Join(dir, "slow-tool")
	// Use a direct sleep binary, not shell script (shell doesn't propagate SIGKILL)
	script := `#!/bin/sh
exec sleep 10
`
	os.WriteFile(slowTool, []byte(script), 0o755)

	wf := &Workflow{
		Name: "test-cancel",
		Steps: []Step{
			{Tool: slowTool, Method: "slow"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runWorkflow(ctx, wf, runHooks{})
		done <- err
	}()

	// Cancel after 1s — exec.CommandContext sends SIGKILL
	time.Sleep(1 * time.Second)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected cancellation error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Error("runWorkflow didn't finish after cancellation")
	}
}

func TestRunWorkflow_StderrCapture(t *testing.T) {
	dir := t.TempDir()
	fakeTool := filepath.Join(dir, "fake-tool")
	script := `#!/bin/sh
echo "warning line" >&2
echo '{"result": "ok"}'
`
	os.WriteFile(fakeTool, []byte(script), 0o755)

	wf := &Workflow{
		Name: "test-stderr",
		Steps: []Step{
			{Tool: fakeTool, Method: "test"},
		},
	}

	var stderrLines []string
	hooks := runHooks{
		StepLine: func(i int, line string) {
			stderrLines = append(stderrLines, line)
		},
	}

	_, err := runWorkflow(context.Background(), wf, hooks)
	if err != nil {
		t.Fatalf("runWorkflow: %v", err)
	}
	if len(stderrLines) != 1 || stderrLines[0] != "warning line" {
		t.Errorf("stderr lines = %v, want [warning line]", stderrLines)
	}
}

func TestRunWorkflow_ToolNotFound(t *testing.T) {
	wf := &Workflow{
		Name: "test-notfound",
		Steps: []Step{
			{Tool: "/nonexistent/tool", Method: "test"},
		},
	}

	_, err := runWorkflow(context.Background(), wf, runHooks{})
	if err == nil {
		t.Error("expected error for nonexistent tool, got nil")
	}
}

func TestRunWorkflow_EmptySteps(t *testing.T) {
	wf := &Workflow{
		Name:  "test-empty",
		Steps: []Step{},
	}

	out, err := runWorkflow(context.Background(), wf, runHooks{})
	if err != nil {
		t.Fatalf("runWorkflow: %v", err)
	}
	if out != nil {
		t.Errorf("output = %v, want nil for empty steps", out)
	}
}

func TestExtractField_Simple(t *testing.T) {
	data := json.RawMessage(`{"name": "test", "count": 42}`)
	if got := extractField(data, "name"); got != "test" {
		t.Errorf("extractField(name) = %q, want %q", got, "test")
	}
	if got := extractField(data, "count"); got != "42" {
		t.Errorf("extractField(count) = %q, want %q", got, "42")
	}
}

func TestExtractField_Nested(t *testing.T) {
	data := json.RawMessage(`{"nested": {"key": "deep"}}`)
	if got := extractField(data, "nested.key"); got != "deep" {
		t.Errorf("extractField(nested.key) = %q, want %q", got, "deep")
	}
}

func TestExtractField_Missing(t *testing.T) {
	data := json.RawMessage(`{"name": "test"}`)
	got := extractField(data, "missing")
	// Missing field in non-map falls through to raw
	if got == "" {
		t.Error("extractField(missing) returned empty string")
	}
}
