package main

import (
	"bufio"
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

// runHooks lets a caller observe a workflow run as it happens (one step at a
// time, one stderr line at a time) instead of only getting the final result.
// engine's headless path and the TUI's run view both drive runWorkflow
// through this; a nil field just means "don't care about that event".
type runHooks struct {
	StepStart func(i, n int, step Step)
	StepLine  func(i int, line string)
	StepDone  func(i int, out []byte)
	StepError func(i int, err error)
}

// runWorkflow runs a workflow's steps in sequence, reporting progress through
// h, and returns the last step's raw output. ctx bounds the whole run; each
// step additionally gets its own 30s timeout derived from ctx, so cancelling
// ctx (e.g. the TUI's esc) kills whichever step is currently in flight.
func runWorkflow(ctx context.Context, w *Workflow, h runHooks) ([]byte, error) {
	var outputs []stepOutput
	var last []byte

	for i, step := range w.Steps {
		// Interpolate args from previous step outputs.
		args := interpolateArgs(step.Args, outputs)

		// Build the IPC command.
		cmdArgs := []string{"ipc", step.Method, "--json"}
		for k, v := range args {
			cmdArgs = append(cmdArgs, k+"="+v)
		}

		if h.StepStart != nil {
			h.StepStart(i+1, len(w.Steps), step)
		}

		stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)

		cmd := exec.CommandContext(stepCtx, step.Tool, cmdArgs...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout

		stderr, err := cmd.StderrPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			cancel()
			if h.StepError != nil {
				h.StepError(i+1, err)
			}
			return last, fmt.Errorf("step %d (%s %s): %w", i+1, step.Tool, step.Method, err)
		}

		// Drain stderr line-by-line before Wait — Wait requires every read
		// from a pipe started via StderrPipe to have completed first.
		linesDone := make(chan struct{})
		go func() {
			defer close(linesDone)
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				if h.StepLine != nil {
					h.StepLine(i+1, scanner.Text())
				}
			}
		}()
		<-linesDone

		err = cmd.Wait()
		cancel()
		if err != nil {
			if h.StepError != nil {
				h.StepError(i+1, err)
			}
			return last, fmt.Errorf("step %d (%s %s): %w", i+1, step.Tool, step.Method, err)
		}

		out := stdout.Bytes()
		outputs = append(outputs, stepOutput{Raw: json.RawMessage(out)})
		last = out
		if h.StepDone != nil {
			h.StepDone(i+1, out)
		}
	}

	return last, nil
}

// engine runs a workflow headlessly: progress goes to stderr, the last
// step's pretty-printed JSON output goes to stdout — unchanged from before
// runWorkflow existed. Tool stderr (taradar's "aviso: ..." lines etc.) is
// still discarded on success, matching the old cmd.Output()-based behavior.
func engine(w *Workflow) error {
	last, err := runWorkflow(context.Background(), w, runHooks{
		StepStart: func(i, n int, step Step) {
			fmt.Fprintf(os.Stderr, "[%d/%d] %s %s\n", i, n, step.Tool, step.Method)
		},
		StepDone: func(i int, out []byte) {
			fmt.Fprintf(os.Stderr, "  ok (%d bytes)\n", len(out))
		},
	})
	if err != nil {
		return err
	}

	if last != nil {
		var pretty bytes.Buffer
		json.Indent(&pretty, last, "", "  ")
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
