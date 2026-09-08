package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/TAbelhaDev/tabelhatuiui"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var theme = tuiui.NewThemeFromEnv("TAGLUE")

const (
	headerLines = 1
	gapLines    = 1
	footerLines = 1
	statusLines = 1

	// 3-panel list-mode layout budget, mirroring tabelharadar/model.go's
	// layout(): fixed line/width overheads instead of letting lipgloss
	// stretch panels past what actually fits the terminal.
	panelGap        = 1
	listBoxOverhead = 2 + 1 // border + title
	metaBoxOverhead = 2 + 1
	descBoxOverhead = 2 + 1
	minListRows     = 3
	minMetaLines    = 4
	minDescLines    = 4
	minListWidth    = 20
	minRightWidth   = 40
	// metaFixedLines is the metadata panel's content budget: creator,
	// installed_at, schedule status, on_calendar, blank, tools.
	metaFixedLines = 6
)

// panelFocus selects which of the two interactive panels — the workflow
// list, or the description panel — currently receives j/k. The metadata
// panel (top-right) is display-only, never a focus target, same as
// tabelharadar's stats panel.
type panelFocus int

const (
	focusList panelFocus = iota
	focusDesc
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
	focus         panelFocus
	width, height int

	// list-mode 3-panel layout, recomputed by layout() on every resize.
	listInnerWidth  int
	rightInnerWidth int
	listRowsHeight  int
	metaLines       int
	descMaxLines    int

	// descScroll is the first visible line of the selected workflow's
	// rendered description; descCache* avoids re-running glamour on every
	// keystroke (it's only invalidated by a cursor move or a resize).
	descScroll     int
	descCachePath  string
	descCacheWidth int
	descCacheLines []string

	scheduling     bool
	scheduleStatus string

	helpModal *tuiui.HelpModal

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
	_ = reg.Load()
	entries, err := listWorkflowEntries()
	return tuiModel{
		entries: entries,
		listErr: err,
		helpModal: tuiui.NewHelpModal(tuiui.HelpSection{
			Title:      "Atalhos",
			BindingsFn: reg.Bindings,
		}),
	}
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

// scheduleToggledMsg reports the result of a background ToggleSchedule call
// (enable/disable shells out to systemd-analyze + systemctl, ~200-500ms —
// too slow to run inline without visibly stalling the UI).
type scheduleToggledMsg struct {
	name    string
	enabled bool
	err     error
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

// toggleScheduleCmd runs ToggleSchedule in the background so the UI doesn't
// block on the systemctl/systemd-analyze calls it makes.
func toggleScheduleCmd(entry workflowEntry) tea.Cmd {
	return func() tea.Msg {
		enabled, err := ToggleSchedule(entry.File, entry.WF)
		return scheduleToggledMsg{name: entry.File, enabled: enabled, err: err}
	}
}

// startRun loads the selected workflow and kicks it off in the background,
// switching the model into modeRun.
func (m tuiModel) startRun(entry workflowEntry) (tea.Model, tea.Cmd) {
	wf := entry.WF
	if wf == nil {
		loaded, err := loadWorkflow(entry.Path)
		if err != nil {
			m.listErr = err
			return m, nil
		}
		wf = loaded
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
		m.helpModal.SetSize(msg.Width, msg.Height)
		m.resizeViewport()
		m.layout()
		m.refreshDescCache()
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

	case scheduleToggledMsg:
		m.scheduling = false
		switch {
		case msg.err != nil:
			m.scheduleStatus = "erro: " + msg.err.Error()
		case msg.enabled:
			m.scheduleStatus = fmt.Sprintf("%q agendado", msg.name)
		default:
			m.scheduleStatus = fmt.Sprintf("%q agendamento removido", msg.name)
		}
		return m, nil
	}

	if m.helpModal.Update(msg) {
		return m, nil
	}

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if key.Matches(keyMsg, resolve("help")) {
			m.helpModal.Toggle()
			return m, nil
		}
		if m.mode == modeRun {
			return m.updateRunKey(keyMsg)
		}
		return m.updateListKey(keyMsg)
	}
	return m, nil
}

func (m *tuiModel) appendLine(line string) {
	m.lines = append(m.lines, line)
	m.vp.SetContent(strings.Join(m.lines, "\n"))
	m.vp.GotoBottom()
}

func (m tuiModel) current() *workflowEntry {
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return nil
	}
	return &m.entries[m.cursor]
}

func (m tuiModel) updateListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, resolve("quit")):
		return m, tea.Quit
	case key.Matches(msg, resolve("focus-list")):
		m.focus = focusList
		return m, nil
	case key.Matches(msg, resolve("focus-desc")):
		m.focus = focusDesc
		return m, nil
	case key.Matches(msg, resolve("run")):
		if entry := m.current(); entry != nil {
			return m.startRun(*entry)
		}
		return m, nil
	case key.Matches(msg, resolve("toggle-schedule")):
		if m.scheduling {
			return m, nil
		}
		if entry := m.current(); entry != nil {
			m.scheduling = true
			m.scheduleStatus = "atualizando agendamento..."
			return m, toggleScheduleCmd(*entry)
		}
		return m, nil
	}

	if m.focus == focusDesc {
		switch msg.String() {
		case "j", "down":
			if m.descScroll < m.maxDescScroll() {
				m.descScroll++
			}
		case "k", "up":
			if m.descScroll > 0 {
				m.descScroll--
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.entries)-1 {
			m.cursor++
			m.descScroll = 0
			m.refreshDescCache()
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			m.descScroll = 0
			m.refreshDescCache()
		}
	}
	return m, nil
}

func (m tuiModel) updateRunKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, resolve("back")):
		if m.runCancel != nil {
			m.runCancel()
		}
		m.gen++
		m.mode = modeList
		return m, nil
	case key.Matches(msg, resolve("quit")):
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

// layout recomputes the list/metadata/description panel widths and heights
// so the whole 3-panel view always fits exactly within m.height — mirrors
// tabelharadar/model.go's layout(): fixed line budgets per panel instead of
// letting lipgloss stretch content past what fits, which is exactly the bug
// radar hit and fixed once already.
func (m *tuiModel) layout() {
	if m.width == 0 || m.height == 0 {
		return
	}

	totalRowWidth := m.width - panelGap
	if minRow := (minListWidth + 4) + (minRightWidth + 4); totalRowWidth < minRow {
		totalRowWidth = minRow
	}
	listBoxWidth := totalRowWidth / 3
	rightBoxWidth := totalRowWidth - listBoxWidth

	m.listInnerWidth = listBoxWidth - 4
	if m.listInnerWidth < minListWidth {
		m.listInnerWidth = minListWidth
	}
	m.rightInnerWidth = rightBoxWidth - 4
	if m.rightInnerWidth < minRightWidth {
		m.rightInnerWidth = minRightWidth
	}

	bodyHeight := m.height - headerLines - footerLines
	minBody := metaBoxOverhead + minMetaLines + descBoxOverhead + minDescLines
	if minListBody := listBoxOverhead + minListRows; minListBody > minBody {
		minBody = minListBody
	}
	if bodyHeight < minBody {
		bodyHeight = minBody
	}

	metaBoxHeight := metaBoxOverhead + metaFixedLines
	if maxMeta := bodyHeight - (descBoxOverhead + minDescLines); metaBoxHeight > maxMeta {
		metaBoxHeight = maxMeta
	}
	if metaBoxHeight < metaBoxOverhead+minMetaLines {
		metaBoxHeight = metaBoxOverhead + minMetaLines
	}
	descBoxHeight := bodyHeight - metaBoxHeight
	if descBoxHeight < descBoxOverhead+minDescLines {
		descBoxHeight = descBoxOverhead + minDescLines
	}

	m.metaLines = metaBoxHeight - metaBoxOverhead
	m.descMaxLines = descBoxHeight - descBoxOverhead

	// The list panel spans both right-column boxes stacked together, so its
	// row budget must match their combined (post-clamp) height exactly or
	// the borders won't line up at the bottom.
	m.listRowsHeight = (metaBoxHeight + descBoxHeight) - listBoxOverhead
	if m.listRowsHeight < minListRows {
		m.listRowsHeight = minListRows
	}
}

// maxDescScroll is the highest descScroll that still leaves the last line
// visible — scrolling past it would just show trailing blank space.
func (m tuiModel) maxDescScroll() int {
	if n := len(m.descCacheLines) - m.descMaxLines; n > 0 {
		return n
	}
	return 0
}

// refreshDescCache re-renders the selected workflow's description (via
// glamour, or a plain-text fallback) only when the selection or the panel
// width actually changed — re-running glamour on every keystroke would be
// wasteful, and WithWordWrap is fixed at renderer construction anyway.
func (m *tuiModel) refreshDescCache() {
	entry := m.current()
	if entry == nil {
		m.descCachePath = ""
		m.descCacheLines = nil
		return
	}
	if m.rightInnerWidth <= 0 {
		return
	}
	if m.descCachePath == entry.Path && m.descCacheWidth == m.rightInnerWidth {
		return
	}
	m.descCacheLines = renderDescription(entry.WF, m.rightInnerWidth)
	m.descCachePath = entry.Path
	m.descCacheWidth = m.rightInnerWidth
}

// renderDescription renders a workflow's full description: DescriptionFile
// (markdown, via glamour) when set, falling back to the plain-text
// Description field — and never failing the panel, since a broken/missing
// markdown file is not fatal to the rest of the TUI.
func renderDescription(wf *Workflow, width int) []string {
	if wf == nil {
		return []string{theme.Dim().Render("nenhum workflow selecionado")}
	}
	if wf.DescriptionFile != "" {
		path := filepath.Join(workflowsDir(), wf.DescriptionFile)
		if data, err := os.ReadFile(path); err == nil {
			if r, err := glamour.NewTermRenderer(
				glamour.WithStandardStyle("dark"),
				glamour.WithWordWrap(width),
			); err == nil {
				if out, err := r.Render(string(data)); err == nil {
					return strings.Split(strings.TrimRight(out, "\n"), "\n")
				}
			}
		}
	}
	if wf.Description == "" {
		return []string{theme.Dim().Render("sem descrição")}
	}
	return strings.Split(tuiui.WrapText(wf.Description, width), "\n")
}

// stepTools returns the distinct tool names used across a workflow's steps,
// in first-seen order, for the metadata panel.
func stepTools(steps []Step) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range steps {
		if s.Tool == "" || seen[s.Tool] {
			continue
		}
		seen[s.Tool] = true
		out = append(out, s.Tool)
	}
	return out
}

// renderPanel wraps content in a bordered panel sized to w×availH — used by
// the single full-width run-mode view. See tuiui.Theme.Panel's doc comment
// on why content is pre-padded with PadLines/PadToHeight instead of relying
// on lipgloss's own Width() wrapping.
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

func (m tuiModel) renderListPanel() string {
	title := theme.Title().Render(fmt.Sprintf("workflows (%d)", len(m.entries)))
	var body string
	switch {
	case m.listErr != nil:
		body = theme.Error().Render("erro: " + m.listErr.Error())
	case len(m.entries) == 0:
		body = theme.Muted().Render("nenhum workflow em " + workflowsDir())
	default:
		lines := make([]string, 0, len(m.entries))
		for i, e := range m.entries {
			if i == m.cursor {
				lines = append(lines, theme.Title().Render("▸ "+e.Name))
			} else {
				lines = append(lines, "  "+e.Name)
			}
		}
		body = strings.Join(lines, "\n")
	}
	content := title + "\n" + tuiui.PadToHeight(body, m.listRowsHeight)
	content = tuiui.PadLines(content, m.listInnerWidth)
	return theme.Panel(m.focus == focusList).Render(content)
}

func (m tuiModel) renderMetaPanel() string {
	title := theme.Title().Render("metadados")
	entry := m.current()
	var body string
	if entry == nil || entry.WF == nil {
		body = theme.Dim().Render("nenhum workflow selecionado")
	} else {
		wf := entry.WF
		creator := wf.Metadata.Creator
		if creator == "" {
			creator = "-"
		}
		installedAt := wf.Metadata.InstalledAt
		if installedAt == "" {
			installedAt = "-"
		}
		onCalendar := wf.Schedule.OnCalendar
		if onCalendar == "" {
			onCalendar = "-"
		}
		scheduleStatus := theme.Muted().Render("agendamento: inativo")
		if IsScheduled(entry.File) {
			scheduleStatus = theme.Success().Render("agendamento: ativo")
		}
		tools := stepTools(wf.Steps)
		toolsLine := "tools: " + strings.Join(tools, ", ")
		if len(tools) == 0 {
			toolsLine = "tools: -"
		}
		body = strings.Join([]string{
			"criador: " + creator,
			"instalado em: " + installedAt,
			scheduleStatus,
			"on_calendar: " + onCalendar,
			"",
			toolsLine,
		}, "\n")
	}
	content := title + "\n" + tuiui.PadToHeight(body, m.metaLines)
	content = tuiui.PadLines(content, m.rightInnerWidth)
	return theme.Panel(false).Render(content)
}

func (m tuiModel) renderDescPanel() string {
	entry := m.current()
	title := "descrição"
	var body string
	switch {
	case entry == nil || entry.WF == nil:
		body = theme.Dim().Render("nenhum workflow selecionado")
	default:
		lines := m.descCacheLines
		if total := len(lines); total > m.descMaxLines {
			title = fmt.Sprintf("descrição (%d–%d/%d)", m.descScroll+1, min(m.descScroll+m.descMaxLines, total), total)
		}
		body = m.renderDescBody(lines)
	}
	content := theme.Title().Render(title) + "\n" + tuiui.PadToHeight(body, m.descMaxLines)
	content = tuiui.PadLines(content, m.rightInnerWidth)
	return theme.Panel(m.focus == focusDesc).Render(content)
}

// renderDescBody clips lines to the panel's fixed descMaxLines budget,
// starting at descScroll — this is what keeps the description panel's
// rendered height constant regardless of content length.
func (m tuiModel) renderDescBody(lines []string) string {
	scroll := m.descScroll
	if max := m.maxDescScroll(); scroll > max {
		scroll = max
	}
	end := scroll + m.descMaxLines
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[scroll:end], "\n")
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "carregando..."
	}
	if m.helpModal.Visible() {
		return m.helpModal.View(theme)
	}
	if m.mode == modeRun {
		return m.viewRun()
	}
	return m.viewList()
}

func (m tuiModel) viewList() string {
	w := m.width
	header := theme.Header(w).Render("taglue · workflows")

	listBox := m.renderListPanel()
	metaBox := m.renderMetaPanel()
	descBox := m.renderDescPanel()

	rightCol := lipgloss.JoinVertical(lipgloss.Left, metaBox, descBox)
	body := lipgloss.JoinHorizontal(lipgloss.Top, listBox, strings.Repeat(" ", panelGap), rightCol)

	status := fmt.Sprintf("%d workflows", len(m.entries))
	if m.scheduleStatus != "" {
		status = m.scheduleStatus
	}
	footer := tuiui.NewFooter(bindingsOf("focus-list", "focus-desc", "scroll", "run", "toggle-schedule", "help", "quit")...).
		Status(status).
		Render(w, theme)

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
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
	footer := tuiui.NewFooter(bindingsOf("back", "quit")...).
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
