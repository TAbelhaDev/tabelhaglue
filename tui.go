package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/TAbelhaDev/tabelhatuiui"
	"github.com/TAbelhaDev/tabelhatuiui/markdown"
	"github.com/TAbelhaDev/tabelhatuiui/schedule"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
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
	// installed_at, group, schedule status, on_calendar, blank, tools.
	metaFixedLines = 7
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

	// listScroll is the first visible line index in the list panel.
	listScroll int

	// descVP is the scrollable description panel, using the shared
	// markdown.Render + Viewport from tabelhatuiui.
	descVP *markdown.Panel

	scheduling     bool
	scheduleStatus string

	helpModal     *tuiui.HelpModal
	settingsModal *tuiui.SettingsModal

	// notice is a transient status message that auto-clears after a timeout
	// (the kanban "renderNotice" pattern).
	noticeMsg     string
	noticeClearAt time.Time

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

	// schedule form state (huh)
	scheduleForm  *huh.Form
	scheduleEntry *workflowEntry
}

func newTUIModel() tuiModel {
	_ = reg.Load()
	entries, err := listWorkflowEntries()
	return tuiModel{
		entries: entries,
		listErr: err,
		descVP:  markdown.NewPanel(),
		helpModal: tuiui.NewHelpModal(tuiui.HelpSection{
			Title:      "Atalhos",
			BindingsFn: reg.Bindings,
		}),
		settingsModal: tuiui.NewSettingsModal(reg),
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

// noticeClearedMsg is sent by the tea.Tick timeout to dismiss the transient
// status message — same pattern as kanban's renderNotice/clearNoticeMsg.
type noticeClearedMsg struct{}

func clearNoticeCmd() tea.Cmd {
	return tea.Tick(4*time.Second, func(_ time.Time) tea.Msg {
		return noticeClearedMsg{}
	})
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

func (m tuiModel) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "panic na TUI: %v\n", r)
			model = m
			cmd = tea.Quit
		}
	}()

	// Notice timeout: clear the transient message if still on the same gen.
	switch msg.(type) {
	case noticeClearedMsg:
		m.noticeMsg = ""
		return m, nil
	}

	// Schedule toggled: update the cached Scheduled field on the entry.
	switch msg := msg.(type) {
	case scheduleToggledMsg:
		m.scheduling = false
		switch {
		case msg.err != nil:
			m.noticeMsg = "erro: " + msg.err.Error()
			m.noticeClearAt = time.Now().Add(8 * time.Second)
		case msg.enabled:
			m.noticeMsg = fmt.Sprintf("%q agendado", msg.name)
			m.noticeClearAt = time.Now().Add(4 * time.Second)
			for i := range m.entries {
				if m.entries[i].File == msg.name {
					m.entries[i].Scheduled = true
					break
				}
			}
			sortWorkflowEntries(m.entries)
		default:
			m.noticeMsg = fmt.Sprintf("%q agendamento removido", msg.name)
			m.noticeClearAt = time.Now().Add(4 * time.Second)
			for i := range m.entries {
				if m.entries[i].File == msg.name {
					m.entries[i].Scheduled = false
					break
				}
			}
			sortWorkflowEntries(m.entries)
		}
		m.scheduleStatus = ""
		return m, clearNoticeCmd()
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.helpModal.SetSize(msg.Width, msg.Height)
		m.settingsModal.SetSize(msg.Width, msg.Height)
		m.resizeViewport()
		m.layout()
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
	}

	// Handle schedule form if active
	if m.scheduleForm != nil {
		if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "esc" {
			m.scheduleForm = nil
			m.scheduleEntry = nil
			m.scheduleStatus = ""
			return m, nil
		}

		// Protect against huh panics — if the form's internal state
		// machine misbehaves, kill the form instead of the whole TUI.
		var form tea.Model
		var cmd tea.Cmd
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "panic no form de agendamento: %v\n", r)
					m.scheduleForm = nil
					m.scheduleEntry = nil
					m.noticeMsg = fmt.Sprintf("erro no form: %v", r)
					m.noticeClearAt = time.Now().Add(8 * time.Second)
					form = nil
					cmd = nil
				}
			}()
			form, cmd = m.scheduleForm.Update(msg)
		}()

		if form != nil {
			if f, ok := form.(*huh.Form); ok {
				m.scheduleForm = f
			}
		}

		if m.scheduleForm == nil {
			return m, cmd
		}

		if m.scheduleForm.State == huh.StateCompleted {
			entry := m.scheduleEntry
			form := m.scheduleForm
			m.scheduleForm = nil
			m.scheduleEntry = nil

			kind, _ := form.Get("type").(schedule.Kind)
			if kind == schedule.KindManual {
				// Disable any existing schedule and remove sidecar
				m.scheduling = true
				m.scheduleStatus = "removendo agendamento..."
				return m, func() tea.Msg {
					_ = removeSchedule(entry.File)
					if IsScheduled(entry.File) {
						if err := DisableWorkflow(entry.File); err != nil {
							return scheduleToggledMsg{name: entry.File, enabled: false, err: err}
						}
					}
					return scheduleToggledMsg{name: entry.File, enabled: false, err: nil}
				}
			}

			hour, minute, errTime := 0, 0, error(nil)
			if kind != schedule.KindManual {
				hour, minute, errTime = schedule.ParseHHMM(form.GetString("time"))
			}

			sched := Schedule{Kind: kindName(kind), Hour: hour, Minute: minute}
			valid := errTime == nil
			switch kind {
			case schedule.KindWeekly:
				weekdays, _ := form.Get("weekdays").([]time.Weekday)
				for _, w := range weekdays {
					sched.Weekdays = append(sched.Weekdays, weekdayStr(w))
				}
				valid = valid && len(weekdays) > 0
			case schedule.KindMonthly:
				dayOfMonth, errDOM := strconv.Atoi(strings.TrimSpace(form.GetString("dayOfMonth")))
				sched.DayOfMonth = dayOfMonth
				valid = valid && errDOM == nil
			case schedule.KindOneshot, schedule.KindCycle:
				dom, month, errDate := schedule.ParseDDMM(form.GetString("date"))
				sched.DOM, sched.Month = dom, month
				valid = valid && errDate == nil
				if kind == schedule.KindCycle {
					cycle, errCycle := schedule.ParseCycle(form.GetString("cycle"))
					sched.Cycle = cycle
					valid = valid && errCycle == nil
				}
			}

			if valid {
				m.scheduling = true
				m.scheduleStatus = "agendando..."
				return m, func() tea.Msg {
					if err := saveSchedule(entry.File, sched); err != nil {
						return scheduleToggledMsg{name: entry.File, enabled: false, err: err}
					}
					wf, err := loadWorkflow(entry.Path)
					if err != nil {
						return scheduleToggledMsg{name: entry.File, enabled: false, err: err}
					}
					entry.WF = wf
					enabled, err := ToggleSchedule(entry.File, wf)
					return scheduleToggledMsg{name: entry.File, enabled: enabled, err: err}
				}
			}
			m.scheduleStatus = "horário inválido"
			return m, nil
		}
		return m, cmd
	}

	// Settings modal: consume all input while visible.
	if m.settingsModal.Update(msg) {
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
		if key.Matches(keyMsg, resolve("settings")) {
			m.settingsModal.Toggle()
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
	case key.Matches(msg, resolve("refresh")):
		entries, err := listWorkflowEntries()
		m.entries = entries
		m.listErr = err
		m.descVP.Viewport().Reset()
		m.reclampList()
		return m, nil
	case key.Matches(msg, resolve("nav")):
		navKeys := resolve("nav").Keys()
		switch {
		case len(navKeys) > 0 && msg.String() == navKeys[0]:
			m.focus = focusList
		case len(navKeys) > 1 && msg.String() == navKeys[1]:
			m.focus = focusDesc
		}
		return m, nil
	case key.Matches(msg, resolve("run")):
		if entry := m.current(); entry != nil {
			return m.startRun(*entry)
		}
		return m, nil
	case key.Matches(msg, resolve("toggle-schedule")):
		if m.scheduling || m.scheduleForm != nil {
			return m, nil
		}
		if entry := m.current(); entry != nil {
			m.scheduleEntry = entry
			// Pre-fill the form with the current schedule if one exists
			// (from sidecar or on_calendar). This way the user always
			// sees the form and can modify/disable the schedule.
			m.scheduleForm = huh.NewForm(schedule.Groups(preFillSchedule(entry.WF))...)
			return m, m.scheduleForm.Init()
		}
		return m, nil
	}

	if m.focus == focusDesc {
		if m.descVP.Viewport().Update(msg) {
			return m, nil
		}
		return m, nil
	}

	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.entries)-1 {
			m.cursor++
			m.descVP.Viewport().Reset()
			m.reclampList()
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			m.descVP.Viewport().Reset()
			m.reclampList()
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
// letting lipgloss stretch content past what fits.
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

	// Dynamic meta sizing: use the selected workflow's actual content
	// length, clamped between minMetaLines and (bodyHeight - minDescLines).
	// This gives short descriptions more room and long tools lists space.
	metaContentLines := metaFixedLines // default fallback
	entry := m.current()
	if entry != nil {
		metaContentLines = len(computeMetaContent(entry))
	}
	metaBoxHeight := metaBoxOverhead + metaContentLines
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
	m.descVP.Viewport().SetHeight(m.descMaxLines)

	// The list panel spans both right-column boxes stacked together, so its
	// row budget must match their combined (post-clamp) height exactly or
	// the borders won't line up at the bottom.
	m.listRowsHeight = (metaBoxHeight + descBoxHeight) - listBoxOverhead
	if m.listRowsHeight < minListRows {
		m.listRowsHeight = minListRows
	}
}

// maxListScroll is the highest listScroll that still leaves the last line
// visible. The number of rendered lines includes both workflow entries and
// group header lines.
func (m tuiModel) maxListScroll() int {
	if n := m.listRenderedLines() - m.listRowsHeight; n > 0 {
		return n
	}
	return 0
}

// reclampList keeps the list scroll window valid after a cursor move or
// entries reload — mirrors reclamp from tabelhakanban.
func (m *tuiModel) reclampList() {
	// Ensure cursor is in bounds.
	if m.cursor < 0 {
		m.cursor = 0
	}
	if max := len(m.entries) - 1; m.cursor > max {
		m.cursor = max
	}
	if max := m.maxListScroll(); m.listScroll > max {
		m.listScroll = max
	}
	if m.listScroll < 0 {
		m.listScroll = 0
	}
	// Scroll down if cursor is below visible window.
	visibleEnd := m.listScroll + m.listRowsHeight
	if m.cursor >= visibleEnd {
		m.listScroll = m.cursor - m.listRowsHeight + 1
	}
	// Scroll up if cursor is above visible window.
	if m.cursor < m.listScroll {
		m.listScroll = m.cursor
	}
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

// listRenderedLines returns the total number of lines that renderListPanel
// would produce (section headers + workflow entries) — used by
// maxListScroll to compute the scrollable range.
func (m tuiModel) listRenderedLines() int {
	if len(m.entries) == 0 {
		return 0
	}
	// One section header per status transition (ativos → inativos).
	count := 1 // first section header
	lastScheduled := m.entries[0].Scheduled
	for _, e := range m.entries {
		if e.Scheduled != lastScheduled {
			count++
			lastScheduled = e.Scheduled
		}
		count++
	}
	return count
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
		// Count per-section for header labels.
		var scheduled, unscheduled int
		for _, e := range m.entries {
			if e.Scheduled {
				scheduled++
			} else {
				unscheduled++
			}
		}
		lines := make([]string, 0, len(m.entries))
		var lastScheduled *bool // nil forces header on first entry
		for i, e := range m.entries {
			if lastScheduled == nil || e.Scheduled != *lastScheduled {
				s := e.Scheduled
				lastScheduled = &s
				if e.Scheduled {
					lines = append(lines, theme.Success().Render(fmt.Sprintf("ativos (%d)", scheduled)))
				} else {
					lines = append(lines, theme.Muted().Render(fmt.Sprintf("inativos (%d)", unscheduled)))
				}
			}
			glyph := theme.Dim().Render("○")
			if e.Scheduled {
				glyph = theme.Success().Render("●")
			}
			if i == m.cursor {
				lines = append(lines, theme.Title().Render("▸ "+glyph+" "+e.Name))
			} else {
				lines = append(lines, "  "+glyph+" "+e.Name)
			}
		}
		// Clip to visible window (list scroll).
		scroll := m.listScroll
		if scroll > len(lines) {
			scroll = len(lines)
		}
		end := scroll + m.listRowsHeight
		if end > len(lines) {
			end = len(lines)
		}
		body = strings.Join(lines[scroll:end], "\n")
	}
	content := title + "\n" + tuiui.PadToHeight(body, m.listRowsHeight)
	content = tuiui.PadLines(content, m.listInnerWidth)
	return theme.Panel(m.focus == focusList).Render(content)
}

// computeMetaContent builds the metadata panel lines for a workflow.
func computeMetaContent(entry *workflowEntry) []string {
	if entry == nil || entry.WF == nil {
		return []string{theme.Dim().Render("nenhum workflow selecionado")}
	}
	wf := entry.WF
	creator := wf.Metadata.Creator
	if creator == "" {
		creator = "-"
	}
	installedAt := wf.Metadata.InstalledAt
	if installedAt == "" {
		installedAt = "-"
	}
	updatedAt := wf.Metadata.UpdatedAt
	if updatedAt == "" {
		updatedAt = "-"
	}
	scheduleStr := "-"
	if wf.Schedule.Kind != "" {
		scheduleStr = scheduleString(wf.Schedule)
	} else if wf.Schedule.OnCalendar != "" {
		scheduleStr = wf.Schedule.OnCalendar
	}
	scheduleStatus := theme.Muted().Render("agendamento: inativo")
	if entry.Scheduled {
		scheduleStatus = theme.Success().Render("agendamento: ativo")
	}
	tools := stepTools(wf.Steps)
	toolsLine := "tools: " + strings.Join(tools, ", ")
	if len(tools) == 0 {
		toolsLine = "tools: -"
	}
	group := entry.Group
	if group == "" {
		group = "-"
	}
	return []string{
		"criador: " + creator,
		"instalado em: " + installedAt,
		"atualizado em: " + updatedAt,
		"grupo: " + group,
		scheduleStatus,
		"schedule: " + scheduleStr,
		"",
		toolsLine,
	}
}

func (m tuiModel) renderMetaPanel() string {
	title := theme.Title().Render("metadados")
	entry := m.current()
	body := strings.Join(computeMetaContent(entry), "\n")
	body = tuiui.WrapText(body, m.rightInnerWidth)
	content := title + "\n" + tuiui.PadToHeight(body, m.metaLines)
	content = tuiui.PadLines(content, m.rightInnerWidth)
	return theme.Panel(false).Render(content)
}

func (m tuiModel) renderDescPanel() string {
	entry := m.current()
	m.descVP.Focus(m.focus == focusDesc)

	if entry == nil || entry.WF == nil {
		m.descVP.SetTitle("descrição")
		m.descVP.SetMarkdown("", m.rightInnerWidth, theme)
		return m.descVP.View(theme, m.rightInnerWidth+4)
	}

	// Build the markdown body: DescriptionFile (markdown) takes precedence.
	var body string
	if entry.WF.DescriptionFile != "" {
		path := filepath.Join(workflowsDir(), entry.WF.DescriptionFile)
		if data, err := os.ReadFile(path); err == nil {
			body = string(data)
		}
	}
	if body == "" {
		body = entry.WF.Description
	}
	if body == "" {
		body = theme.Dim().Render("sem descrição")
	}

	m.descVP.SetTitle(entry.WF.Name)
	m.descVP.SetMarkdown(body, m.rightInnerWidth, theme)
	return m.descVP.View(theme, m.rightInnerWidth+4)
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "carregando..."
	}
	if m.settingsModal.Visible() {
		return m.settingsModal.View(theme)
	}
	if m.helpModal.Visible() {
		return m.helpModal.View(theme)
	}
	if m.mode == modeRun {
		return m.viewRun()
	}
	if m.scheduleForm != nil {
		return m.viewScheduleForm()
	}
	return m.viewList()
}

// viewScheduleForm renders the huh schedule form as a centered modal
// overlay. Without this, the form processes input but never renders —
// the TUI looks frozen because the user can't see the form.
func (m tuiModel) viewScheduleForm() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	body := m.scheduleForm.View()
	box := theme.Modal().Render(body)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

func (m tuiModel) viewList() string {
	w := m.width
	header := theme.Header(w).Render("taglue · workflows")

	listBox := m.renderListPanel()
	metaBox := m.renderMetaPanel()
	descBox := m.renderDescPanel()

	rightCol := lipgloss.JoinVertical(lipgloss.Left, metaBox, descBox)
	body := lipgloss.JoinHorizontal(lipgloss.Top, listBox, strings.Repeat(" ", panelGap), rightCol)

	scheduledCount := 0
	for _, e := range m.entries {
		if e.Scheduled {
			scheduledCount++
		}
	}
	status := fmt.Sprintf("%d workflows", len(m.entries))
	if scheduledCount > 0 {
		status += fmt.Sprintf(" · %d agendados", scheduledCount)
	}
	// Transient notice takes priority over the count.
	if m.noticeMsg != "" {
		status = m.noticeMsg
	} else if m.scheduleStatus != "" {
		status = m.scheduleStatus
	}
	footer := tuiui.NewFooter(bindingsOf("nav", "scroll", "run", "toggle-schedule", "refresh", "help", "quit")...).
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

// kindName converts schedule.Kind to string for storage.
func kindName(k schedule.Kind) string {
	switch k {
	case schedule.KindOneshot:
		return "oneshot"
	case schedule.KindDaily:
		return "daily"
	case schedule.KindWeekly:
		return "weekly"
	case schedule.KindMonthly:
		return "monthly"
	case schedule.KindCycle:
		return "cycle"
	case schedule.KindManual:
		return "manual"
	default:
		return "daily"
	}
}

// weekdayStr converts time.Weekday to string for storage.
func weekdayStr(w time.Weekday) string {
	switch w {
	case time.Monday:
		return "Mon"
	case time.Tuesday:
		return "Tue"
	case time.Wednesday:
		return "Wed"
	case time.Thursday:
		return "Thu"
	case time.Friday:
		return "Fri"
	case time.Saturday:
		return "Sat"
	case time.Sunday:
		return "Sun"
	default:
		return "Mon"
	}
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
