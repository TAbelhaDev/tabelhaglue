# TAbelhaGlue

Workflow engine for the TAbelha ecosystem. Runs declarative pipelines of IPC calls between tools.

## Install

```bash
go install github.com/TAbelhaDev/tabelhaglue/cmd/taglue@latest
```

## Usage

```bash
# Open the TUI (3-panel: workflows / metadata / description)
taglue

# List available workflows
taglue list

# Run a workflow
taglue run project-status

# Enable a workflow's schedule (creates systemd user timer)
taglue enable project-selfdoc

# Disable a workflow's schedule
taglue disable project-selfdoc
```

## TUI

Running `taglue` with no arguments opens the interactive Bubble Tea TUI.

**Keys:**
| Key | Action |
|-----|--------|
| `j`/`k` or arrows | Navigate workflows |
| `r` or `Enter` | Run selected workflow |
| `e` | Toggle schedule (enable/disable) |
| `ctrl+h`/`ctrl+l` | Move focus between list and description panels |
| `?` | Help (all keybindings) |
| `q` | Quit |

The TUI has three panels:
- **Workflows** (left): list of available workflows, grouped by project group
- **Metadata** (top-right): creator, install date, schedule status, tools used
- **Description** (bottom-right): workflow description or markdown file

Keybindings are customizable via `~/.config/taglue/keybindings.json`.

## Workflow files

Workflows live in `~/.config/taglue/workflows/` as TOML files:

```toml
name = "project-selfdoc"
description = "Gera relatório de estado dos projetos"
description_file = "project-selfdoc.md"  # optional markdown file
group = "tabeladev"                      # optional: group for TUI sections

[metadata]
creator = "Ian Soares"
installed_at = "2026-09-08T17:38:52-03:00"

[schedule]
on_calendar = "*-*-* 21:00:00"  # systemd OnCalendar expression

[[steps]]
tool = "taradar"
method = "projects.list"
args = { group = "tabeladev" }

[[steps]]
tool = "taselfdoc"
method = "digest"
args = { group = "tabeladev" }
```

Each step is a `<tool> ipc <method> --json [key=value...]` call. Steps run in sequence. The last step's JSON output is printed to stdout.

## Step options

### Timeout

By default each step has a 30-second timeout. Override per step with `timeout_seconds`:

```toml
[[steps]]
tool = "taselfdoc"
method = "digest"
args = { group = "tabeladev" }
timeout_seconds = 120
```

### Interpolation

Steps can reference previous step outputs using `${steps.N.output.field}`:

```toml
[[steps]]
tool = "taradar"
method = "projects.list"
args = {}

[[steps]]
tool = "taselfdoc"
method = "project.summary"
args = { name = "${steps.0.output.0.name}" }
```

**Note:** If the referenced field doesn't exist as a map key (e.g. the output is a JSON array), `extractField` falls back to returning the raw JSON string. This is intentional and used by workflows like `post-suggestions` to pass entire arrays between steps.

## Scheduling

`taglue enable <workflow>` creates systemd user timer units following the tajobs convention (`~/.config/systemd/user/<name>.{timer,service}`). A wrapper script is written to `~/jobs/<name>/<name>.sh` and logs to `~/jobs/<name>/<name>.log`, so scheduled workflows also appear in [jobs-tui](https://github.com/TAbelhaDev/tajobs).

The schedule uses `Persistent=yes`, so a missed run fires when the machine wakes up. Timer state is derived from the `.timer` file existence (same convention as [tabelhajobs](https://github.com/TAbelhaDev/tabelhajobs)).

## IPC convention

All tools must implement `<tool> ipc <method> --json [key=value...]` and print JSON to stdout.

## Local development

A `post-commit` hook in `.githooks/` rebuilds and reinstalls `taglue` to
`~/.local/bin/taglue` after every commit, so the local command never goes
stale. Git doesn't enable a repo's `.githooks/` automatically on clone — run
this once per clone:

```bash
git config core.hooksPath .githooks
```
