# Changelog

## v0.1.0 (2026-09-11)

### Features
- **TUI**: 3-panel Bubble Tea interface (workflows/metadata/description) with key registry
- **Scheduling**: `enable`/`disable` commands + `e` toggle in TUI, systemd user timers
- **Groups**: optional `group` field in workflow TOML, TUI shows workflows in group sections
- **Timeout**: per-step `timeout_seconds` override (default 30s)

### Fixes
- `taglue list` no longer shows `.toml` extension

### Testing
- 28 unit + integration tests covering TOML parsing, interpolation, engine, scheduling, and group sorting

### Docs
- README with TUI keys, scheduling, timeout, groups, and interpolation docs
