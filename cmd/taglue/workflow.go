package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/TAbelhaDev/tabelhatuiui"
)

// Workflow is a declarative pipeline: a sequence of IPC calls to tools.
type Workflow struct {
	Name            string   `toml:"name"`
	Description     string   `toml:"description"`
	DescriptionFile string   `toml:"description_file"`
	Group           string   `toml:"group"`
	Metadata        Metadata `toml:"metadata"`
	Schedule        Schedule `toml:"schedule"`
	Steps           []Step   `toml:"steps"`
}

// Metadata records who installed a workflow and when — display-only, shown
// in the TUI's metadata panel. UpdatedAt is auto-maintained via the
// metadata.toml sidecar (installed_at on first schedule, updated_at on
// every change).
type Metadata struct {
	Creator     string `toml:"creator"`
	InstalledAt string `toml:"installed_at"` // RFC3339
	UpdatedAt   string `toml:"updated_at"`   // RFC3339
}

// Schedule holds the systemd OnCalendar expression for a workflow. Whether
// the schedule is actually active is NOT stored here — it's derived from the
// existence of ~/.config/systemd/user/taglue-<nome>.timer (see schedule.go),
// the same convention jobs-tui uses to discover jobs.
//
// The structured fields (Kind, Hour, Minute, etc.) are used when the schedule
// is created via the TUI modal. They're stored in a sidecar TOML file
// (~/.config/taglue/schedules.toml) to avoid rewriting the user's workflow TOML.
type Schedule struct {
	OnCalendar string `toml:"on_calendar"` // raw escape hatch (existing)
	// structured fields (from TOML or sidecar):
	Kind       string   `toml:"kind"`         // oneshot|daily|weekly|monthly|cycle|manual
	Hour       int      `toml:"hour"`
	Minute     int      `toml:"minute"`
	Weekdays   []string `toml:"weekdays"`     // ["Mon","Tue",...]
	DayOfMonth int      `toml:"day_of_month"` // monthly
	DOM        int      `toml:"dom"`          // oneshot/cycle first run day
	Month      int      `toml:"month"`        // oneshot/cycle first run month
	Cycle      []int    `toml:"cycle"`        // cycle day-intervals
}

// Step is one IPC call in a workflow.
type Step struct {
	Tool           string            `toml:"tool"`
	Method         string            `toml:"method"`
	Args           map[string]string `toml:"args"`
	TimeoutSeconds *int              `toml:"timeout_seconds"`
}

// loadWorkflow reads a workflow TOML file and merges any sidecar schedule
// and metadata. Sidecar metadata overrides installed_at/updated_at/creator
// when present (sidecar is authoritative for auto-managed fields).
func loadWorkflow(path string) (*Workflow, error) {
	var w Workflow
	_, err := toml.DecodeFile(path, &w)
	if err != nil {
		return nil, err
	}
	file := strings.TrimSuffix(filepath.Base(path), ".toml")

	// Merge sidecar if no raw on_calendar and no structured kind in TOML
	if w.Schedule.OnCalendar == "" && w.Schedule.Kind == "" {
		sidecar, err := loadSchedules()
		if err == nil {
			if s, ok := sidecar[file]; ok {
				w.Schedule = s
			}
		}
	}

	// Merge metadata sidecar: sidecar wins for installed_at/updated_at/creator
	if meta, err := loadMetadata(); err == nil {
		if m, ok := meta[file]; ok {
			if m.Creator != "" {
				w.Metadata.Creator = m.Creator
			}
			if m.InstalledAt != "" {
				w.Metadata.InstalledAt = m.InstalledAt
			}
			if m.UpdatedAt != "" {
				w.Metadata.UpdatedAt = m.UpdatedAt
			}
		}
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
			names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
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
	// Group is the named project group this workflow belongs to
	// (mirroring radar's [[groups]] config). Empty means ungrouped.
	Group string
	// Scheduled is true when a systemd timer unit exists for this workflow.
	// Cached at list load + refresh to avoid os.Stat per render frame.
	Scheduled bool
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
		entries = append(entries, workflowEntry{Name: name, Description: w.Description, Path: path, File: file, Group: w.Group, Scheduled: IsScheduled(file), WF: w})
	}
	sortWorkflowEntries(entries)
	return entries, nil
}

// sortWorkflowEntries orders entries in place: scheduled (ativos) first, then
// group, then name. Callers that mutate an entry's Scheduled field after the
// initial load (e.g. toggling a schedule in the TUI) must call this again to
// keep the list grouped correctly.
func sortWorkflowEntries(entries []workflowEntry) {
	sort.Slice(entries, func(i, j int) bool {
		si, sj := entries[i].Scheduled, entries[j].Scheduled
		if si != sj {
			return si
		}
		gi, gj := entries[i].Group, entries[j].Group
		// Empty group sorts last
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
}
