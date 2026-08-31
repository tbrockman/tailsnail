// Package logring buffers log output in memory so that a TUI application can
// capture library logging without letting it corrupt the screen.
//
// tsnet is chatty by design, and every line it writes to stderr would land in
// the middle of the rendered frame. Everything is routed here instead: the ring
// keeps the most recent lines for the in-app debug view, and can optionally
// mirror them to a file for --verbose.
package logring

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// DefaultCapacity is how many lines the ring keeps by default. It is enough to
// cover a full node startup and several minutes of steady-state chatter.
const DefaultCapacity = 500

// maxLineLen truncates absurdly long log lines so one of them cannot blow out
// the debug view or the mirror file.
const maxLineLen = 2000

// Line is one captured log line.
type Line struct {
	At   time.Time
	Text string
}

// Ring is a fixed-capacity, concurrency-safe circular buffer of log lines.
// The zero value is not usable; call New.
type Ring struct {
	mu    sync.Mutex
	lines []Line
	next  int  // index of the next slot to write
	full  bool // whether the buffer has wrapped
	total int  // lines ever written, including evicted ones

	mirror *os.File
}

// New returns a ring holding up to capacity lines.
func New(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Ring{lines: make([]Line, capacity)}
}

// Logf appends a formatted line. Its signature matches tailscale's logger.Logf
// so it can be handed straight to tsnet.
func (r *Ring) Logf(format string, args ...any) {
	r.append(fmt.Sprintf(format, args...))
}

// Write implements io.Writer, splitting input on newlines. This lets the ring
// stand in for os.Stderr in anything that writes plain bytes.
func (r *Ring) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if strings.TrimSpace(line) != "" {
			r.append(line)
		}
	}
	return len(p), nil
}

// append sanitises and stores one line, evicting the oldest when full.
func (r *Ring) append(text string) {
	text = Sanitize(text)
	if text == "" {
		return
	}
	entry := Line{At: time.Now(), Text: text}

	r.mu.Lock()
	r.lines[r.next] = entry
	r.next = (r.next + 1) % len(r.lines)
	if r.next == 0 {
		r.full = true
	}
	r.total++
	mirror := r.mirror
	r.mu.Unlock()

	if mirror != nil {
		// A failed mirror write must never break the app; the ring itself is
		// still the source of truth for the debug view.
		fmt.Fprintf(mirror, "%s %s\n", entry.At.Format("15:04:05.000"), entry.Text)
	}
}

// Lines returns the buffered lines, oldest first.
func (r *Ring) Lines() []Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot()
}

// snapshot copies the ring in chronological order. The caller holds r.mu.
func (r *Ring) snapshot() []Line {
	if !r.full {
		return append([]Line(nil), r.lines[:r.next]...)
	}
	out := make([]Line, 0, len(r.lines))
	out = append(out, r.lines[r.next:]...)
	out = append(out, r.lines[:r.next]...)
	return out
}

// Tail returns the most recent n lines, oldest first.
func (r *Ring) Tail(n int) []Line {
	if n <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	all := r.snapshot()
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// Len returns the number of lines currently buffered.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return len(r.lines)
	}
	return r.next
}

// Total returns how many lines have ever been written, including any the ring
// has since evicted. The debug view shows it so a user can tell that older
// output existed.
func (r *Ring) Total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// MirrorTo also appends every line to the given file, creating it if needed.
// It is what --verbose turns on. Any previously opened mirror is closed.
func (r *Ring) MirrorTo(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logring: opening %s: %w", path, err)
	}
	r.mu.Lock()
	old := r.mirror
	r.mirror = f
	r.mu.Unlock()
	if old != nil {
		old.Close()
	}
	fmt.Fprintf(f, "\n--- tsnail started %s ---\n", time.Now().Format(time.RFC3339))
	return nil
}

// Close flushes and closes the mirror file, if any.
func (r *Ring) Close() error {
	r.mu.Lock()
	f := r.mirror
	r.mirror = nil
	r.mu.Unlock()
	if f == nil {
		return nil
	}
	return f.Close()
}

// Sanitize strips anything that could move the cursor, change colours, or
// otherwise disturb the rendered frame if a log line reaches the screen. It
// removes ANSI escape sequences and every other control character, then caps
// the length.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == utf8.RuneError && size == 1:
			// Invalid UTF-8 byte; drop it rather than emitting a stray glyph.
		case unicode.IsControl(r):
			// Newlines are split by the caller; everything else goes.
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(b.String(), " \t\r\n")
	if len([]rune(out)) > maxLineLen {
		out = string([]rune(out)[:maxLineLen]) + "…"
	}
	return out
}

// skipEscape returns the index just past the escape sequence starting at i,
// which s[i] must begin with ESC. The three shapes that matter are handled
// separately, because they have genuinely different terminators: guessing one
// rule for all of them truncates the wrong part of the line.
func skipEscape(s string, i int) int {
	i++ // consume ESC
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		// CSI: parameter and intermediate bytes, then one final byte.
		i++
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
			i++
		}
		if i < len(s) {
			i++
		}
		return i
	case ']', 'P', '^', '_':
		// OSC, DCS, PM and APC run until BEL or a string terminator (ESC \),
		// and may contain letters — which is why they cannot share the CSI rule.
		i++
		for i < len(s) {
			switch s[i] {
			case 0x07:
				return i + 1
			case 0x1b:
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i // an unterminated string; let the outer loop retry here
			}
			i++
		}
		return i
	default:
		// Two- and three-byte sequences such as ESC ( B: optional
		// intermediates followed by a single final byte.
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) {
			i++
		}
		return i
	}
}
