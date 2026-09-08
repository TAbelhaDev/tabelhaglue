package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/TAbelhaDev/tabelhatuiui"
)

// Workflow is a declarative pipeline: a sequence of IPC calls to tools.
type Workflow struct {
	Name            string   `toml:"name"`
	Description     string   `toml:"description"`
	DescriptionFile string   `toml:"description_file"`
	Metadata        Metadata `toml:"metadata"`
	Schedule        Schedule `toml:"schedule"`
	Steps           []Step   `toml:"steps"`
}

// Metadata records who installed a workflow and when — display-only, shown
// in the TUI's metadata panel.
type Metadata struct {
	Creator     string `toml:"creator"`
	InstalledAt string `toml:"installed_at"` // RFC3339
}

// Schedule holds the systemd OnCalendar expression for a workflow. Whether
// the schedule is actually active is NOT stored here — it's derived from the
// existence of ~/.config/systemd/user/taglue-<nome>.timer (see schedule.go),
// the same convention jobs-tui uses to discover jobs.
type Schedule struct {
	OnCalendar string `toml:"on_calendar"`
}

// Step is one IPC call in a workflow.
type Step struct {
	Tool   string            `toml:"tool"`
	Method string            `toml:"method"`
	Args   map[string]string `toml:"args"`
}

// loadWorkflow reads a workflow TOML file.
func loadWorkflow(path string) (*Workflow, error) {
	var w Workflow
	_, err := toml.DecodeFile(path, &w)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// workflowsDir returns ~/.config/taglue/workflows/.
func workflowsDir() string {
	return filepath.Join(tuiui.ConfigDir(), "taglue", "workflows")
}

// listWorkflows returns all .toml files in the workflows directory.
func listWorkflows() ([]string, error) {
	dir := workflowsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".toml" {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// workflowEntry is a workflow ready to show in the TUI's list: parsed enough
// to display a name/description without the caller re-reading the TOML.
type workflowEntry struct {
	Name        string
	Description string
	Path        string
	// File is the basename without ".toml" — the identity used by
	// run/enable/disable (main.go resolves `taglue run <n>` as a filename,
	// while Name comes from the TOML and can differ from it).
	File string
	// WF is the fully parsed workflow, kept around so the TUI's metadata and
	// description panels don't need to re-read the TOML on every render.
	WF *Workflow
}

// listWorkflowEntries loads every workflow in the workflows directory. A
// malformed TOML file is skipped rather than failing the whole list — one
// broken workflow shouldn't blank out the TUI. A workflow with no `name` set
// falls back to its filename (without the .toml extension).
func listWorkflowEntries() ([]workflowEntry, error) {
	dir := workflowsDir()
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var entries []workflowEntry
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".toml" {
			continue
		}
		path := filepath.Join(dir, f.Name())
		w, err := loadWorkflow(path)
		if err != nil {
			continue
		}
		file := strings.TrimSuffix(f.Name(), ".toml")
		name := w.Name
		if name == "" {
			name = file
		}
		entries = append(entries, workflowEntry{Name: name, Description: w.Description, Path: path, File: file, WF: w})
	}
	return entries, nil
}
