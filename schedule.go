package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/TAbelhaDev/tabelhatuiui"
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
	expr := wf.Schedule.OnCalendar
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

	service := fmt.Sprintf(`[Unit]
Description=taglue %s (workflow agendado)

[Service]
Type=oneshot
TimeoutStartSec=infinity
ExecStart=%s run %s
StandardOutput=append:%s
StandardError=append:%s
`, name, bin, name, log, log)

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

// DisableWorkflow stops and removes a workflow's systemd units.
func DisableWorkflow(name string) error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", unitName(name)+".timer").Run()

	if err := os.Remove(timerPath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("erro removendo timer unit: %w", err)
	}
	if err := os.Remove(servicePath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("erro removendo service unit: %w", err)
	}

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
