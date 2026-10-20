// Package tui contains the interactive screens: the run monitor, the skill
// picker, the report browser and the init wizard.
package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/traust-security/traust-cli/internal/harness"
	"github.com/traust-security/traust-cli/internal/runner"
	"github.com/traust-security/traust-cli/internal/theme"
)

// MonitorHeader describes the run for the monitor's header bar.
type MonitorHeader struct {
	Skill     string
	Harness   string
	Model     string
	BudgetCap int64
}

type monitorItemKind int

const (
	itemEvent monitorItemKind = iota
	itemRaw
	itemProgress
	itemDone
)

type monitorItem struct {
	kind monitorItemKind
	ev   harness.Event
	text string
}

// monitorSink forwards run updates into the monitor's channel. Sends are
// non-blocking (the journal keeps everything; the UI may drop under
// backpressure) and guarded against a close, so the runner goroutine can
// never panic or deadlock when the program exits before the run does.
type monitorSink struct {
	ch     chan monitorItem
	mu     sync.Mutex
	closed bool
	once   sync.Once
}

func (s *monitorSink) send(it monitorItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- it:
	default: // UI backpressure: drop; the journal keeps everything.
	}
}

func (s *monitorSink) Progress(msg string) {
	s.send(monitorItem{kind: itemProgress, text: msg})
}
func (s *monitorSink) Event(ev harness.Event) {
	s.send(monitorItem{kind: itemEvent, ev: ev, text: ev.Text})
}
func (s *monitorSink) Raw(line string) {
	s.send(monitorItem{kind: itemRaw, text: line})
}
func (s *monitorSink) Done() { s.Close() }

// Close tears the channel down exactly once, whether the runner finished
// cleanly or the program exited first.
func (s *monitorSink) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
}

type tickMsg time.Time

type monitorModel struct {
	header              MonitorHeader
	cancel              func()
	styles              theme.Styles
	viewport            viewport.Model
	lines               []string
	tokensIn, tokensOut int64
	cost                float64
	band                string
	done                bool
	failed              bool
	aborting            bool
	// abortArmed is the ctrl+c confirmation state: the first press asks,
	// the second (within armWindow) actually aborts. A single stray ctrl+c
	// must not kill a 10-minute audit that merely looks quiet.
	abortArmed    bool
	abortArmedAt  time.Time
	lastOutput    time.Time
	started       time.Time
	width, height int
}

const armWindow = 6 * time.Second

func newMonitor(header MonitorHeader, cancel func(), width, height int) monitorModel {
	vp := viewport.New(width-2, height-8)
	return monitorModel{
		header: header, cancel: cancel, styles: theme.Default(),
		viewport: vp, band: "within", started: time.Now(),
		width: width, height: height,
	}
}

func (m monitorModel) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		// Disarm the abort confirmation when the window passes.
		if m.abortArmed && time.Since(m.abortArmedAt) > armWindow {
			m.abortArmed = false
		}
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
	case monitorItem:
		m.lastOutput = time.Now()
		switch msg.kind {
		case itemEvent:
			m.applyEvent(msg.ev)
		case itemRaw:
			m.pushLine(m.styles.Muted.Render("  " + msg.text))
		case itemProgress:
			style := m.styles.Accent
			if strings.HasPrefix(msg.text, "warning:") || strings.HasPrefix(msg.text, "note:") {
				style = m.styles.Warn
			}
			if strings.Contains(msg.text, "failed") || strings.Contains(msg.text, "refused") {
				style = m.styles.Err
				m.failed = true
			}
			if strings.Contains(msg.text, "is not runnable") {
				style = m.styles.Err
				m.failed = true
			}
			if rest, ok := strings.CutPrefix(msg.text, "budget band "); ok {
				if fields := strings.Fields(rest); len(fields) > 0 {
					m.band = fields[0]
				}
			}
			m.pushLine(style.Render("› " + msg.text))
		case itemDone:
			m.done = true
			m.pushLine(m.styles.Muted.Render("— run complete —"))
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width - 2
		m.viewport.Height = msg.Height - 8
		m.rebuild()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.done {
				return m, tea.Quit
			}
			if m.aborting {
				// Second press while already aborting: leave the view; the
				// runner keeps tearing down in the background.
				return m, tea.Quit
			}
			if m.abortArmed {
				m.aborting = true
				m.pushLine(m.styles.Warn.Render("› aborting — terminating the harness…"))
				if m.cancel != nil {
					m.cancel()
				}
				return m, nil
			}
			m.abortArmed = true
			m.abortArmedAt = time.Now()
			m.pushLine(m.styles.Warn.Render("› press ctrl+c again to abort the run"))
			return m, nil
		case "q", "enter", "esc":
			if m.done {
				return m, tea.Quit
			}
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m *monitorModel) applyEvent(ev harness.Event) {
	switch ev.Kind {
	case harness.EventText:
		m.pushLine(ev.Text)
	case harness.EventToolCall:
		m.pushLine(m.styles.Accent.Render("→ " + ev.Tool))
	case harness.EventUsage:
		m.tokensIn, m.tokensOut = ev.TokensIn, ev.TokensOut
	case harness.EventResult:
		m.tokensIn, m.tokensOut = ev.TokensIn, ev.TokensOut
		m.cost = ev.CostUSD
		if ev.Text != "" {
			m.pushLine(m.styles.OK.Render("✓ " + ev.Text))
		}
	case harness.EventError:
		m.failed = true
		m.pushLine(m.styles.Err.Render("✗ " + ev.Text))
	case harness.EventNeedsInput:
		m.failed = true
		m.pushLine(m.styles.Err.Render("✗ the skill asked for interactive input — run it interactively in your harness"))
	}
}

// pushLine stores a styled line UNWRAPPED and rebuilds the viewport at the
// current width. Lines are wrapped, never truncated — the transcript stays
// complete at any terminal size.
func (m *monitorModel) pushLine(line string) {
	line = strings.ReplaceAll(line, "\r", "")
	for _, l := range strings.Split(line, "\n") {
		m.lines = append(m.lines, l)
	}
	if len(m.lines) > 1000 {
		m.lines = m.lines[len(m.lines)-1000:]
	}
	m.rebuild()
}

// rebuild re-wraps every retained line at the current viewport width. This
// is what makes a terminal resize actually take effect: previously pushed
// lines re-flow instead of staying clipped to whatever the width was when
// they arrived.
func (m *monitorModel) rebuild() {
	width := m.viewport.Width
	if width < 4 {
		width = 4
	}
	wrapped := make([]string, 0, len(m.lines))
	for _, l := range m.lines {
		wrapped = append(wrapped, Wrap(l, width))
	}
	m.viewport.SetContent(strings.Join(wrapped, "\n"))
	m.viewport.GotoBottom()
}

func (m monitorModel) View() string {
	var b strings.Builder

	elapsed := time.Since(m.started).Round(time.Second)
	status := m.styles.OK.Render("running")
	if m.done {
		if m.failed {
			status = m.styles.Err.Render("finished (with errors)")
		} else {
			status = m.styles.OK.Render("finished")
		}
	} else if m.aborting {
		status = m.styles.Warn.Render("aborting…")
	}
	title := m.styles.App.Render(" traust ")
	head := m.styles.Header.Render(fmt.Sprintf(" %s · %s · %s · %s · %s", m.header.Skill, m.header.Harness, ellipsize(m.header.Model, 40), elapsed, status))
	// The header must fit one line at any width: clip after assembly rather
	// than letting the terminal hard-wrap it into the transcript frame.
	head = Truncate(head, maxInt(m.width-lipgloss.Width(title), 10), "…")
	b.WriteString(title + head)
	b.WriteString("\n\n")

	body := m.viewport.View()
	frame := lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, true).
		BorderForeground(theme.Palette.Muted).Render(body)
	b.WriteString(frame)
	b.WriteString("\n")

	// Status bar: usage + budget bar + output age. The age indicator is the
	// answer to "is it stuck?": quiet harnesses (crush run emits one large
	// chunk every few minutes) look frozen without it.
	usage := fmt.Sprintf("in %s · out %s", human(m.tokensIn), human(m.tokensOut))
	if m.cost > 0 {
		usage += fmt.Sprintf(" · $%.2f", m.cost)
	}
	if !m.done && !m.lastOutput.IsZero() {
		if age := time.Since(m.lastOutput).Round(time.Second); age > 15*time.Second {
			usage += fmt.Sprintf(" · last output %s ago", age)
		}
	}
	var bar string
	if m.header.BudgetCap > 0 {
		// The bar yields its cells to the text on narrow terminals.
		barWidth := clampInt((m.width-lipgloss.Width(usage))/3, 8, 24)
		bar = m.budgetBar(m.tokensOut, m.header.BudgetCap, barWidth)
		usage = usage + "  " + bar
	}
	// The status bar style adds one cell of padding on each side.
	b.WriteString(m.styles.StatusBar.Render(Truncate(usage, maxInt(m.width-2, 20), "")))
	b.WriteString("\n")

	footer := "ctrl+c×2 abort"
	if m.abortArmed && !m.aborting {
		footer = "press ctrl+c again to abort"
	}
	if m.aborting {
		footer = "aborting… (ctrl+c again to leave)"
	}
	if m.done {
		footer = "q quit · journal written"
	}
	b.WriteString(m.styles.Muted.Render(footer))
	b.WriteString("\n")
	return b.String()
}

func (m monitorModel) budgetBar(out, cap int64, width int) string {
	if width < 4 {
		width = 4
	}
	ratio := 0.0
	if cap > 0 {
		ratio = float64(out) / float64(cap)
	}
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}
	var style lipgloss.Style
	switch m.band {
	case "warn":
		style = m.styles.BarWarn
	case "degrade":
		style = m.styles.BarDegrade
	case "refuse":
		style = m.styles.BarRefuse
	default:
		style = m.styles.BarOK
	}
	bar := style.Render(strings.Repeat("█", filled)) + m.styles.Muted.Render(strings.Repeat("░", width-filled))
	return fmt.Sprintf("budget %s/%s [%s]", human(out), human(cap), bar)
}

// RunMonitor runs the tea program while runFn executes the pipeline with a
// sink wired to the monitor. cancel is invoked when the user confirms an
// abort. It only returns after runFn has finished (or timed out), so the
// caller can rely on the run result being complete — force-quitting the
// view tears the harness down first.
func RunMonitor(header MonitorHeader, cancel func(), runFn func(sink runner.Sink) error) error {
	items := make(chan monitorItem, 128)
	sink := &monitorSink{ch: items}

	prog := tea.NewProgram(newMonitor(header, cancel, 80, 24),
		tea.WithAltScreen(), tea.WithMouseCellMotion())

	runDone := make(chan error, 1)
	go func() {
		err := runFn(sink)
		sink.Close() // terminate the relay even on early error paths
		runDone <- err
	}()
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		for it := range items {
			prog.Send(it)
		}
		prog.Send(monitorItem{kind: itemDone})
	}()

	_, err := prog.Run()
	// The program is gone: make sure a still-running harness is torn down,
	// then join the runner so the result is complete before returning.
	if cancel != nil {
		cancel()
	}
	select {
	case runErr := <-runDone:
		if err != nil {
			return err
		}
		return runErr
	case <-time.After(30 * time.Second):
		if err != nil {
			return err
		}
		return fmt.Errorf("run did not shut down within 30s of the monitor exiting")
	}
}

func ellipsize(s string, n int) string {
	if s == "" {
		return "(default)"
	}
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func human(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
