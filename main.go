package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
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
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "uso: taglue <comando>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "comandos:")
	fmt.Fprintln(os.Stderr, "  run <workflow>   executa um workflow")
	fmt.Fprintln(os.Stderr, "  list             lista workflows disponíveis")
}
