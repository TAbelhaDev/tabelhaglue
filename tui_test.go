package main

import (
	"testing"

	"github.com/TAbelhaDev/tabelhatuiui/schedule"
	"github.com/charmbracelet/huh"
	tea "github.com/charmbracelet/bubbletea"
)

// TestScheduleFormCompletionDoesNotPanic is a regression test for the nil
// dereference that crashed the TUI when a schedule form was submitted.
// The old code set m.scheduleForm = nil then called form.Get() on it.
func TestScheduleFormCompletionDoesNotPanic(t *testing.T) {
	form := huh.NewForm(schedule.Groups(schedule.Schedule{})...)
	form.State = huh.StateCompleted

	m := tuiModel{
		scheduleForm: form,
		scheduleEntry: &workflowEntry{
			File: "test",
			Path: t.TempDir() + "/test.toml",
			WF:   &Workflow{},
		},
	}

	// With empty results, kind == KindOneshot (zero value), ParseHHMM("")
	// errors → status "horário inválido". The key point: no panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Update panicked on schedule form completion: %v", r)
		}
	}()
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = got
}
