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

// placement is where a popover sits relative to the text it describes.
type placement int

const (
	// placeRight is the default: beside the text, reading on from it.
	placeRight placement = iota
	// placeBelow hangs the box under the text, notched into its top border.
	placeBelow
	placeAbove
	placeLeft
)

// tooltip is a description anchored to a specific point in the frame.
type tooltip struct {
	text string
	// row is the frame row of the text being described, and col the column it
	// attaches at: just past the text for a placement beside it, or the point
	// the notch marks for one above or below. The popover attaches to the text
	// rather than to the container's edge, the way a tooltip attaches to the
	// element it belongs to and not to the page.
	//
	// A full-width table row has no meaningful "end", so those anchor at the
	// column their content starts in, and the box hangs beneath aligned to it.
	row, col int
	// prefer is where to try first. Whatever is asked for, the box falls back
	// through the other placements until one fits the space actually
	// available, so a popover never simply disappears because the window is an
	// awkward shape.
	prefer placement
}

// Popover sizing. The box is never narrower than minimum — below that a
// description wraps into unreadable slivers — and never wider than maximum,
// which is about as long a line as stays comfortable to read.
const (
	tipChrome  = 4 // the box's own border and padding
	tipGap     = 1 // the pointer
	tipMaximum = 46
	tipMinimum = 14
)

// fallbackOrder returns the placements to try, starting from the preferred
// one. Left is always last: it is the only placement that covers the text
// being described, which is the one thing worth avoiding.
func fallbackOrder(prefer placement) []placement {
	switch prefer {
	case placeBelow:
		return []placement{placeBelow, placeAbove, placeRight, placeLeft}
	case placeAbove:
		return []placement{placeAbove, placeBelow, placeRight, placeLeft}
	default:
		return []placement{placeRight, placeBelow, placeAbove, placeLeft}
	}
}

// withTooltip composites a description box attached to the text it describes,
// trying each placement in turn until one fits.
//
// Descriptions are drawn as a popover rather than inline underneath the
// selected row: an inline description changes the container's height as the
// selection moves, so every row below it shifts by a line on each keypress,
// which makes a list unreadable to scan. Overlapping whatever is beneath is
// deliberate and expected.
func (m *Model) withTooltip(frame string, tip tooltip) string {
	if tip.text == "" {
		return frame
	}
	lines := strings.Split(frame, "\n")
	frameWidth := naturalWidth(lines)

	for _, p := range fallbackOrder(tip.prefer) {
		if out, ok := m.placeTooltip(frame, lines, frameWidth, tip, p); ok {
			return out
		}
	}
	return frame
}

// renderTooltip builds the box, wrapping at width and then shrinking to hug
// the text so a short description does not sit in a wide empty box.
func (m *Model) renderTooltip(text string, width int) string {
	th := m.style.Theme
	wrapped := lipgloss.NewStyle().Width(width).Render(m.style.Text(th.Dim, text))
	hug := naturalWidth(strings.Split(wrapped, "\n"))
	return lipgloss.NewStyle().
		Border(m.tooltipBorder()).
		BorderForeground(th.Accent2.TermColor(m.style.Mode)).
		Padding(0, 1).
		Width(hug + 2).
		Render(wrapped)
}

// placeTooltip attempts one placement, reporting whether it fitted.
func (m *Model) placeTooltip(frame string, lines []string, frameWidth int, tip tooltip, p placement) (string, bool) {
	g := m.style.Glyphs
	height := len(lines)

	var budget int
	switch p {
	case placeRight:
		budget = frameWidth - tip.col - tipGap - tipChrome
	case placeLeft:
		budget = tip.col - tipGap - tipChrome
	default:
		budget = frameWidth - tipChrome - 2
	}
	// Take only as much width as the text actually wants. Sizing every
	// popover to a fixed figure wrapped short text that had room to sit on
	// one line.
	want := naturalWidth(strings.Split(tip.text, "\n"))
	width := min(budget, min(max(want, tipMinimum), tipMaximum))
	if width < tipMinimum {
		return "", false
	}

	box := m.renderTooltip(tip.text, width)
	boxWidth, boxHeight := lipgloss.Width(box), lipgloss.Height(box)
	point := func(glyph string) string { return m.style.Text(m.style.Theme.Accent2, glyph) }

	switch p {
	case placeRight, placeLeft:
		if boxHeight > height {
			return "", false
		}
		// Centre the box on the row it belongs to, then pull it back inside
		// the frame if that would push it off the top or bottom.
		top := min(max(tip.row-boxHeight/2, 0), height-boxHeight)
		col, pointerCol, glyph := tip.col+tipGap, tip.col, g.PointLeft
		if p == placeLeft {
			col, pointerCol, glyph = tip.col-tipGap-boxWidth, tip.col-tipGap, g.PointRight
		}
		if col < 0 || col+boxWidth > frameWidth {
			return "", false
		}
		out := overlayAt(frame, box, top, col)
		return overlayAt(out, point(glyph), tip.row, pointerCol), true

	default: // placeBelow, placeAbove
		top, pointerRow, glyph := tip.row+1, tip.row+1, g.PointUp
		if p == placeAbove {
			top = tip.row - boxHeight
			pointerRow, glyph = tip.row-1, g.PointDown
		}
		if top < 0 || top+boxHeight > height {
			return "", false
		}
		// Keep the box in frame, then notch its border under the text.
		left := min(max(tip.col-2, 0), max(frameWidth-boxWidth, 0))
		out := overlayAt(frame, box, top, left)
		if tip.col > left && tip.col < left+boxWidth-1 {
			out = overlayAt(out, point(glyph), pointerRow, tip.col)
		}
		return out, true
	}
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
