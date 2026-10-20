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

// monitorSink forwards run updates into the monitor's channel. Done closes
// the channel exactly once so the relay terminates even on error paths.
type monitorSink struct {
	ch    chan monitorItem
	close sync.Once
}

func (s *monitorSink) Progress(msg string) {
	s.ch <- monitorItem{kind: itemProgress, text: msg}
}
func (s *monitorSink) Event(ev harness.Event) {
	s.ch <- monitorItem{kind: itemEvent, ev: ev, text: ev.Text}
}
func (s *monitorSink) Raw(line string) {
	s.ch <- monitorItem{kind: itemRaw, text: line}
}
func (s *monitorSink) Done() {
	s.close.Do(func() { close(s.ch) })
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
	started             time.Time
	width, height       int
}

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
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
	case monitorItem:
		switch msg.kind {
		case itemEvent:
			m.applyEvent(msg.ev)
		case itemRaw:
			m.pushLine(m.styles.Muted.Render("  " + truncateLine(msg.text, m.width-4)))
		case itemProgress:
			style := m.styles.Accent
			if strings.HasPrefix(msg.text, "warning:") || strings.HasPrefix(msg.text, "note:") {
				style = m.styles.Warn
			}
			if strings.Contains(msg.text, "failed") || strings.Contains(msg.text, "refused") {
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
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.aborting {
				return m, tea.Quit
			}
			m.aborting = true
			m.pushLine(m.styles.Warn.Render("› aborting — terminating the harness…"))
			if m.cancel != nil {
				m.cancel()
			}
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
		m.pushLine(truncateLine(ev.Text, m.width-4))
	case harness.EventToolCall:
		m.pushLine(m.styles.Accent.Render("→ " + ev.Tool))
	case harness.EventUsage:
		m.tokensIn, m.tokensOut = ev.TokensIn, ev.TokensOut
	case harness.EventResult:
		m.tokensIn, m.tokensOut = ev.TokensIn, ev.TokensOut
		m.cost = ev.CostUSD
		if ev.Text != "" {
			m.pushLine(m.styles.OK.Render("✓ " + truncateLine(ev.Text, m.width-4)))
		}
	case harness.EventError:
		m.failed = true
		m.pushLine(m.styles.Err.Render("✗ " + truncateLine(ev.Text, m.width-4)))
	case harness.EventNeedsInput:
		m.failed = true
		m.pushLine(m.styles.Err.Render("✗ the skill asked for interactive input — run it interactively in your harness"))
	}
}

func (m *monitorModel) pushLine(line string) {
	m.lines = append(m.lines, line)
	if len(m.lines) > 1000 {
		m.lines = m.lines[len(m.lines)-1000:]
	}
	m.viewport.SetContent(strings.Join(m.lines, "\n"))
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
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Left,
		title,
		m.styles.Header.Render(fmt.Sprintf(" %s · %s · %s · %s · %s", m.header.Skill, m.header.Harness, ellipsize(m.header.Model, 40), elapsed, status))))
	b.WriteString("\n\n")

	body := m.viewport.View()
	frame := lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, true).
		BorderForeground(theme.Palette.Muted).Render(body)
	b.WriteString(frame)
	b.WriteString("\n")

	// Status bar: usage + budget bar.
	usage := fmt.Sprintf("in %s · out %s", human(m.tokensIn), human(m.tokensOut))
	if m.cost > 0 {
		usage += fmt.Sprintf(" · $%.2f", m.cost)
	}
	var bar string
	if m.header.BudgetCap > 0 {
		bar = m.budgetBar(m.tokensOut, m.header.BudgetCap)
		usage = usage + "  " + bar
	}
	b.WriteString(m.styles.StatusBar.Render(usage))
	b.WriteString("\n")

	footer := "ctrl+c abort"
	if m.done {
		footer = "q quit · journal written"
	}
	b.WriteString(m.styles.Muted.Render(footer))
	b.WriteString("\n")
	return b.String()
}

func (m monitorModel) budgetBar(out, cap int64) string {
	width := 24
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
// sink wired to the monitor. cancel is invoked when the user hits ctrl+c.
func RunMonitor(header MonitorHeader, cancel func(), runFn func(sink runner.Sink) error) error {
	items := make(chan monitorItem, 128)
	sink := &monitorSink{ch: items}

	prog := tea.NewProgram(newMonitor(header, cancel, 80, 24),
		tea.WithAltScreen(), tea.WithMouseCellMotion())

	var runErr error
	var once sync.Once
	finish := func() { once.Do(func() { close(items) }) }

	go func() {
		runErr = runFn(sink)
		finish()
	}()
	go func() {
		for it := range items {
			prog.Send(it)
		}
		prog.Send(monitorItem{kind: itemDone})
	}()

	if _, err := prog.Run(); err != nil {
		return err
	}
	return runErr
}

func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ")
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
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
