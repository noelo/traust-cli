package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/traust-security/traust-cli/internal/theme"
	"github.com/traust-security/traust-cli/internal/views"
)

// reportModel browses a report's findings: a severity-colored table with a
// glamour-rendered detail pane. Read-only.
type reportModel struct {
	styles        theme.Styles
	table         table.Model
	viewport      viewport.Model
	report        *views.Report
	mode          string // "table" | "detail"
	renderer      *glamour.TermRenderer
	width, height int
}

var severityOrder = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4, "none": 5, "unrated": 6}

func newReportModel(rep *views.Report, width, height int) reportModel {
	styles := theme.Default()
	cols := []table.Column{
		{Title: "SEV", Width: 9},
		{Title: "ID", Width: 30},
		{Title: "TITLE", Width: width - 45},
	}
	rows := make([]table.Row, 0, len(rep.Findings))
	sorted := make([]views.Finding, len(rep.Findings))
	copy(sorted, rep.Findings)
	// Sort by severity, stable.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && severityOrder[sevKey(sorted[j])] < severityOrder[sevKey(sorted[j-1])]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	for _, f := range sorted {
		id := f.ID
		if id == "" {
			id = "—"
		}
		if len(id) > 28 {
			id = id[:27] + "…"
		}
		rows = append(rows, table.Row([]string{strings.ToUpper(f.Severity), id, f.Title}))
	}
	t := table.New(table.WithColumns(cols), table.WithRows(rows), table.WithHeight(minInt(height-8, 20)), table.WithWidth(width-4))
	t.SetStyles(table.Styles{
		Header:   styles.Header.BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(theme.Palette.Muted),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(theme.Palette.Text).Background(theme.Palette.Surface),
	})
	vp := viewport.New(width-4, height-10)
	renderer, _ := glamour.NewTermRenderer(glamour.WithWordWrap(width-8), glamour.WithStandardStyle("dark"))
	return reportModel{styles: styles, table: t, viewport: vp, report: rep, mode: "table", renderer: renderer, width: width, height: height}
}

func sevKey(f views.Finding) string {
	if f.Severity == "" {
		return "unrated"
	}
	return f.Severity
}

func (m reportModel) Init() tea.Cmd { return nil }

func (m reportModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width - 4
		m.viewport.Height = msg.Height - 10
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.mode == "table" && len(m.report.Findings) > 0 {
				m.showDetail(m.table.Cursor())
				m.mode = "detail"
				return m, nil
			}
		case "esc":
			m.mode = "table"
			return m, nil
		}
	}
	if m.mode == "detail" {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m *reportModel) showDetail(idx int) {
	if idx < 0 || idx >= len(m.report.Findings) {
		return
	}
	f := m.report.Findings[idx]
	md := fmt.Sprintf("# %s\n\n**Severity:** %s  ·  **ID:** %s\n\n%s\n", f.Title, f.Severity, f.ID, f.Description)
	rendered := md
	if m.renderer != nil {
		if out, err := m.renderer.Render(md); err == nil {
			rendered = out
		}
	}
	m.viewport.SetContent(rendered)
	m.viewport.GotoTop()
}

func (m reportModel) View() string {
	var b strings.Builder
	counts := m.report.CountsBySeverity()
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low", "info"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, theme.SeverityStyle(m.styles, sev).Render(fmt.Sprintf("%s %d", sev, n)))
		}
	}
	title := m.styles.App.Render(" traust ") + m.styles.Header.Render(" "+shortPath(m.report.Path))
	b.WriteString("\n" + title + "\n")
	if len(parts) > 0 {
		b.WriteString(strings.Join(parts, "  ") + "\n")
	}
	b.WriteString("\n")

	if m.mode == "table" {
		if len(m.report.Findings) == 0 {
			b.WriteString(m.styles.Muted.Render("no findings in this report\n"))
		} else {
			b.WriteString(m.table.View() + "\n")
			b.WriteString(m.styles.Muted.Render("↑/↓ select · enter detail · q quit\n"))
		}
		return b.String()
	}
	frame := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Palette.Muted).Padding(0, 1).Render(m.viewport.View())
	b.WriteString(frame + "\n")
	b.WriteString(m.styles.Muted.Render("↑/↓ scroll · esc back · q quit\n"))
	return b.String()
}

// BrowseReport runs the report browser TUI.
func BrowseReport(rep *views.Report) error {
	p := tea.NewProgram(newReportModel(rep, 100, 40), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) > 3 {
		return "…/" + strings.Join(parts[len(parts)-3:], "/")
	}
	return p
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
