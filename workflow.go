package main

import (
	"os"
	"path/filepath"

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
