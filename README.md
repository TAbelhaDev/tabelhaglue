# TAbelhaGlue

Workflow engine for the TAbelha ecosystem. Runs declarative pipelines of IPC calls between tools.

## Install

```bash
go install github.com/TAbelhaDev/tabelhaglue@latest
```

## Usage

```bash
# List available workflows
taglue list

# Run a workflow
taglue run project-status
```

## Workflow files

Workflows live in `~/.config/taglue/workflows/` as TOML files:

```toml
name = "project-status"
description = "Lista projetos e gera resumo"

[[steps]]
tool = "taradar"
method = "projects.list"
args = {}

[[steps]]
tool = "taselfdoc"
method = "projects.state"
args = {}
```

Each step is a `<tool> ipc <method> --json [key=value...]` call. Steps run in sequence. The last step's JSON output is printed to stdout.

## Step interpolation

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

## IPC convention

All tools must implement `<tool> ipc <method> --json [key=value...]` and print JSON to stdout.
