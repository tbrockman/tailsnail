package logring

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

func TestLogfBuffersLines(t *testing.T) {
	r := New(10)
	r.Logf("hello %s", "world")
	r.Logf("second")

	got := texts(r.Lines())
	if len(got) != 2 || got[0] != "hello world" || got[1] != "second" {
		t.Fatalf("lines = %v", got)
	}
	if r.Len() != 2 {
		t.Errorf("Len() = %d, want 2", r.Len())
	}
}

func TestRingEvictsOldestLines(t *testing.T) {
	r := New(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		r.Logf("%s", s)
	}
	got := texts(r.Lines())
	want := []string{"c", "d", "e"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	if r.Len() != 3 {
		t.Errorf("Len() = %d, want the capacity 3", r.Len())
	}
	if r.Total() != 5 {
		t.Errorf("Total() = %d, want 5 including evicted lines", r.Total())
	}
}

func TestLinesStayInChronologicalOrderAfterWrapping(t *testing.T) {
	r := New(4)
	for i := range 11 {
		r.Logf("line-%02d", i)
	}
	got := texts(r.Lines())
	want := []string{"line-07", "line-08", "line-09", "line-10"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

func TestTail(t *testing.T) {
	r := New(10)
	for i := range 6 {
		r.Logf("line-%d", i)
	}
	if got := texts(r.Tail(2)); strings.Join(got, ",") != "line-4,line-5" {
		t.Errorf("Tail(2) = %v", got)
	}
	if got := r.Tail(100); len(got) != 6 {
		t.Errorf("Tail(100) returned %d lines, want all 6", len(got))
	}
	if got := r.Tail(0); got != nil {
		t.Errorf("Tail(0) = %v, want nil", got)
	}
	if got := r.Tail(-1); got != nil {
		t.Errorf("Tail(-1) = %v, want nil", got)
	}
}

func TestWriteSplitsOnNewlines(t *testing.T) {
	r := New(10)
	n, err := r.Write([]byte("one\ntwo\n\nthree\n"))
	if err != nil {
		t.Fatal(err)
	}
	if n != len("one\ntwo\n\nthree\n") {
		t.Errorf("Write returned %d, want the full input length", n)
	}
	got := texts(r.Lines())
	if strings.Join(got, ",") != "one,two,three" {
		t.Fatalf("lines = %v, want blank lines dropped", got)
	}
}

func TestSanitizeStripsEscapeSequences(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"bell\x07here", "bellhere"},
		{"carriage\rreturn", "carriagereturn"},
		{"back\x08space", "backspace"},
		{"tab\there", "tab    here"},
		{"\x1b]0;window title\x07after", "after"},
		{"trailing   ", "trailing"},
		{"", ""},
		{"\x1b[2J\x1b[H", ""},
	}
	for _, tc := range cases {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeLeavesNoControlCharacters(t *testing.T) {
	nasty := "\x1b[1;31mALERT\x1b[0m\x00\x07\x1b[2J\rreset\x1b(Bdone"
	got := Sanitize(nasty)
	for _, r := range got {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("Sanitize(%q) = %q, still contains control character %q", nasty, got, r)
		}
	}
}

func TestSanitizePreservesNonASCIIText(t *testing.T) {
	// Box-drawing characters and accents must survive: the debug view shows
	// the same glyph set the rest of the UI uses.
	in := "peer ─── café ▲ 蛇"
	if got := Sanitize(in); got != in {
		t.Errorf("Sanitize(%q) = %q", in, got)
	}
}

func TestSanitizeTruncatesVeryLongLines(t *testing.T) {
	got := Sanitize(strings.Repeat("x", maxLineLen*2))
	if len([]rune(got)) > maxLineLen+1 {
		t.Fatalf("Sanitize produced a %d-rune line, want it capped near %d", len([]rune(got)), maxLineLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a truncated line should be marked with an ellipsis")
	}
}

func TestLoggedLinesAreSanitized(t *testing.T) {
	r := New(10)
	r.Logf("tsnet: \x1b[31mconnecting\x1b[0m")
	got := texts(r.Lines())
	if len(got) != 1 || got[0] != "tsnet: connecting" {
		t.Fatalf("lines = %q", got)
	}
}

func TestEmptyLinesAreNotBuffered(t *testing.T) {
	r := New(10)
	r.Logf("   ")
	r.Logf("\x1b[0m")
	if r.Len() != 0 {
		t.Fatalf("Len() = %d, want 0: lines that sanitise to nothing are noise", r.Len())
	}
}

func TestMirrorWritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tsnail.log")
	r := New(10)
	if err := r.MirrorTo(path); err != nil {
		t.Fatal(err)
	}
	r.Logf("mirrored line")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mirrored line") {
		t.Fatalf("log file does not contain the line:\n%s", raw)
	}
	if !strings.Contains(string(raw), "tsnail started") {
		t.Error("log file is missing the session header")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("log file mode = %o, want no group or world access", perm)
	}
}

func TestMirrorAppendsAcrossSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tsnail.log")
	for _, msg := range []string{"first session", "second session"} {
		r := New(10)
		if err := r.MirrorTo(path); err != nil {
			t.Fatal(err)
		}
		r.Logf("%s", msg)
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"first session", "second session"} {
		if !strings.Contains(string(raw), msg) {
			t.Errorf("log file lost %q:\n%s", msg, raw)
		}
	}
}

func TestMirrorToAnUnwritablePathIsReported(t *testing.T) {
	r := New(10)
	if err := r.MirrorTo(filepath.Join(t.TempDir(), "nope", "deeper", "x.log")); err == nil {
		t.Fatal("MirrorTo succeeded for a path whose parent does not exist")
	}
	// The ring must still work without a mirror.
	r.Logf("still logging")
	if r.Len() != 1 {
		t.Error("the ring stopped buffering after a mirror failure")
	}
}

func TestCloseWithoutAMirrorIsFine(t *testing.T) {
	r := New(4)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestConcurrentWritersAreSafe(t *testing.T) {
	r := New(64)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				r.Logf("writer-%d line-%d", w, i)
			}
		}()
	}
	// Read concurrently too, which is what the debug view does.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			r.Lines()
			r.Tail(10)
			r.Len()
		}
	}()
	wg.Wait()

	if got := r.Total(); got != 800 {
		t.Errorf("Total() = %d, want 800", got)
	}
	if got := r.Len(); got != 64 {
		t.Errorf("Len() = %d, want the capacity 64", got)
	}
}

func TestNewClampsAnInvalidCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		r := New(capacity)
		r.Logf("x")
		if r.Len() != 1 {
			t.Fatalf("New(%d) produced an unusable ring", capacity)
		}
	}
}
