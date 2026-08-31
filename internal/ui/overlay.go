package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// This file provides the compositing the interface needs in order to draw one
// thing on top of another: tooltips beside a selected row, and modal dialogs
// over a screen.
//
// Everything works in display cells rather than bytes, because every string
// here carries colour escapes and many carry wide glyphs. Cutting a styled
// line at a byte offset would slice an escape sequence in half and leave the
// rest of the frame the wrong colour.

// overlayAt composites box onto base with its top-left corner at the given row
// and column, replacing whatever is underneath.
//
// The result is never taller or wider than base: rows outside it are dropped
// and a patch that would run past the right edge is clipped. An overlay that
// could grow the frame would push the help bar off the bottom of the screen,
// or shear every line after it.
func overlayAt(base, box string, row, col int) string {
	if box == "" {
		return base
	}
	baseLines := strings.Split(base, "\n")
	frameWidth := 0
	for _, l := range baseLines {
		frameWidth = max(frameWidth, ansi.StringWidth(l))
	}
	for i, boxLine := range strings.Split(box, "\n") {
		target := row + i
		if target < 0 || target >= len(baseLines) {
			continue
		}
		baseLines[target] = spliceLine(baseLines[target], boxLine, col, frameWidth)
	}
	return strings.Join(baseLines, "\n")
}

// spliceLine replaces cells of line, starting at col, with patch, keeping the
// result within maxWidth cells.
func spliceLine(line, patch string, col, maxWidth int) string {
	if col < 0 {
		col = 0
	}
	if col >= maxWidth {
		return line
	}
	if col+ansi.StringWidth(patch) > maxWidth {
		patch = ansi.Truncate(patch, maxWidth-col, "")
	}
	width := ansi.StringWidth(patch)
	if width == 0 {
		return line
	}

	left := ansi.Truncate(line, col, "")
	// Pad when the underlying line stops short of the overlay position.
	if gap := col - ansi.StringWidth(left); gap > 0 {
		left += strings.Repeat(" ", gap)
	}
	right := ansi.TruncateLeft(line, col+width, "")
	return left + patch + right
}

// place centres content within the given height and reports where it landed,
// so an overlay can be positioned relative to it.
func (m *Model) place(body string, height int) (out string, top, left int) {
	top = max((height-lipgloss.Height(body))/2, 0)
	left = max((m.width-lipgloss.Width(body))/2, 0)
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, body), top, left
}

// tooltip is a description attached to a particular row of a panel.
type tooltip struct {
	// text is the description to show.
	text string
	// row is its anchor, counted from the top of the placed panel.
	row int
	// panelTop and panelLeft are where the panel was placed.
	panelTop, panelLeft int
	// panelWidth is the panel's width in cells.
	panelWidth int
}

// withTooltip composites a description box beside the panel it belongs to.
//
// Descriptions are drawn as a popover rather than inline underneath the
// selected row: an inline description changes the panel's height as the
// selection moves, so every row below it shifts by a line on each keypress,
// which makes a list unreadable to scan.
func (m *Model) withTooltip(frame string, tip tooltip) string {
	if tip.text == "" {
		return frame
	}
	th := m.style.Theme
	g := m.style.Glyphs

	// Prefer the right of the panel, fall back to the left, and give up rather
	// than overlap the panel on a narrow window. The budget is for the text
	// itself: the box adds two cells of border and two of padding on top.
	const gutter = 2    // one cell for the pointer, one of breathing room
	const boxChrome = 4 // border and padding, both sides
	rightRoom := m.width - (tip.panelLeft + tip.panelWidth) - gutter - boxChrome
	leftRoom := tip.panelLeft - gutter - boxChrome

	width := min(max(rightRoom, leftRoom), 34)
	if width < 16 {
		return frame
	}

	body := lipgloss.NewStyle().Width(width).Render(m.style.Text(th.Dim, tip.text))
	box := lipgloss.NewStyle().
		Border(m.tooltipBorder()).
		BorderForeground(th.Accent2.TermColor(m.style.Mode)).
		Padding(0, 1).
		Render(body)

	boxWidth := lipgloss.Width(box)
	boxHeight := lipgloss.Height(box)

	// Line the box's middle up with the row it describes, then pull it back
	// inside the frame if that would push it off the top or bottom.
	anchorRow := tip.panelTop + tip.row
	top := anchorRow - boxHeight/2
	top = min(max(top, 0), max(lipgloss.Height(frame)-boxHeight, 0))

	var col int
	var pointer string
	if rightRoom >= leftRoom {
		col = tip.panelLeft + tip.panelWidth + gutter
		pointer = g.PointLeft
	} else {
		col = tip.panelLeft - gutter - boxWidth
		pointer = g.PointRight
	}
	if col < 0 {
		return frame
	}

	frame = overlayAt(frame, box, top, col)

	// The pointer sits in the gutter on the anchored row, tying the box to the
	// row it belongs to.
	pointerCol := col - 1
	if rightRoom < leftRoom {
		pointerCol = col + boxWidth
	}
	return overlayAt(frame, m.style.Text(th.Accent2, pointer), anchorRow, pointerCol)
}

// tooltipBorder returns the border style for a popover.
func (m *Model) tooltipBorder() lipgloss.Border {
	if m.style.Glyphs.ASCII {
		return lipgloss.Border{
			Top: "-", Bottom: "-", Left: "|", Right: "|",
			TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
		}
	}
	return lipgloss.RoundedBorder()
}

// renderModal draws a bordered dialog centred over the current frame.
func (m *Model) renderModal(frame, title, body string, footer string, width int) string {
	th := m.style.Theme
	width = min(max(width, 24), max(m.width-6, 24))

	parts := []string{m.style.Bold(title), ""}
	parts = append(parts, body)
	if footer != "" {
		parts = append(parts, "", m.style.FaintText(footer))
	}
	box := m.style.Panel().
		BorderForeground(th.Accent.TermColor(m.style.Mode)).
		Width(width).
		Render(lipgloss.JoinVertical(lipgloss.Left, parts...))

	frameHeight := lipgloss.Height(frame)
	top := max((frameHeight-lipgloss.Height(box))/2, 0)
	left := max((m.width-lipgloss.Width(box))/2, 0)
	return overlayAt(frame, box, top, left)
}
