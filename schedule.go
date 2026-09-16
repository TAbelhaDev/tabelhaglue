package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/TAbelhaDev/tabelhatuiui"
	"github.com/TAbelhaDev/tabelhatuiui/schedule"
)

// Scheduling follows the jobs-tui convention: a workflow's active/inactive
// state is DERIVED from whether its systemd timer unit file exists — no
// "enabled" flag is stored in the workflow TOML itself, so enabling/disabling
// never needs to rewrite (and risk reformatting) the user's TOML file.

// systemdUserDir is where user-scoped systemd units live.
func systemdUserDir() string {
	return filepath.Join(tuiui.ConfigDir(), "systemd", "user")
}

// unitName is the systemd unit basename (without extension) for a workflow.
// Following tajobs convention: units are named <workflow>.timer/.service
// with no prefix, matching ~/jobs/<workflow>/ discovery.
func unitName(name string) string {
	return name
}

func timerPath(name string) string {
	return filepath.Join(systemdUserDir(), unitName(name)+".timer")
}

func servicePath(name string) string {
	return filepath.Join(systemdUserDir(), unitName(name)+".service")
}

// jobsDir returns the tajobs-compatible job directory for a workflow.
func jobsDir(name string) string {
	return filepath.Join(tuiui.HomeDir(), "jobs", name)
}

// logPath is where the scheduled run's stdout/stderr is appended.
func logPath(name string) string {
	return filepath.Join(jobsDir(name), name+".log")
}

// IsScheduled reports whether a workflow currently has an active systemd
// timer — the .timer file's existence IS the marker, exactly like jobs-tui
// discovers jobs by checking for a job's dedicated unit pair.
func IsScheduled(name string) bool {
	_, err := os.Stat(timerPath(name))
	return err == nil
}

// taglueBinPath resolves the installed taglue binary's path for the
// systemd unit's ExecStart — preferring whatever's on $PATH (the normal
// install location), falling back to the currently running binary.
func taglueBinPath() (string, error) {
	if p, err := exec.LookPath("taglue"); err == nil {
		return p, nil
	}
	return os.Executable()
}

// validateCalendar checks an OnCalendar expression's syntax before it's
// written to a unit file, surfacing systemd-analyze's own error message.
func validateCalendar(expr string) error {
	out, err := exec.Command("systemd-analyze", "calendar", expr).CombinedOutput()
	if err != nil {
		return fmt.Errorf("expressão de agendamento inválida %q: %s", expr, string(out))
	}
	return nil
}

// EnableWorkflow validates the workflow's schedule, writes its service+timer
// units, and enables the timer — the single entry point used by both the CLI
// (`taglue enable`) and the TUI's toggle-schedule key, per the task's
// "ativar já cria todo o processo automaticamente" requirement.
// It also creates ~/jobs/<name>/ with a wrapper script (tajobs layout)
// and updates the metadata sidecar.
func EnableWorkflow(name string, wf *Workflow) error {
	// Determine the OnCalendar expression
	var expr string
	sched := scheduleToSchedule(wf.Schedule)
	if wf.Schedule.OnCalendar != "" {
		// Raw expression from TOML
		expr = wf.Schedule.OnCalendar
	} else if sched.Kind != schedule.KindManual {
		// Compute from structured fields
		expr = sched.OnCalendar(time.Now())
	}
	if expr == "" {
		return fmt.Errorf("workflow %q não tem schedule.on_calendar definido", name)
	}
	if err := validateCalendar(expr); err != nil {
		return err
	}

	bin, err := taglueBinPath()
	if err != nil {
		return fmt.Errorf("não foi possível localizar o binário taglue: %w", err)
	}

	// Create the tajobs-compatible job directory
	jobDir := jobsDir(name)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return fmt.Errorf("erro criando job dir: %w", err)
	}

	if err := os.MkdirAll(systemdUserDir(), 0o755); err != nil {
		return fmt.Errorf("erro criando dir de units systemd: %w", err)
	}

	// Remove legacy taglue-<name> units if they exist from the old layout
	removeLegacyUnits(name)

	// Write wrapper script for all kinds (tajobs expects ~/jobs/<name>/<name>.sh)
	var script string
	switch sched.Kind {
	case schedule.KindOneshot:
		script = fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\n\nJOB_NAME=%q\n\n%s run %s\n%s",
			name, bin, name, schedule.OneshotCleanupTail(timerPath(name), servicePath(name)))
	case schedule.KindCycle:
		cycleFields := make([]string, len(sched.Cycle))
		for i, d := range sched.Cycle {
			cycleFields[i] = strconv.Itoa(d)
		}
		if err := os.WriteFile(filepath.Join(jobDir, name+".recur"), []byte(strings.Join(cycleFields, " ")+"\n0\n"), 0o644); err != nil {
			return fmt.Errorf("erro escrevendo recur file: %w", err)
		}
		script = fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\n\nJOB_NAME=%q\n\n%s run %s\n%s",
			name, bin, name, schedule.CycleRescheduleTail(recurPath(name), timerPath(name), name+".timer"))
	default:
		// daily/weekly/monthly: simple wrapper
		script = fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\n\nJOB_NAME=%q\n\n%s run %s\n", name, bin, name)
	}
	if err := os.WriteFile(scriptPath(name), []byte(script), 0o755); err != nil {
		return fmt.Errorf("erro escrevendo wrapper script: %w", err)
	}

	execStart := scriptPath(name)

	service := fmt.Sprintf(`[Unit]
Description=taglue %s (workflow agendado)

[Service]
Type=oneshot
TimeoutStartSec=infinity
ExecStart=%s
StandardOutput=append:%s
StandardError=append:%s
`, name, execStart, logPath(name), logPath(name))

	timer := fmt.Sprintf(`[Unit]
Description=taglue %s (schedule)

[Timer]
OnCalendar=%s
Persistent=yes

[Install]
WantedBy=timers.target
`, name, expr)

	if err := os.WriteFile(servicePath(name), []byte(service), 0o644); err != nil {
		return fmt.Errorf("erro escrevendo service unit: %w", err)
	}
	if err := os.WriteFile(timerPath(name), []byte(timer), 0o644); err != nil {
		return fmt.Errorf("erro escrevendo timer unit: %w", err)
	}

	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("erro em daemon-reload: %s", string(out))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", name+".timer").CombinedOutput(); err != nil {
		return fmt.Errorf("erro habilitando timer: %s", string(out))
	}

	// Auto-update metadata (installed_at on first enable, updated_at always)
	touchMetadata(name)

	return nil
}

// removeLegacyUnits removes old taglue-<name> units from the pre-tajobs layout.
func removeLegacyUnits(name string) {
	prefix := "taglue-" + name
	timer := filepath.Join(systemdUserDir(), prefix+".timer")
	service := filepath.Join(systemdUserDir(), prefix+".service")

	_ = exec.Command("systemctl", "--user", "disable", "--now", prefix+".timer").Run()
	os.Remove(timer)
	os.Remove(service)
}

// DisableWorkflow stops and removes a workflow's systemd units, plus any
// wrapper scripts or recur files. The ~/jobs/<name>/ dir and log are kept
// for tajobs history (shows as "done" after disable).
func DisableWorkflow(name string) error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", name+".timer").Run()

	if err := os.Remove(timerPath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("erro removendo timer unit: %w", err)
	}
	if err := os.Remove(servicePath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("erro removendo service unit: %w", err)
	}
	// Remove wrapper script and recur file if they exist
	os.Remove(scriptPath(name))
	os.Remove(filepath.Join(jobsDir(name), name+".recur"))

	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("erro em daemon-reload: %s", string(out))
	}
	return nil
}

// ToggleSchedule flips a workflow's schedule on or off, deriving the current
// state from IsScheduled — the single call the TUI's `e` key needs.
func ToggleSchedule(name string, wf *Workflow) (nowEnabled bool, err error) {
	if IsScheduled(name) {
		if err := DisableWorkflow(name); err != nil {
			return true, err
		}
		return false, nil
	}
	if err := EnableWorkflow(name, wf); err != nil {
		return false, err
	}
	return true, nil
}

// Sidecar file: ~/.config/taglue/schedules.toml
// Stores structured schedules created via the TUI modal, keeping the
// user's workflow TOML untouched (preserves comments).

func sidecarPath() string {
	return filepath.Join(tuiui.ConfigDir(), "taglue", "schedules.toml")
}

// loadSchedules reads the sidecar file into a map keyed by workflow filename.
func loadSchedules() (map[string]Schedule, error) {
	path := sidecarPath()
	m := make(map[string]Schedule)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return m, nil
	}
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil, fmt.Errorf("erro lendo sidecar de schedules: %w", err)
	}
	return m, nil
}

// saveSchedule writes a single workflow's schedule to the sidecar, preserving
// other entries. It reads the existing file, updates the entry, and rewrites.
// Also touches metadata to keep updated_at current.
func saveSchedule(name string, s Schedule) error {
	path := sidecarPath()
	m := make(map[string]Schedule)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		if _, err := toml.DecodeFile(path, &m); err != nil {
			return fmt.Errorf("erro lendo sidecar: %w", err)
		}
	}
	m[name] = s

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("erro criando dir do sidecar: %w", err)
	}
	if err := writeSidecar(path, m); err != nil {
		return err
	}

	// Auto-update metadata timestamps on schedule change
	touchMetadata(name)

	return nil
}

// removeSchedule deletes a workflow's entry from the sidecar.
func removeSchedule(name string) error {
	path := sidecarPath()
	m := make(map[string]Schedule)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		if _, err := toml.DecodeFile(path, &m); err != nil {
			return nil // best effort
		}
	}
	delete(m, name)
	return writeSidecar(path, m)
}

// Metadata sidecar: ~/.config/taglue/metadata.toml
// Stores installed_at/updated_at per workflow, auto-maintained on schedule
// changes. Sidecar overrides TOML [metadata] for these fields.

func metadataSidecarPath() string {
	return filepath.Join(tuiui.ConfigDir(), "taglue", "metadata.toml")
}

// loadMetadata reads the metadata sidecar into a map keyed by workflow filename.
func loadMetadata() (map[string]Metadata, error) {
	path := metadataSidecarPath()
	m := make(map[string]Metadata)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return m, nil
	}
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil, fmt.Errorf("erro lendo sidecar de metadata: %w", err)
	}
	return m, nil
}

// saveMetadata writes a single workflow's metadata to the sidecar, preserving
// other entries. It reads the existing file, updates the entry, and rewrites.
func saveMetadata(name string, md Metadata) error {
	path := metadataSidecarPath()
	m := make(map[string]Metadata)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		if _, err := toml.DecodeFile(path, &m); err != nil {
			return fmt.Errorf("erro lendo sidecar de metadata: %w", err)
		}
	}
	m[name] = md

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("erro criando dir do sidecar: %w", err)
	}

	var b strings.Builder
	for key, md := range m {
		fmt.Fprintf(&b, "[%s]\n", key)
		if md.Creator != "" {
			fmt.Fprintf(&b, "creator = %q\n", md.Creator)
		}
		if md.InstalledAt != "" {
			fmt.Fprintf(&b, "installed_at = %q\n", md.InstalledAt)
		}
		if md.UpdatedAt != "" {
			fmt.Fprintf(&b, "updated_at = %q\n", md.UpdatedAt)
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// touchMetadata sets installed_at (if empty) and updated_at = now for a
// workflow. Called on schedule changes and enable to keep metadata current.
func touchMetadata(name string) {
	meta, err := loadMetadata()
	if err != nil {
		return
	}
	m := meta[name]
	now := time.Now().Format(time.RFC3339)
	if m.InstalledAt == "" {
		m.InstalledAt = now
	}
	m.UpdatedAt = now
	_ = saveMetadata(name, m)
}

func writeSidecar(path string, m map[string]Schedule) error {
	var b strings.Builder
	for name, s := range m {
		fmt.Fprintf(&b, "[%s]\n", name)
		if s.OnCalendar != "" {
			fmt.Fprintf(&b, "on_calendar = %q\n", s.OnCalendar)
		}
		if s.Kind != "" {
			fmt.Fprintf(&b, "kind = %q\n", s.Kind)
		}
		if s.Hour > 0 || s.Minute > 0 {
			fmt.Fprintf(&b, "hour = %d\nminute = %d\n", s.Hour, s.Minute)
		}
		if len(s.Weekdays) > 0 {
			fmt.Fprintf(&b, "weekdays = [")
			for i, w := range s.Weekdays {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%q", w)
			}
			b.WriteString("]\n")
		}
		if s.DayOfMonth > 0 {
			fmt.Fprintf(&b, "day_of_month = %d\n", s.DayOfMonth)
		}
		if s.DOM > 0 {
			fmt.Fprintf(&b, "dom = %d\n", s.DOM)
		}
		if s.Month > 0 {
			fmt.Fprintf(&b, "month = %d\n", s.Month)
		}
		if len(s.Cycle) > 0 {
			fmt.Fprintf(&b, "cycle = [")
			for i, c := range s.Cycle {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%d", c)
			}
			b.WriteString("]\n")
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// scheduleToSchedule converts glue's Schedule to the shared schedule.Schedule type.
func scheduleToSchedule(s Schedule) schedule.Schedule {
	kind := schedule.KindDaily // default
	switch s.Kind {
	case "oneshot":
		kind = schedule.KindOneshot
	case "daily":
		kind = schedule.KindDaily
	case "weekly":
		kind = schedule.KindWeekly
	case "monthly":
		kind = schedule.KindMonthly
	case "cycle":
		kind = schedule.KindCycle
	case "manual":
		kind = schedule.KindManual
	}

	var weekdays []time.Weekday
	for _, w := range s.Weekdays {
		switch w {
		case "Mon":
			weekdays = append(weekdays, time.Monday)
		case "Tue":
			weekdays = append(weekdays, time.Tuesday)
		case "Wed":
			weekdays = append(weekdays, time.Wednesday)
		case "Thu":
			weekdays = append(weekdays, time.Thursday)
		case "Fri":
			weekdays = append(weekdays, time.Friday)
		case "Sat":
			weekdays = append(weekdays, time.Saturday)
		case "Sun":
			weekdays = append(weekdays, time.Sunday)
		}
	}

	return schedule.Schedule{
		Kind:       kind,
		Hour:       s.Hour,
		Minute:     s.Minute,
		DOM:        s.DOM,
		Month:      s.Month,
		Weekdays:   weekdays,
		DayOfMonth: s.DayOfMonth,
		Cycle:      s.Cycle,
	}
}

// scheduleString returns a human-readable summary for the metadata panel.
// When Kind is set, always prefer the structured representation (e.g.
// "diário 21:00") over the raw OnCalendar expression for consistent
// display across workflows.
func scheduleString(s Schedule) string {
	if s.Kind != "" {
		return scheduleToSchedule(s).String()
	}
	if s.OnCalendar != "" {
		return s.OnCalendar
	}
	return scheduleToSchedule(s).String()
}

// preFillSchedule builds a schedule.Schedule from a workflow's current config
// so the huh form opens with the existing values already selected. If the
// workflow only has a raw on_calendar (no structured kind), it defaults to
// KindManual since raw cron can't be mapped to the form's fields.
func preFillSchedule(wf *Workflow) schedule.Schedule {
	if wf == nil {
		return schedule.Schedule{}
	}
	s := wf.Schedule
	// If there's only a raw on_calendar and no structured kind, default
	// to manual — the form can't represent raw cron expressions.
	if s.Kind == "" && s.OnCalendar != "" {
		return schedule.Schedule{Kind: schedule.KindManual}
	}
	return scheduleToSchedule(s)
}

// scriptPath is the wrapper script in the tajobs job directory.
func scriptPath(name string) string {
	return filepath.Join(jobsDir(name), name+".sh")
}

// recurPath is the cycle state file in the tajobs job directory.
func recurPath(name string) string {
	return filepath.Join(jobsDir(name), name+".recur")
}
