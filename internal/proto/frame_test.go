package proto

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// pipePair returns two connected Conns backed by an in-memory pipe.
func pipePair(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	a, b := net.Pipe()
	ca, cb := NewConn(a), NewConn(b)
	t.Cleanup(func() { ca.Close(); cb.Close() })
	return ca, cb
}

func TestFrameRoundTrip(t *testing.T) {
	client, server := pipePair(t)
	want := Hello{App: AppName, Version: Version, DisplayName: "ada", Intent: IntentProbe}

	go func() {
		if err := client.Send(KindHello, want); err != nil {
			t.Errorf("send: %v", err)
		}
	}()

	env, err := server.RecvTimeout(2 * time.Second)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if env.Kind != KindHello {
		t.Fatalf("kind = %q, want %q", env.Kind, KindHello)
	}
	if env.V != Version {
		t.Errorf("envelope version = %d, want %d", env.V, Version)
	}
	got, err := Decode[Hello](env)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("hello = %+v, want %+v", got, want)
	}
}

func TestMultipleFramesArriveInOrder(t *testing.T) {
	client, server := pipePair(t)
	go func() {
		for i := range 20 {
			if err := client.Send(KindInput, Input{ClientTick: i}); err != nil {
				t.Errorf("send %d: %v", i, err)
				return
			}
		}
	}()
	for i := range 20 {
		env, err := server.RecvTimeout(2 * time.Second)
		if err != nil {
			t.Fatalf("recv %d: %v", i, err)
		}
		in, err := Decode[Input](env)
		if err != nil {
			t.Fatal(err)
		}
		if in.ClientTick != i {
			t.Fatalf("frame %d carried tick %d", i, in.ClientTick)
		}
	}
}

func TestConcurrentSendersDoNotInterleaveFrames(t *testing.T) {
	client, server := pipePair(t)
	const senders, each = 4, 25
	for s := range senders {
		go func() {
			for i := range each {
				if err := client.Send(KindInput, Input{ClientTick: s*1000 + i}); err != nil {
					return
				}
			}
		}()
	}
	// Every frame must decode cleanly; a torn write would corrupt the stream.
	seen := map[int]bool{}
	for range senders * each {
		env, err := server.RecvTimeout(3 * time.Second)
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		in, err := Decode[Input](env)
		if err != nil {
			t.Fatalf("frames interleaved: %v", err)
		}
		if seen[in.ClientTick] {
			t.Fatalf("duplicate frame %d", in.ClientTick)
		}
		seen[in.ClientTick] = true
	}
}

func TestRecvReportsCleanCloseAsEOF(t *testing.T) {
	client, server := pipePair(t)
	go client.Close()
	if _, err := server.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestTruncatedBodyIsAnUnexpectedEOF(t *testing.T) {
	a, b := net.Pipe()
	server := NewConn(b)
	defer server.Close()

	go func() {
		defer a.Close()
		// Announce 64 bytes and deliver 4.
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], 64)
		a.Write(hdr[:])
		a.Write([]byte("{\"v\":"))
	}()
	if _, err := server.Recv(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestOversizedFrameAnnouncementIsRejected(t *testing.T) {
	a, b := net.Pipe()
	server := NewConn(b)
	defer server.Close()

	go func() {
		defer a.Close()
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], MaxFrame+1)
		a.Write(hdr[:])
	}()
	if _, err := server.Recv(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestZeroLengthFrameIsRejected(t *testing.T) {
	a, b := net.Pipe()
	server := NewConn(b)
	defer server.Close()

	go func() {
		defer a.Close()
		a.Write([]byte{0, 0, 0, 0})
	}()
	if _, err := server.Recv(); err == nil {
		t.Fatal("accepted a zero-length frame")
	}
}

func TestRecvTimeoutExpires(t *testing.T) {
	_, server := pipePair(t)
	start := time.Now()
	if _, err := server.RecvTimeout(50 * time.Millisecond); err == nil {
		t.Fatal("RecvTimeout returned without a message")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("RecvTimeout blocked for %v", elapsed)
	}
	// The deadline must be cleared so the connection stays usable.
	if err := server.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsAnEmptyBody(t *testing.T) {
	if _, err := Decode[Hello](Envelope{V: Version, Kind: KindHello}); err == nil {
		t.Fatal("decoded a message with no body")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	client, _ := pipePair(t)
	if err := client.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestSendRejectsAnOversizedBody(t *testing.T) {
	client, server := pipePair(t)
	go func() {
		for {
			if _, err := server.Recv(); err != nil {
				return
			}
		}
	}()
	huge := ErrorMsg{Code: "big", Message: string(make([]byte, MaxFrame+1))}
	if err := client.Send(KindError, huge); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

func TestErrorMsgImplementsError(t *testing.T) {
	var err error = ErrorMsg{Code: ErrLobbyFull, Message: "4 of 4 seats taken"}
	if got, want := err.Error(), "lobby_full: 4 of 4 seats taken"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := (ErrorMsg{Code: ErrKicked}).Error(); got != ErrKicked {
		t.Errorf("Error() = %q, want %q", got, ErrKicked)
	}
}

func TestAdvertJoinable(t *testing.T) {
	cases := []struct {
		name string
		adv  *Advert
		want bool
	}{
		{"nil", nil, false},
		{"open with room", &Advert{Phase: PhaseOpen, Seats: 4, Taken: 2}, true},
		{"open but full", &Advert{Phase: PhaseOpen, Seats: 4, Taken: 4}, false},
		{"in game", &Advert{Phase: PhaseInGame, Seats: 4, Taken: 2}, false},
		{"closed", &Advert{Phase: PhaseClosed, Seats: 4, Taken: 0}, false},
	}
	for _, tc := range cases {
		if got := tc.adv.Joinable(); got != tc.want {
			t.Errorf("%s: Joinable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHandshakeCompatibility(t *testing.T) {
	if !(Hello{App: AppName, Version: Version}).Compatible() {
		t.Error("a matching hello was rejected")
	}
	if (Hello{App: "nethack", Version: Version}).Compatible() {
		t.Error("accepted a hello from another application")
	}
	if (HelloOK{App: AppName, Version: Version + 1}).Compatible() {
		t.Error("accepted a hello_ok from a future protocol version")
	}
}
