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
func unitName(name string) string {
	return "taglue-" + name
}

func timerPath(name string) string {
	return filepath.Join(systemdUserDir(), unitName(name)+".timer")
}

func servicePath(name string) string {
	return filepath.Join(systemdUserDir(), unitName(name)+".service")
}

// logPath is where the scheduled run's stdout/stderr is appended.
func logPath(name string) string {
	return filepath.Join(tuiui.HomeDir(), ".local", "state", "taglue", name+".log")
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

	log := logPath(name)
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		return fmt.Errorf("erro criando dir de log: %w", err)
	}
	if err := os.MkdirAll(systemdUserDir(), 0o755); err != nil {
		return fmt.Errorf("erro criando dir de units systemd: %w", err)
	}

	// Determine ExecStart based on kind
	var execStart string
	switch sched.Kind {
	case schedule.KindOneshot:
		// Generate wrapper script with cleanup tail
		script := fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\n\nJOB_NAME=%q\n\n%s run %s\n%s",
			name, bin, name, schedule.OneshotCleanupTail(timerPath(name), servicePath(name)))
		if err := os.WriteFile(scriptPath(name), []byte(script), 0o755); err != nil {
			return fmt.Errorf("erro escrevendo wrapper script: %w", err)
		}
		execStart = scriptPath(name)
	case schedule.KindCycle:
		// Generate wrapper script with cycle reschedule tail
		// Write recur file
		cycleFields := make([]string, len(sched.Cycle))
		for i, d := range sched.Cycle {
			cycleFields[i] = strconv.Itoa(d)
		}
		if err := os.WriteFile(recurPath(name), []byte(strings.Join(cycleFields, " ")+"\n0\n"), 0o644); err != nil {
			return fmt.Errorf("erro escrevendo recur file: %w", err)
		}
		script := fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\n\nJOB_NAME=%q\n\n%s run %s\n%s",
			name, bin, name, schedule.CycleRescheduleTail(recurPath(name), timerPath(name), unitName(name)+".timer"))
		if err := os.WriteFile(scriptPath(name), []byte(script), 0o755); err != nil {
			return fmt.Errorf("erro escrevendo wrapper script: %w", err)
		}
		execStart = scriptPath(name)
	default:
		// daily/weekly/monthly: direct taglue run
		execStart = fmt.Sprintf("%s run %s", bin, name)
	}

	service := fmt.Sprintf(`[Unit]
Description=taglue %s (workflow agendado)

[Service]
Type=oneshot
TimeoutStartSec=infinity
ExecStart=%s
StandardOutput=append:%s
StandardError=append:%s
`, name, execStart, log, log)

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
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", unitName(name)+".timer").CombinedOutput(); err != nil {
		return fmt.Errorf("erro habilitando timer: %s", string(out))
	}
	return nil
}

// DisableWorkflow stops and removes a workflow's systemd units, plus any
// wrapper scripts or recur files for oneshot/cycle workflows.
func DisableWorkflow(name string) error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", unitName(name)+".timer").Run()

	if err := os.Remove(timerPath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("erro removendo timer unit: %w", err)
	}
	if err := os.Remove(servicePath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("erro removendo service unit: %w", err)
	}
	// Remove wrapper script and recur file if they exist
	os.Remove(scriptPath(name))
	os.Remove(recurPath(name))

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
	return writeSidecar(path, m)
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
func scheduleString(s Schedule) string {
	if s.OnCalendar != "" {
		return s.OnCalendar
	}
	return scheduleToSchedule(s).String()
}

// scriptPath is the wrapper script for oneshot/cycle workflows.
func scriptPath(name string) string {
	return filepath.Join(tuiui.HomeDir(), ".local", "state", "taglue", name+".sh")
}

// recurPath is the cycle state file.
func recurPath(name string) string {
	return filepath.Join(tuiui.HomeDir(), ".local", "state", "taglue", name+".recur")
}
