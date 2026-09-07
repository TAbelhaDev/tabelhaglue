package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/TAbelhaDev/tabelhatuiui"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var theme = tuiui.NewThemeFromEnv("TAGLUE")

const (
	headerLines = 1
	gapLines    = 1
	footerLines = 1
	statusLines = 1
)

// Five fixed keys don't earn tuiui.KeyRegistry's rebind-from-disk machinery
// (that pays off once a TUI grows a couple dozen actions, see tabelhavagas) —
// plain key.Binding vars are enough here and still drop straight into
// tuiui.NewFooter, since tuiui.Binding is just an alias for key.Binding.
var (
	keyUp   = key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "cima"))
	keyDown = key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "baixo"))
	keyRun  = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "rodar"))
	keyBack = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "voltar"))
	keyQuit = key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "sair"))
)

type tuiMode int

const (
	modeList tuiMode = iota
	modeRun
)

type tuiModel struct {
	mode          tuiMode
	entries       []workflowEntry
	listErr       error
	cursor        int
	width, height int

	// run state
	wf        *Workflow
	stepIdx   int
	stepTotal int
	stepLabel string
	lines     []string
	vp        viewport.Model
	runCh     chan tea.Msg
	runCancel context.CancelFunc
	gen       int
	done      bool
	runErr    error
}

func newTUIModel() tuiModel {
	entries, err := listWorkflowEntries()
	return tuiModel{entries: entries, listErr: err}
}

func (m tuiModel) Init() tea.Cmd {
	return nil
}

// Step-run progress messages, sent by runWorkflow's hooks (via sendMsg) into
// runCh and read back one at a time by waitForRunMsg. gen ties each message
// to the startRun call that produced it, mirroring tabelhakanban's
// radarGen/radarCancel guard: a message from a run the user already
// abandoned (esc) is simply dropped instead of updating a stale view.
type stepStartMsg struct {
	gen, step, total int
	tool, method     string
}

type stepLineMsg struct {
	gen, step int
	line      string
}

type stepDoneMsg struct {
	gen, step, bytes int
}

type runFinishedMsg struct {
	gen    int
	output []byte
	err    error
}

// sendMsg delivers msg to ch, unless ctx is already cancelled — the
// non-blocking escape hatch so a hook never wedges the background goroutine
// after the TUI has stopped reading (esc cancels ctx first).
func sendMsg(ctx context.Context, ch chan<- tea.Msg, msg tea.Msg) {
	select {
	case ch <- msg:
	case <-ctx.Done():
	}
}

// waitForRunMsg reads one message off ch and hands it back to Update as a
// tea.Msg. Every handler that wants more messages re-issues this command.
func waitForRunMsg(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// startRun loads the selected workflow and kicks it off in the background,
// switching the model into modeRun.
func (m tuiModel) startRun(entry workflowEntry) (tea.Model, tea.Cmd) {
	wf, err := loadWorkflow(entry.Path)
	if err != nil {
		m.listErr = err
		return m, nil
	}

	m.gen++
	gen := m.gen
	ctx, cancel := context.WithCancel(context.Background())

	m.mode = modeRun
	m.wf = wf
	m.runCancel = cancel
	m.stepIdx = 0
	m.stepTotal = len(wf.Steps)
	m.stepLabel = ""
	m.lines = nil
	m.done = false
	m.runErr = nil
	m.vp = viewport.New(0, 0)
	m.resizeViewport()

	ch := make(chan tea.Msg, 256)
	m.runCh = ch

	hooks := runHooks{
		StepStart: func(i, n int, step Step) {
			sendMsg(ctx, ch, stepStartMsg{gen: gen, step: i, total: n, tool: step.Tool, method: step.Method})
		},
		StepLine: func(i int, line string) {
			sendMsg(ctx, ch, stepLineMsg{gen: gen, step: i, line: line})
		},
		StepDone: func(i int, out []byte) {
			sendMsg(ctx, ch, stepDoneMsg{gen: gen, step: i, bytes: len(out)})
		},
	}

	go func() {
		out, err := runWorkflow(ctx, wf, hooks)
		sendMsg(ctx, ch, runFinishedMsg{gen: gen, output: out, err: err})
		close(ch)
	}()

	return m, waitForRunMsg(ch)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewport()
		return m, nil

	case stepStartMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.stepIdx, m.stepTotal = msg.step, msg.total
		m.stepLabel = msg.tool + " " + msg.method
		return m, waitForRunMsg(m.runCh)

	case stepLineMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.appendLine(msg.line)
		return m, waitForRunMsg(m.runCh)

	case stepDoneMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.appendLine(fmt.Sprintf("  ok (%d bytes)", msg.bytes))
		return m, waitForRunMsg(m.runCh)

	case runFinishedMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.done = true
		m.runErr = msg.err
		switch {
		case msg.err != nil:
			m.appendLine("")
			m.appendLine("erro: " + msg.err.Error())
		case msg.output != nil:
			var pretty bytes.Buffer
			if json.Indent(&pretty, msg.output, "", "  ") == nil {
				m.appendLine("")
				m.appendLine(pretty.String())
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.mode == modeRun {
			return m.updateRunKey(msg)
		}
		return m.updateListKey(msg)
	}
	return m, nil
}

func (m *tuiModel) appendLine(line string) {
	m.lines = append(m.lines, line)
	m.vp.SetContent(strings.Join(m.lines, "\n"))
	m.vp.GotoBottom()
}

func (m tuiModel) updateListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.entries)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		if len(m.entries) > 0 {
			return m.startRun(m.entries[m.cursor])
		}
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m tuiModel) updateRunKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.runCancel != nil {
			m.runCancel()
		}
		m.gen++
		m.mode = modeList
		return m, nil
	case "q", "ctrl+c":
		if m.runCancel != nil {
			m.runCancel()
		}
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// resizeViewport keeps the run view's viewport sized to whatever's currently
// available, whether that's from a fresh WindowSizeMsg or a run just
// starting.
func (m *tuiModel) resizeViewport() {
	innerW := m.width - 4
	innerH := m.height - headerLines - gapLines - statusLines - footerLines - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	m.vp.Width = innerW
	m.vp.Height = innerH
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "carregando..."
	}
	if m.mode == modeRun {
		return m.viewRun()
	}
	return m.viewList()
}

// renderPanel wraps content in a bordered panel sized to w×availH. See
// tuiui.Theme.Panel's doc comment on why content is pre-padded with
// PadLines/PadToHeight instead of relying on lipgloss's own Width() wrapping.
func (m tuiModel) renderPanel(content string, availH, w int) string {
	innerW := w - 4
	innerH := availH - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	content = tuiui.PadLines(content, innerW)
	content = tuiui.PadToHeight(content, innerH)
	return theme.Panel(true).Render(content)
}

func (m tuiModel) viewList() string {
	w := m.width
	header := theme.Header(w).Render("taglue · workflows")

	availH := m.height - headerLines - gapLines - footerLines
	if availH < 1 {
		availH = 1
	}

	var body string
	switch {
	case m.listErr != nil:
		body = m.renderPanel(theme.Error().Render("erro: "+m.listErr.Error()), availH, w)
	case len(m.entries) == 0:
		body = m.renderPanel(theme.Muted().Render("nenhum workflow em "+workflowsDir()), availH, w)
	default:
		lines := make([]string, 0, len(m.entries))
		for i, e := range m.entries {
			var line string
			if i == m.cursor {
				line = theme.Title().Render("▸ " + e.Name)
			} else {
				line = "  " + e.Name
			}
			if e.Description != "" {
				line += "  " + theme.Dim().Render(e.Description)
			}
			lines = append(lines, line)
		}
		body = m.renderPanel(strings.Join(lines, "\n"), availH, w)
	}

	footer := tuiui.NewFooter(keyUp, keyDown, keyRun, keyQuit).
		Status(fmt.Sprintf("%d workflows", len(m.entries))).
		Render(w, theme)

	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, footer)
}

func (m tuiModel) viewRun() string {
	w := m.width
	name := ""
	if m.wf != nil {
		name = m.wf.Name
	}
	header := theme.Header(w).Render("taglue · " + name)

	var status string
	switch {
	case m.done && m.runErr != nil:
		status = theme.Error().Render("erro: " + m.runErr.Error())
	case m.done:
		status = theme.Success().Render("ok")
	default:
		status = theme.Info().Render(fmt.Sprintf("step %d/%d: %s...", m.stepIdx, m.stepTotal, m.stepLabel))
	}

	availH := m.height - headerLines - gapLines - statusLines - footerLines
	if availH < 1 {
		availH = 1
	}
	body := m.renderPanel(m.vp.View(), availH, w)

	runStatus := "rodando"
	if m.done {
		runStatus = "concluído"
	}
	footer := tuiui.NewFooter(keyBack, keyQuit).
		Status(runStatus).
		Render(w, theme)

	return lipgloss.JoinVertical(lipgloss.Left, header, status, body, footer)
}

// runTUI opens the interactive Bubble Tea UI. Called when taglue is invoked
// with no arguments; taglue run/list stay headless and never touch this.
func runTUI() int {
	p := tea.NewProgram(newTUIModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}
