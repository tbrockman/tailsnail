package tsnode

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"tailscale.com/health"
	"tailscale.com/ipn"
)

func TestSanitizeHostname(t *testing.T) {
	cases := []struct{ in, want string }{
		{"laptop", "laptop"},
		{"LAPTOP", "laptop"},
		{"Ada's Laptop", "adas-laptop"}, // dropped, not treated as a separator
		{"tsnail-my.machine", "tsnail-my-machine"},
		{"  spaced  out  ", "spaced-out"},
		{"under_scores", "under-scores"},
		{"--leading-and-trailing--", "leading-and-trailing"},
		{"dots...everywhere", "dots-everywhere"},
		{"emoji-\U0001F40C-snail", "emoji-snail"},
		{"", ""},
		{"!!!", ""},
		{"123", "123"},
		{strings.Repeat("a", 80), strings.Repeat("a", 63)},
	}
	for _, tc := range cases {
		if got := SanitizeHostname(tc.in); got != tc.want {
			t.Errorf("SanitizeHostname(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeHostnameProducesValidDNSLabels(t *testing.T) {
	inputs := []string{"Ada's Laptop", "a.b.c.d", "___", "x" + strings.Repeat("-", 70) + "y", strings.Repeat("q-", 60)}
	for _, in := range inputs {
		got := SanitizeHostname(in)
		if got == "" {
			continue // an input that reduces to nothing is handled by the caller
		}
		if len(got) > 63 {
			t.Errorf("SanitizeHostname(%q) = %q, longer than a DNS label allows", in, got)
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("SanitizeHostname(%q) = %q, has a leading or trailing hyphen", in, got)
		}
		for _, r := range got {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				t.Errorf("SanitizeHostname(%q) = %q, contains %q", in, got, r)
				break
			}
		}
	}
}

func TestDefaultHostname(t *testing.T) {
	cases := []struct{ in, want string }{
		{"laptop", "tsnail-laptop"},
		{"laptop.local", "tsnail-laptop"},
		{"Ada-MBP.lan", "tsnail-ada-mbp"},
		{"", "tsnail-node"},
		{"!!!", "tsnail-node"},
	}
	for _, tc := range cases {
		if got := DefaultHostname(tc.in); got != tc.want {
			t.Errorf("DefaultHostname(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPhaseMapping(t *testing.T) {
	cases := []struct {
		state ipn.State
		want  Phase
	}{
		{ipn.NoState, PhaseStarting},
		{ipn.NeedsLogin, PhaseNeedsLogin},
		{ipn.NeedsMachineAuth, PhaseNeedsApproval},
		{ipn.Starting, PhaseConnecting},
		{ipn.Running, PhaseRunning},
		{ipn.Stopped, PhaseStopped},
		{ipn.InUseOtherUser, PhaseFailed},
	}
	for _, tc := range cases {
		if got := phaseFor(tc.state); got != tc.want {
			t.Errorf("phaseFor(%v) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestPhaseStringsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for p := PhaseStarting; p <= PhaseFailed; p++ {
		s := p.String()
		if s == "unknown" {
			t.Errorf("phase %d has no name", p)
		}
		if seen[s] {
			t.Errorf("phase name %q used twice", s)
		}
		seen[s] = true
	}
}

func TestSelfShortStripsTheTailnetSuffix(t *testing.T) {
	cases := []struct {
		self Self
		want string
	}{
		{Self{DNSName: "tsnail-laptop.tail1234.ts.net"}, "tsnail-laptop"},
		{Self{DNSName: "tsnail-laptop"}, "tsnail-laptop"},
		{Self{Hostname: "fallback"}, "fallback"},
		{Self{}, ""},
	}
	for _, tc := range cases {
		if got := tc.self.Short(); got != tc.want {
			t.Errorf("Self%+v.Short() = %q, want %q", tc.self, got, tc.want)
		}
	}
}

func TestHealthWarningsAreFilteredAndStable(t *testing.T) {
	state := &health.State{Warnings: map[health.WarnableCode]health.UnhealthyState{
		"zebra":   {Title: "Zebra", Text: "no internet connection", ImpactsConnectivity: true},
		"alpha":   {Title: "Alpha", Text: "DERP unreachable", ImpactsConnectivity: true},
		"chatter": {Title: "Chatter", Text: "an update is available", Severity: health.SeverityLow},
	}}
	want := []string{"DERP unreachable", "no internet connection"}
	// Run repeatedly: map iteration order varies, and the UI must not flicker.
	for range 20 {
		if got := healthWarnings(state); !reflect.DeepEqual(got, want) {
			t.Fatalf("healthWarnings = %v, want %v", got, want)
		}
	}
	if got := healthWarnings(nil); got != nil {
		t.Errorf("healthWarnings(nil) = %v, want nil", got)
	}
	if got := healthWarnings(&health.State{}); got != nil {
		t.Errorf("healthWarnings(empty) = %v, want nil", got)
	}
}

func TestHealthWarningsFallBackToTheTitle(t *testing.T) {
	state := &health.State{Warnings: map[health.WarnableCode]health.UnhealthyState{
		"a": {Title: "Only a title", ImpactsConnectivity: true},
	}}
	if got, want := healthWarnings(state), []string{"Only a title"}; !reflect.DeepEqual(got, want) {
		t.Errorf("healthWarnings = %v, want %v", got, want)
	}
}

func TestHealthWarningTextIsSanitized(t *testing.T) {
	state := &health.State{Warnings: map[health.WarnableCode]health.UnhealthyState{
		"a": {Text: "danger \x1b[31mred\x1b[0m\x07", ImpactsConnectivity: true},
	}}
	got := healthWarnings(state)
	if len(got) != 1 {
		t.Fatalf("healthWarnings = %v", got)
	}
	if strings.ContainsRune(got[0], 0x1b) || strings.ContainsRune(got[0], 0x07) {
		t.Errorf("warning %q still carries control characters", got[0])
	}
}

func TestEqualStrings(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{}, nil, true},
		{[]string{"a"}, []string{"a"}, true},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a"}, []string{"a", "b"}, false},
	}
	for _, tc := range cases {
		if got := equalStrings(tc.a, tc.b); got != tc.want {
			t.Errorf("equalStrings(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestStartRejectsAnEmptyStateDir(t *testing.T) {
	if _, err := Start(context.Background(), Options{Hostname: "x"}); err == nil {
		t.Fatal("Start accepted an empty state directory")
	}
}

func TestStartRejectsAnUnusableHostname(t *testing.T) {
	_, err := Start(context.Background(), Options{StateDir: t.TempDir(), Hostname: "!!!"})
	if err == nil {
		t.Fatal("Start accepted a hostname that sanitises to nothing")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("error = %v, want it to mention the hostname", err)
	}
}

func TestOpenURLRejectsNonHTTPSchemes(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "javascript:alert(1)", "ssh://host"} {
		if err := OpenURL(context.Background(), u); err == nil {
			t.Errorf("OpenURL(%q) was allowed", u)
		}
	}
}

func TestOpenURLRejectsMalformedInput(t *testing.T) {
	if err := OpenURL(context.Background(), "://not a url"); err == nil {
		t.Error("OpenURL accepted malformed input")
	}
}

func TestPublishAlwaysDeliversTheLatestStatus(t *testing.T) {
	n := &Node{updates: make(chan Status, 2)}
	// Publish more statuses than the channel can hold.
	for i := range 10 {
		n.publish(Status{Phase: PhaseConnecting, Self: Self{Hostname: string(rune('a' + i))}})
	}
	final := Status{Phase: PhaseRunning, Self: Self{Hostname: "final"}}
	n.publish(final)

	// Drain: the newest status must be in there, and Status() must agree.
	var last Status
	for {
		select {
		case s := <-n.updates:
			last = s
			continue
		default:
		}
		break
	}
	if last.Phase != PhaseRunning || last.Self.Hostname != "final" {
		t.Fatalf("last delivered status = %+v, want the final one", last)
	}
	if got := n.Status(); got.Phase != PhaseRunning {
		t.Fatalf("Status() = %v, want the latest published phase", got.Phase)
	}
}

func TestSleepReturnsFalseOnceClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{ctx: ctx, cancel: cancel}
	cancel()
	if n.sleep(time.Second) {
		t.Fatal("sleep reported success after the node was closed")
	}
}
