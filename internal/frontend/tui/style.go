package tui

import (
	"fmt"
	"path"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Colour is applied to the frame only — the title bar, the status line, the
// help line — and never to transcript content. Tool output and model text
// reach this process as data, are stripped of control sequences by safe(),
// and stay that way: a styled transcript would make "contains an escape
// sequence" stop meaning "something injected one".
var (
	styleTitle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("#0c1110")).Background(lipgloss.Color("#6ece9f"))
	styleTitleAlert = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("#0c1110")).Background(lipgloss.Color("#e0b46a"))
	styleStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("#aebbb5"))
	styleHelp   = lipgloss.NewStyle().Faint(true)
	styleRule   = lipgloss.NewStyle().Faint(true)
	styleCursor = lipgloss.NewStyle().Foreground(lipgloss.Color("#6ece9f")).Bold(true)
)

// bar pads a title across the terminal so the frame reads as a bar rather
// than a floating word.
func bar(style lipgloss.Style, title string, width int) string {
	if width <= 0 {
		return ""
	}
	if pad := width - lipgloss.Width(title); pad > 0 {
		title += spaces(pad)
	}
	return style.Render(title)
}

func spaces(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = ' '
	}
	return string(out)
}

// repoName is the last element of a workspace path: enough to tell two
// sessions apart, short enough for a list row.
func repoName(dir string) string {
	trimmed := strings.TrimRight(dir, "/")
	if trimmed == "" {
		return "—"
	}
	return path.Base(trimmed)
}

// since renders an age the way a reader scanning a list wants it.
func since(when time.Time) string {
	if when.IsZero() {
		return ""
	}
	d := time.Since(when)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return when.Format("2 Jan")
}

// twoColumn clips the label so the meta column always survives, and pads
// between them so the meta ends at the right edge.
func twoColumn(label, meta string, width int, selected bool) string {
	if width <= 0 {
		return ""
	}
	if meta == "" {
		return clip(label, width)
	}
	// Widths are display columns, never bytes: "·" is one column and two
	// bytes, and a row that mixes the two is off by one for every one.
	metaWidth := lipgloss.Width(meta)
	room := width - metaWidth - 2
	if room < 12 {
		return clip(label, width)
	}
	left := clip(label, room)
	gap := width - metaWidth - lipgloss.Width(left)
	if gap < 1 {
		gap = 1
	}
	styled := styleStatus.Render(meta)
	if selected {
		styled = meta
	}
	return left + spaces(gap) + styled
}

// ansiStrip and lipglossWidth exist for tests that assert on layout rather
// than on styling.
func ansiStrip(s string) string { return ansi.Strip(s) }

func lipglossWidth(s string) int { return lipgloss.Width(s) }
