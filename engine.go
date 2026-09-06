package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// stepOutput holds the result of one step's IPC call.
type stepOutput struct {
	Raw    json.RawMessage
	Parsed any
}

// engine runs a workflow's steps in sequence.
func engine(w *Workflow) error {
	var outputs []stepOutput

	for i, step := range w.Steps {
		// Interpolate args from previous step outputs.
		args := interpolateArgs(step.Args, outputs)

		// Build the IPC command.
		cmdArgs := []string{"ipc", step.Method, "--json"}
		for k, v := range args {
			cmdArgs = append(cmdArgs, k+"="+v)
		}

		fmt.Fprintf(os.Stderr, "[%d/%d] %s %s\n", i+1, len(w.Steps), step.Tool, step.Method)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, step.Tool, cmdArgs...)
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("step %d (%s %s): %w", i+1, step.Tool, step.Method, err)
		}

		outputs = append(outputs, stepOutput{Raw: json.RawMessage(out)})
		fmt.Fprintf(os.Stderr, "  ok (%d bytes)\n", len(out))
	}

	// Print the last step's output.
	if len(outputs) > 0 {
		var pretty bytes.Buffer
		json.Indent(&pretty, outputs[len(outputs)-1].Raw, "", "  ")
		fmt.Println(pretty.String())
	}

	return nil
}

// interpolateArgs replaces ${steps.N.output.field} references in args.
func interpolateArgs(args map[string]string, outputs []stepOutput) map[string]string {
	if len(args) == 0 {
		return nil
	}
	result := make(map[string]string, len(args))
	for k, v := range args {
		result[k] = interpolate(v, outputs)
	}
	return result
}

var stepRefRe = regexp.MustCompile(`\$\{steps\.(\d+)\.output\.([^}]+)\}`)

func interpolate(s string, outputs []stepOutput) string {
	return stepRefRe.ReplaceAllStringFunc(s, func(match string) string {
		parts := stepRefRe.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		idx, err := strconv.Atoi(parts[1])
		if err != nil || idx >= len(outputs) {
			return match
		}
		field := parts[2]
		return extractField(outputs[idx].Raw, field)
	})
}

func extractField(data json.RawMessage, field string) string {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return string(data)
	}
	// Support nested fields with dot notation.
	parts := strings.Split(field, ".")
	current := any(m)
	for _, p := range parts {
		switch v := current.(type) {
		case map[string]any:
			current = v[p]
		default:
			return fmt.Sprintf("%v", current)
		}
	}
	return fmt.Sprintf("%v", current)
}
