package termkeys

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
)

// readAll drains the reader until EOF. The buffer is large on purpose:
// readAll is not what models a split read (the chunkReaders below are),
// and a small buffer here would cut the stream at a size nobody chose.
func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			return out.Bytes()
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
}

func translate(t *testing.T, in string) string {
	t.Helper()
	return string(readAll(t, New(bytes.NewBufferString(in))))
}

// TestShiftEnterBecomesNewline pins THE contract: shift+enter under
// kitty's extended reporting becomes the ctrl+j byte the compose
// newline binding matches, while plain enter stays a submit (\r).
func TestShiftEnterBecomesNewline(t *testing.T) {
	if got := translate(t, "before\x1b[13;130uafter"); got != "before\nafter" {
		t.Fatalf("shift+enter translation = %q, want %q", got, "before\nafter")
	}
	if got := translate(t, "\x1b[13;1u"); got != "\r" {
		t.Fatalf("plain enter translation = %q, want \\r", got)
	}
	if got := translate(t, "\x1b[13u"); got != "\r" {
		t.Fatalf("enter without modifier parameter = %q, want \\r", got)
	}
}

// TestPlainTextKeysRoundTrip pins the extended-reporting consequence:
// EVERY printable key arrives as CSI-u and must translate back to its
// byte. kitty reports the BASE codepoint for shifted keys with the
// shifted character as the embedded text, and includes lock bits in
// the modifier value.
func TestPlainTextKeysRoundTrip(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"\x1b[105;1u", "i"},
		{"\x1b[105;129u", "i"},    // num-lock held (state, not chord)
		{"\x1b[73;2u", "I"},       // shift+i via shifted codepoint
		{"\x1b[108;130;76u", "L"}, // shift+l via embedded text (kitty 0.48.2)
		{"\x1b[49;130;33u", "!"},  // shift+1 via embedded text
		{"\x1b[32;1u", " "},       // space
		{"\x1b[97;5u", "\x01"},    // ctrl+a
		{"\x1b[99;5u", "\x03"},    // ctrl+c
		{"\x1b[32;5u", "\x00"},    // ctrl+space
		{"\x1b[120;3u", "\x1bx"},  // alt+x
		{"\x1b[9;1u", "\t"},       // tab
		{"\x1b[9;2u", "\x1b[Z"},   // shift+tab
		{"\x1b[127;1u", "\x7f"},   // backspace
		{"\x1b[27;1u", "\x1b"},    // escape
		{"\x1b[13;3u", "\x1b\r"},  // alt+enter
		{"\x1b[13;129u", "\r"},    // enter with num-lock
	}
	for _, c := range cases {
		if got := translate(t, c.in); got != c.want {
			t.Fatalf("translate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFunctionalKeysRoundTrip pins the navigation keys in kitty
// 0.48.2's actual wire form: letter/tilde trailers with code 1, and
// lock bits in the modifier. Legacy xterm forms are idempotent.
func TestFunctionalKeysRoundTrip(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"\x1b[1;129A", "\x1b[A"},    // up (num-lock held)
		{"\x1b[1;130A", "\x1b[1;2A"}, // shift+up
		{"\x1b[1;129B", "\x1b[B"},    // down
		{"\x1b[1;129D", "\x1b[D"},    // left
		{"\x1b[1;129C", "\x1b[C"},    // right
		{"\x1b[1;129H", "\x1b[H"},    // home
		{"\x1b[1;129F", "\x1b[F"},    // end
		{"\x1b[5;129~", "\x1b[5~"},   // page up
		{"\x1b[6;129~", "\x1b[6~"},   // page down
		{"\x1b[2;129~", "\x1b[2~"},   // insert
		{"\x1b[3;129~", "\x1b[3~"},   // delete
		{"\x1b[1;129P", "\x1bOP"},    // F1
		{"\x1b[1;130P", "\x1b[1;2P"}, // shift+F1
		{"\x1b[13;129~", "\x1bOR"},   // F3
		{"\x1b[15;129~", "\x1b[15~"}, // F5
		{"\x1b[24;129~", "\x1b[24~"}, // F12
		{"\x1b[1;2A", "\x1b[1;2A"},   // legacy xterm shift+up: idempotent
	}
	for _, c := range cases {
		if got := translate(t, c.in); got != c.want {
			t.Fatalf("translate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLegacySequencesPassThrough pins the passthrough contract:
// sequences bubbletea already understands are byte-identical, and
// modifier-only key reports (which bubbletea drops) are untouched.
func TestLegacySequencesPassThrough(t *testing.T) {
	cases := []string{
		"plain text\r\n",
		"\x1b[200~",       // bracketed paste start
		"\x1b[201~",       // bracketed paste end
		"\x1b[A",          // plain legacy arrow
		"\x1bx",           // alt+x in legacy form
		"\x1b",            // lone escape at EOF
		"\x1b[57447;130u", // right-shift key report: not a chord
	}
	for _, c := range cases {
		if got := translate(t, c); got != c {
			t.Fatalf("translate(%q) = %q, want %q", c, got, c)
		}
	}
}

// TestSequenceSplitAcrossReads pins the state machine: a CSI sequence
// split between reads (around surrounding text) still translates.
func TestSequenceSplitAcrossReads(t *testing.T) {
	r := New(&chunkReader{data: []byte("ab\x1b[13;130ucd"), size: 6})
	if got := string(readAll(t, r)); got != "ab\ncd" {
		t.Fatalf("split translation = %q", got)
	}
}

// TestLoneEscapeIsFlushedImmediately pins the modal-TUI contract: a
// lone ESC passes through as the escape key immediately, never held
// (the live "esc dead, stuck in insert mode" bug).
func TestLoneEscapeIsFlushedImmediately(t *testing.T) {
	r := New(&chunkReader{data: []byte("ab\x1bx"), size: 1})
	if got := string(readAll(t, r)); got != "ab\x1bx" {
		t.Fatalf("escape handling = %q", got)
	}
}

// TestDanglingSequenceAtEOFFlushesVerbatim pins the shutdown path.
func TestDanglingSequenceAtEOFFlushesVerbatim(t *testing.T) {
	in := "\x1b[13;2"
	if got := translate(t, in); got != in {
		t.Fatalf("dangling sequence was not flushed verbatim: %q", got)
	}
}

// TestReaderSatisfiesTermFile pins the raw-mode contract: the wrapper
// must present itself as a terminal file, or bubbletea never enables
// raw mode.
func TestReaderSatisfiesTermFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "termkeys-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()
	r := New(f)
	var _ term.File = r
	if r.Fd() == 0 {
		t.Fatal("a wrapped file reader must report the file's descriptor")
	}
	if _, err := r.Write([]byte("x")); err != nil {
		t.Fatalf("write passthrough: %v", err)
	}
}

// chunkReader serves data in fixed-size chunks, for exercising partial
// sequences across Read boundaries.
type chunkReader struct {
	data []byte
	size int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.size
	if n > len(c.data) {
		n = len(c.data)
	}
	// A reader never hands back more than the caller's buffer: this fake
	// split the stream to exercise the state machine, not to model a
	// terminal that overfills a read.
	if n > len(p) {
		n = len(p)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

// TestFullReadHoldsATrailingEsc pins the boundary rule for a read that
// filled the caller's buffer. With the kitty protocol pushed every key is
// a CSI sequence, so a buffer edge can land right after a sequence's ESC;
// treating that as the escape key delivered a stray Escape mid-typing
// (which leaves insert mode, so the keys behind it ran as commands) and
// then the rest of the sequence as literal text.
//
// The input is the reviewer's probe: 5 plain bytes then 51 five-byte key
// reports, 260 bytes, so a 256-byte read ends exactly on the 51st ESC.
func TestFullReadHoldsATrailingEsc(t *testing.T) {
	in := []byte("abcde")
	for i := 0; i < 51; i++ {
		in = append(in, []byte("\x1b[97u")...)
	}

	r := New(bytes.NewReader(in))
	var out bytes.Buffer
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}

	want := "abcde" + strings.Repeat("a", 51)
	if got := out.String(); got != want {
		t.Fatalf("a full read split a key sequence:\ngot  %q\nwant %q", got, want)
	}
}

// TestAPartialEscapeRunDoesNotBlockTheRead pins the other half of the
// same rule: when a chunk is nothing but an incomplete sequence, Read must
// hand control back rather than read again. In the TUI the caller waits on
// the terminal before every read, so that wait is cancellable; a read
// blocked here is not, and would survive the cancel bubbletea does before
// it starts an editor (macula-lazymesh#60).
func TestAPartialEscapeRunDoesNotBlockTheRead(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	in := New(r)

	done := make(chan int, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := in.Read(buf)
		done <- n
	}()

	if _, err := w.Write([]byte("\x1b[")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case n := <-done:
		if n != 0 {
			t.Fatalf("an incomplete sequence produced %d bytes, want 0", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read blocked on a second read instead of returning the incomplete sequence to the caller")
	}

	// The held run is completed by the next bytes: CSI A is the up arrow,
	// which the caller must still receive as the legacy form.
	if _, err := w.Write([]byte("A")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 16)
	n, err := in.Read(buf)
	if err != nil {
		t.Fatalf("read the completed sequence: %v", err)
	}
	if got := string(buf[:n]); got != "\x1b[A" {
		t.Fatalf("completed sequence = %q, want %q", got, "\x1b[A")
	}
}
