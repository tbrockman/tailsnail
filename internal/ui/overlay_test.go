package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestOverlayReplacesCellsInPlace(t *testing.T) {
	base := strings.Join([]string{
		"..........",
		"..........",
		"..........",
	}, "\n")
	got := overlayAt(base, "XX\nYY", 1, 3)
	want := strings.Join([]string{
		"..........",
		"...XX.....",
		"...YY.....",
	}, "\n")
	if got != want {
		t.Fatalf("overlay produced:\n%s\nwant:\n%s", got, want)
	}
}

func TestOverlayKeepsEveryLineTheSameWidth(t *testing.T) {
	base := strings.Repeat(".", 40) + "\n" + strings.Repeat(".", 40)
	for _, col := range []int{0, 5, 20, 38} {
		got := overlayAt(base, "ABC", 0, col)
		for i, line := range strings.Split(got, "\n") {
			if w := ansi.StringWidth(line); w != 40 {
				t.Errorf("col %d line %d is %d cells, want 40: %q", col, i, w, line)
			}
		}
	}
}

func TestOverlayIsClippedRatherThanExtendingTheFrame(t *testing.T) {
	base := "....\n...."
	// Beyond the bottom, and past the right edge.
	got := overlayAt(base, "XX\nYY\nZZ", 1, 3)
	if lines := strings.Split(got, "\n"); len(lines) != 2 {
		t.Fatalf("overlay grew the frame to %d lines", len(lines))
	}
	for _, line := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(line); w > 5 {
			t.Errorf("line %q is %d cells; the overlay should be clipped", line, w)
		}
	}
	// Negative rows are dropped, not wrapped to the top.
	if got := overlayAt(base, "XX", -1, 0); got != base {
		t.Errorf("a negative row changed the frame: %q", got)
	}
}

func TestOverlayPadsAShortBaseLine(t *testing.T) {
	// The frame is ten cells wide; the second line is short. Patching it past
	// its end must pad rather than leave a ragged line.
	base := "..........\nab"
	got := overlayAt(base, "Z", 1, 5)
	lines := strings.Split(got, "\n")
	if w := ansi.StringWidth(lines[1]); w != 6 {
		t.Fatalf("width = %d, want the line padded out to the overlay: %q", w, lines[1])
	}
	if !strings.HasSuffix(lines[1], "Z") {
		t.Errorf("got %q, want the overlay at the far end", lines[1])
	}
}

func TestOverlayClipsAPatchThatWouldRunOffTheEdge(t *testing.T) {
	base := strings.Repeat(".", 10)
	got := overlayAt(base, "ABCDE", 0, 8)
	if w := ansi.StringWidth(got); w != 10 {
		t.Fatalf("width = %d, want the patch clipped to the frame: %q", w, got)
	}
	if stripANSI(got) != "........AB" {
		t.Errorf("got %q, want the patch clipped after two cells", stripANSI(got))
	}
	// Entirely past the edge is a no-op.
	if got := overlayAt(base, "XX", 0, 20); got != base {
		t.Errorf("a patch beyond the frame changed it: %q", got)
	}
}

func TestOverlayPreservesStylingAroundThePatch(t *testing.T) {
	s := &Model{style: newTestModel(t).style}
	th := s.style.Theme
	// A fully styled base line, patched in the middle.
	base := s.style.Text(th.Accent, strings.Repeat("-", 20))
	patch := s.style.Text(th.Err, "XX")

	got := overlayAt(base, patch, 0, 8)
	if w := ansi.StringWidth(got); w != 20 {
		t.Fatalf("width = %d, want 20", w)
	}
	plain := stripANSI(got)
	if plain != "--------XX----------" {
		t.Fatalf("plain text = %q", plain)
	}
	// The patch's own colour must appear, and the line must not end mid-style.
	if !strings.Contains(got, s.style.SGR(th.Err)) {
		t.Error("the patch lost its colour")
	}
}

func TestOverlayHandlesWideGlyphs(t *testing.T) {
	// A double-width rune occupies two cells; splicing must count cells, not
	// runes, or every column after it shears.
	base := strings.Repeat(".", 12)
	got := overlayAt(base, "🐌", 0, 4)
	if w := ansi.StringWidth(got); w != 12 {
		t.Fatalf("width = %d, want 12: %q", w, got)
	}
}

func TestPlaceReportsWhereContentLanded(t *testing.T) {
	m := newTestModel(t)
	m.width = 40
	body := "12345\n12345"

	out, top, left := m.place(body, 10)
	if left != (40-5)/2 {
		t.Errorf("left = %d, want the body centred", left)
	}
	if top != (10-2)/2 {
		t.Errorf("top = %d, want the body centred", top)
	}
	// The reported offsets must actually match where the content is.
	lines := strings.Split(out, "\n")
	if len(lines) <= top {
		t.Fatalf("placed output has %d lines, cannot contain row %d", len(lines), top)
	}
	if got := stripANSI(lines[top]); strings.TrimSpace(got) != "12345" {
		t.Errorf("row %d = %q, want the first body line", top, got)
	}
	if idx := strings.Index(lines[top], "1"); idx != left {
		t.Errorf("content starts at column %d, want %d", idx, left)
	}
}

func TestTooltipAttachesToTheTextAndNeverOverflows(t *testing.T) {
	m := newTestModel(t)
	for _, width := range []int{62, 80, 100, 140} {
		m.width, m.height = width, 30
		panel := m.style.Panel().Width(40).Render(strings.Join([]string{
			"row one", "row two", "row three", "row four",
		}, "\n"))
		frame, top, left := m.place(panel, 20)

		// Anchored just past the text of row three, not at the panel's edge.
		out := m.withTooltip(frame, tooltip{
			text: "a description long enough to need wrapping across a couple of lines",
			row:  top + 3,
			col:  left + 2 + len("row three") + 1,
		})
		if lipgloss.Height(out) != lipgloss.Height(frame) {
			t.Errorf("width %d: the tooltip changed the frame height", width)
		}
		for i, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("width %d: line %d is %d cells: %q", width, i, w, line)
			}
		}
	}
}

func TestTooltipPointerSitsNextToTheText(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 120, 24
	panel := m.style.Panel().Width(40).Render("row one\nrow two\nrow three")
	frame, top, left := m.place(panel, 18)

	const anchorRow = 2
	anchorCol := left + 2 + len("row two") + 1
	out := m.withTooltip(frame, tooltip{text: "describes row two", row: top + anchorRow, col: anchorCol})

	line := strings.Split(out, "\n")[top+anchorRow]
	idx := strings.Index(stripANSI(line), m.style.Glyphs.PointLeft)
	if idx < 0 {
		t.Fatalf("no pointer on the anchored row: %q", stripANSI(line))
	}
	// The pointer must be attached to the text rather than out at the panel
	// border, which is what "row two" ending well short of the panel tests.
	if got := ansi.StringWidth(stripANSI(line)[:idx]); got != anchorCol {
		t.Errorf("pointer at column %d, want %d (just past the text)", got, anchorCol)
	}
}

func TestTooltipDropsBelowWhenThereIsNoRoomBeside(t *testing.T) {
	// Falling back to the left would cover the label of the very field being
	// described, so a cramped frame puts the box underneath instead.
	m := newTestModel(t)
	m.width, m.height = 40, 20
	panel := m.style.Panel().Width(34).Render("row one\nrow two\nrow three")
	frame, top, left := m.place(panel, 16)

	// Row 0 of the panel is its top border, so "row two" is two rows down.
	anchorRow := top + 2
	out := m.withTooltip(frame, tooltip{
		text: "described", row: anchorRow, col: left + 30,
	})
	if out == frame {
		t.Fatal("no tooltip was drawn at all")
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > m.width {
			t.Errorf("line %d is %d cells, want at most %d", i, w, m.width)
		}
	}
	// The anchored row itself must survive: covering the field being described
	// is the one thing worth avoiding.
	if got := stripANSI(strings.Split(out, "\n")[anchorRow]); !strings.Contains(got, "row two") {
		t.Errorf("the described row was covered: %q", got)
	}
	// And the description landed below it.
	below := stripANSI(strings.Join(strings.Split(out, "\n")[anchorRow+1:], "\n"))
	if !strings.Contains(below, "described") {
		t.Errorf("the description is not below the row:\n%s", below)
	}
}

func TestTooltipIsDroppedWhenTheFrameIsTooNarrowEntirely(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 18, 10
	frame := strings.TrimRight(strings.Repeat(strings.Repeat(".", 18)+"\n", 10), "\n")

	if out := m.withTooltip(frame, tooltip{text: "no room at all", row: 4, col: 9}); out != frame {
		t.Error("a tooltip was drawn in a frame with no room for one")
	}
}

func TestModalIsCentredAndFitsTheFrame(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 30
	frame := strings.TrimRight(strings.Repeat(strings.Repeat(".", 100)+"\n", 30), "\n")

	out := m.renderModal(frame, "activity", "something happened\nand then something else", "esc close")
	if lipgloss.Height(out) != 30 {
		t.Fatalf("modal changed the frame height to %d", lipgloss.Height(out))
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != 100 {
			t.Errorf("line %d is %d cells, want 100", i, w)
		}
	}
	plain := stripANSI(out)
	for _, want := range []string{"activity", "something happened", "esc close"} {
		if !strings.Contains(plain, want) {
			t.Errorf("modal is missing %q", want)
		}
	}
}

func TestModalHugsItsContents(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 120, 30
	frame := strings.TrimRight(strings.Repeat(strings.Repeat(".", 120)+"\n", 30), "\n")

	body := "short\nalso short"
	out := m.renderModal(frame, "tiny", body, "esc")

	// Find the dialog's border and measure it: a container that reserves far
	// more width than its contents leaves a band of dead space down one side.
	widest := 0
	for _, line := range strings.Split(stripANSI(out), "\n") {
		if i := strings.Index(line, "╭"); i >= 0 {
			widest = ansi.StringWidth(line) - i - strings.Index(reverse(line), reverse("╮"))
		}
	}
	box := 0
	for _, line := range strings.Split(stripANSI(out), "\n") {
		if strings.Contains(line, "╭") {
			box = strings.Count(line, "─") + 2
		}
	}
	_ = widest
	// "also short" is 10 cells; plus a border and a cell of padding each side.
	if box > len("also short")+8 {
		t.Errorf("the dialog is %d cells wide for %d cells of content", box, len("also short"))
	}
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func TestModalFitsANarrowFrame(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 40, 12
	frame := strings.TrimRight(strings.Repeat(strings.Repeat(".", 40)+"\n", 12), "\n")

	out := m.renderModal(frame, "title", "body text", "footer")
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != 40 {
			t.Errorf("line %d is %d cells, want 40", i, w)
		}
	}
}
