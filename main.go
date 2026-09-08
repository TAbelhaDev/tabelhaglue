package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(runTUI())
	}

	switch os.Args[1] {
	case "run":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "uso: taglue run <workflow>")
			os.Exit(1)
		}
		name := os.Args[2]
		if filepath.Ext(name) == "" {
			name += ".toml"
		}
		path := filepath.Join(workflowsDir(), name)
		w, err := loadWorkflow(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "erro lendo workflow %s: %v\n", path, err)
			os.Exit(1)
		}
		if err := engine(w); err != nil {
			fmt.Fprintf(os.Stderr, "erro: %v\n", err)
			os.Exit(1)
		}
	case "list":
		names, err := listWorkflows()
		if err != nil {
			fmt.Fprintf(os.Stderr, "erro listando workflows: %v\n", err)
			os.Exit(1)
		}
		for _, n := range names {
			fmt.Println(n)
		}
	case "enable":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "uso: taglue enable <workflow>")
			os.Exit(1)
		}
		name, wf := loadWorkflowByName(os.Args[2])
		if wf == nil {
			os.Exit(1)
		}
		if err := EnableWorkflow(name, wf); err != nil {
			fmt.Fprintf(os.Stderr, "erro: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("agendamento de %q ativado\n", name)
	case "disable":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "uso: taglue disable <workflow>")
			os.Exit(1)
		}
		name := strings.TrimSuffix(os.Args[2], ".toml")
		if err := DisableWorkflow(name); err != nil {
			fmt.Fprintf(os.Stderr, "erro: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("agendamento de %q desativado\n", name)
	default:
		printUsage()
		os.Exit(1)
	}
}

// loadWorkflowByName resolves a workflow name (with or without .toml) to its
// file stem and parsed contents, printing an error and returning a nil
// Workflow on failure. Shared by the enable command and (later) the TUI.
func loadWorkflowByName(arg string) (name string, wf *Workflow) {
	filename := arg
	if filepath.Ext(filename) == "" {
		filename += ".toml"
	}
	path := filepath.Join(workflowsDir(), filename)
	w, err := loadWorkflow(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "erro lendo workflow %s: %v\n", path, err)
		return "", nil
	}
	return strings.TrimSuffix(filename, ".toml"), w
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "uso: taglue [<comando>]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "sem comando abre a TUI")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "comandos:")
	fmt.Fprintln(os.Stderr, "  run <workflow>      executa um workflow")
	fmt.Fprintln(os.Stderr, "  list                lista workflows disponíveis")
	fmt.Fprintln(os.Stderr, "  enable <workflow>   ativa o agendamento (cria units systemd)")
	fmt.Fprintln(os.Stderr, "  disable <workflow>  desativa o agendamento (remove units systemd)")
}
