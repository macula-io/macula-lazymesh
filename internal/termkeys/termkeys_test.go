package termkeys

import (
	"bytes"
	"io"
	"testing"
)

// readAll drains the reader until EOF.
func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, 3) // deliberately small: exercises the internal buffering
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

// TestShiftEnterBecomesNewline pins the one translation that matters:
// CSI 13;2u (shift+enter under the kitty protocol) arrives as the
// ctrl+j byte the compose newline binding matches.
func TestShiftEnterBecomesNewline(t *testing.T) {
	got := translate(t, "before\x1b[13;2uafter")
	if got != "before\nafter" {
		t.Fatalf("translation = %q, want %q", got, "before\nafter")
	}
}

// TestAltEnterPassesThrough pins the boundary: alt+enter (mods=3) is not
// shift+enter and passes through untouched (bubbletea drops it as an
// unknown CSI, same as today).
func TestAltEnterPassesThrough(t *testing.T) {
	in := "\x1b[13;3u"
	if got := translate(t, in); got != in {
		t.Fatalf("alt+enter was rewritten: %q", got)
	}
}

// TestLegacySequencesPassThrough pins the non-regression contract:
// everything bubbletea already understands (plain enter, legacy
// modified arrows, alt+key, plain text) is byte-identical.
func TestLegacySequencesPassThrough(t *testing.T) {
	cases := []string{
		"plain text\r\n",
		"\x1b[A\x1b[B",       // plain arrows
		"\x1b[1;2A\x1b[1;5D", // legacy shift/ctrl arrows (bubbletea parses these)
		"\x1bx",              // alt+x
		"\x1b",               // lone escape at EOF
	}
	for _, c := range cases {
		if got := translate(t, c); got != c {
			t.Fatalf("translate(%q) = %q, want %q", c, got, c)
		}
	}
}

// TestMixedContentTranslatesOnlyShiftEnter pins the selectivity: a
// chunk holding text, a legacy sequence and a shift+enter rewrites
// exactly the shift+enter.
func TestMixedContentTranslatesOnlyShiftEnter(t *testing.T) {
	in := "text\x1b[A\x1b[13;2umore"
	if got := translate(t, in); got != "text\x1b[A\nmore" {
		t.Fatalf("mixed translation = %q", got)
	}
}

// TestSequenceSplitAcrossReads pins the state machine: the CSI-u
// sequence arriving across Read boundaries (split between the
// surrounding text, not inside the ESC itself -- the pty delivers a
// terminal write whole, and a lone ESC is the escape key, never a
// sequence prefix) translates exactly like the whole-chunk case.
func TestSequenceSplitAcrossReads(t *testing.T) {
	r := New(&chunkReader{data: []byte("ab\x1b[13;2ucd"), size: 6})
	if got := string(readAll(t, r)); got != "ab\ncd" {
		t.Fatalf("split translation = %q", got)
	}
}

// TestLoneEscapeIsFlushedImmediately pins the modal-TUI contract: a
// lone ESC passes through as the escape key (bubbletea maps it to
// KeyEscape the moment it arrives); holding it would deadlock the
// escape key -- the live bug this test exists for.
func TestLoneEscapeIsFlushedImmediately(t *testing.T) {
	r := New(&chunkReader{data: []byte("ab\x1bx"), size: 1})
	if got := string(readAll(t, r)); got != "ab\x1bx" {
		t.Fatalf("escape handling = %q", got)
	}
}

// TestDanglingEscapeAtEOFFlushesVerbatim pins the shutdown path: a lone
// ESC (or cut-off sequence) at EOF is delivered unchanged, never held.
func TestDanglingEscapeAtEOFFlushesVerbatim(t *testing.T) {
	in := "\x1b[13;2"
	if got := translate(t, in); got != in {
		t.Fatalf("dangling sequence was not flushed verbatim: %q", got)
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
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}
