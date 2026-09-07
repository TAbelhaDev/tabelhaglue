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
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Steps       []Step `toml:"steps"`
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
		name := w.Name
		if name == "" {
			name = strings.TrimSuffix(f.Name(), ".toml")
		}
		entries = append(entries, workflowEntry{Name: name, Description: w.Description, Path: path})
	}
	return entries, nil
}
