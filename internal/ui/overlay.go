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

// naturalWidth returns the widest of the given rendered rows, in display
// cells. Panels are sized from it so a container hugs its contents instead of
// trailing a band of empty space down its right-hand side.
func naturalWidth(rows []string) int {
	w := 0
	for _, r := range rows {
		w = max(w, ansi.StringWidth(r))
	}
	return w
}

// tooltip is a description anchored to a specific point in the frame.
type tooltip struct {
	text string
	// row is the frame row of the text being described, and col the column
	// just past its last character. The popover attaches there rather than to
	// the container's edge, the way a tooltip attaches to the element it
	// belongs to and not to the page.
	row, col int
}

// withTooltip composites a description box attached to the text it describes.
//
// Descriptions are drawn as a popover rather than inline underneath the
// selected row: an inline description changes the panel's height as the
// selection moves, so every row below it shifts by a line on each keypress,
// which makes a list unreadable to scan. Overlapping whatever is beneath is
// deliberate and expected.
func (m *Model) withTooltip(frame string, tip tooltip) string {
	if tip.text == "" {
		return frame
	}
	th := m.style.Theme
	g := m.style.Glyphs

	lines := strings.Split(frame, "\n")
	frameWidth := naturalWidth(lines)

	const chrome = 4 // the box's own border and padding
	const gap = 1    // the pointer
	const preferred = 30
	const minimum = 14

	// Wrap at the available width, then size the box to what the text actually
	// came out as, so a short description does not sit in a wide empty box.
	render := func(width int) string {
		wrapped := lipgloss.NewStyle().Width(width).Render(m.style.Text(th.Dim, tip.text))
		hug := naturalWidth(strings.Split(wrapped, "\n"))
		return lipgloss.NewStyle().
			Border(m.tooltipBorder()).
			BorderForeground(th.Accent2.TermColor(m.style.Mode)).
			Padding(0, 1).
			Width(hug + 2).
			Render(wrapped)
	}

	// Beside the text is the natural place for a description, so try that
	// first. Falling back to the left would cover the label of the very field
	// being described, so when there is no room to the right the box drops
	// underneath instead — the same thing a tooltip does on a page.
	if width := min(frameWidth-tip.col-gap-chrome, preferred); width >= minimum {
		box := render(width)
		top := min(max(tip.row-lipgloss.Height(box)/2, 0), max(len(lines)-lipgloss.Height(box), 0))
		frame = overlayAt(frame, box, top, tip.col+gap)
		return overlayAt(frame, m.style.Text(th.Accent2, g.PointLeft), tip.row, tip.col)
	}

	width := min(frameWidth-chrome-2, preferred)
	if width < minimum {
		return frame
	}
	box := render(width)
	boxWidth, boxHeight := lipgloss.Width(box), lipgloss.Height(box)

	// Prefer below; go above when the bottom of the frame is too close.
	top := tip.row + 1
	pointer := g.PointUp
	pointerRow := top
	if top+boxHeight > len(lines) {
		top = tip.row - boxHeight
		pointerRow = top + boxHeight - 1
	}
	if top < 0 {
		return frame
	}
	// Keep the box in frame, then notch its border under the text it belongs to.
	left := min(max(tip.col-2, 0), max(frameWidth-boxWidth, 0))

	frame = overlayAt(frame, box, top, left)
	if tip.col > left && tip.col < left+boxWidth-1 {
		frame = overlayAt(frame, m.style.Text(th.Accent2, pointer), pointerRow, tip.col)
	}
	return frame
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

// renderModal draws a bordered dialog centred over the current frame, sized to
// its contents rather than to a fixed width.
func (m *Model) renderModal(frame, title, body, footer string) string {
	th := m.style.Theme

	parts := []string{m.style.Bold(title), ""}
	parts = append(parts, body)
	if footer != "" {
		parts = append(parts, "", m.style.FaintText(footer))
	}

	// Measure every line the dialog will hold, including its title and footer,
	// so the box hugs the widest of them.
	var measured []string
	for _, p := range parts {
		measured = append(measured, strings.Split(p, "\n")...)
	}
	width := min(naturalWidth(measured), max(m.width-6, 20))

	box := m.style.Panel().
		BorderForeground(th.Accent.TermColor(m.style.Mode)).
		// lipgloss counts padding inside Width, so hold `width` cells of
		// content by asking for two more than that.
		Width(width + 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, parts...))

	frameHeight := lipgloss.Height(frame)
	top := max((frameHeight-lipgloss.Height(box))/2, 0)
	left := max((m.width-lipgloss.Width(box))/2, 0)
	return overlayAt(frame, box, top, left)
}
