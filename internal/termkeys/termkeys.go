// Package termkeys enables the kitty keyboard protocol's modified-key
// reporting and rewrites the one sequence lazymesh cares about —
// shift+enter (CSI 13;2u) — into the ctrl+j byte (0x0a) that the
// compose box's newline binding already understands.
//
// Why this exists: terminals conflate enter and shift+enter (both send
// \r) unless the app opts into the kitty keyboard protocol, and
// bubbletea v1.3.10 neither enables that protocol nor parses its CSI-u
// sequences. With the protocol pushed, modified keys arrive as
// CSI <code>;<mods>u; everything lazymesh does not translate passes
// through unchanged (other modified keys become unknown CSIs and are
// dropped by bubbletea — the same treatment they get today).
//
// The protocol is PUSHED on start and POPPED on exit, so the shell
// underneath never sees CSI-u sequences for its own shift+keys.
package termkeys

import (
	"encoding/hex"
	"io"
	"log/slog"
)

// Enable is the push sequence written to the terminal once, before the
// TUI starts: report alternate keys -- the modified forms of enter,
// tab, escape and backspace -- in CSI-u form (flag 4). Flag 1
// (disambiguate) is deliberately NOT set: it would also convert
// alt+arrows etc. to CSI-u and regress the legacy forms bubbletea
// already parses. Shift+enter is the one sequence lazymesh needs:
// CSI 13;2u.
const Enable = "\x1b[>4u"

// Disable is the pop sequence written after the TUI exits, restoring
// the terminal's default key reporting.
const Disable = "\x1b[<u"

// Reader wraps stdin and translates CSI 13;2u (shift+enter) into the
// ctrl+j byte. It is a byte-stream rewriter: bubbletea still parses the
// output, so a translated sequence must be one of the byte forms
// bubbletea already understands.
//
// Reader deliberately satisfies charmbracelet/x/term's File interface
// (ReadWriteCloser + Fd) by delegating to the wrapped file: bubbletea
// only puts the terminal in raw mode when the input reader it was given
// IS a term.File, and a wrapper that hid the fd would leave the
// terminal in cooked mode -- keys line-buffered, chars echoed by the
// terminal at the cursor (the "typed chars appear in the wrong place"
// bug found live 2026-09-12).
type Reader struct {
	inner io.Reader

	// fd backs raw-mode setup: the wrapped file's descriptor, when the
	// wrapped reader is a terminal file.
	fd func() uintptr
	// writeThrough and closeThrough delegate the rest of the File
	// contract to the wrapped file, when it has one.
	writeThrough func([]byte) (int, error)
	closeThrough func() error

	// out holds translated bytes ready to be delivered to the caller.
	out []byte
	// partial holds an incomplete CSI candidate across reads (an
	// escape-starting run whose final byte has not arrived yet).
	partial []byte
	eof     bool
}

// New wraps inner. When inner is a terminal file (os.Stdin), the
// returned Reader presents itself as the same file, so bubbletea
// applies raw mode to it.
func New(inner io.Reader) *Reader {
	r := &Reader{inner: inner}
	if f, ok := inner.(interface{ Fd() uintptr }); ok {
		r.fd = f.Fd
	}
	if w, ok := inner.(io.Writer); ok {
		r.writeThrough = w.Write
	}
	if c, ok := inner.(io.Closer); ok {
		r.closeThrough = c.Close
	}
	return r
}

// Fd reports the wrapped file's descriptor (0 when not a file), which
// is what lets bubbletea recognize the reader as a terminal.
func (r *Reader) Fd() uintptr {
	if r.fd != nil {
		return r.fd()
	}
	return 0
}

// Write passes writes through to the wrapped file, for the File
// contract.
func (r *Reader) Write(p []byte) (int, error) {
	if r.writeThrough != nil {
		return r.writeThrough(p)
	}
	return 0, io.ErrClosedPipe
}

// Close passes the close through to the wrapped file, for the File
// contract.
func (r *Reader) Close() error {
	if r.closeThrough != nil {
		return r.closeThrough()
	}
	return nil
}

// Read satisfies io.Reader. It refills from inner whenever its output
// buffer is empty, translating as it goes, and reports EOF only after
// everything has been delivered (including any dangling partial
// sequence, flushed verbatim).
func (r *Reader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		chunk := make([]byte, 4096)
		n, err := r.inner.Read(chunk)
		if n > 0 {
			r.process(chunk[:n])
			continue
		}
		if err != nil {
			r.eof = true
			// A sequence cut off by EOF is not a sequence: flush it.
			r.out = append(r.out, r.partial...)
			r.partial = nil
			if len(r.out) > 0 {
				break // deliver the tail first; EOF on the next call
			}
			return 0, err
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

// process consumes one input chunk: non-escape bytes pass through
// verbatim, escape-starting runs are classified and either translated,
// passed through, or held as partial. Chunks containing enter/escape
// bytes are hex-logged at debug level, so a live "shift+enter still
// submits" report can be answered with the terminal's actual bytes
// instead of a guess (2026-09-12).
func (r *Reader) process(data []byte) {
	for _, b := range data {
		if b == 0x0d || b == 0x0a || b == 0x1b || b >= 0x80 {
			slog.Debug("termkeys: input bytes", "hex", hex.EncodeToString(data))
			break
		}
	}
	buf := append(r.partial, data...)
	r.partial = nil
	i := 0
	for i < len(buf) {
		if buf[i] != 0x1b {
			j := i
			for j < len(buf) && buf[j] != 0x1b {
				j++
			}
			r.out = append(r.out, buf[i:j]...)
			i = j
			continue
		}
		seq, complete := classifyCSI(buf[i:])
		if !complete {
			// An escape-starting run with no decision yet: hold it.
			// Bounded: a candidate longer than a real sequence is
			// flushed verbatim rather than held forever.
			if len(buf)-i > 32 {
				r.out = append(r.out, buf[i:]...)
			} else {
				r.partial = append(r.partial, buf[i:]...)
			}
			return
		}
		if seq.newline {
			r.out = append(r.out, 0x0a) // ctrl+j -- the compose newline key
		} else {
			r.out = append(r.out, buf[i:i+seq.n]...)
		}
		i += seq.n
	}
}

// csi is one classified escape-starting run: n bytes consumed, and
// whether it is the shift+enter sequence.
type csi struct {
	n       int
	newline bool
}

// classifyCSI looks at an escape-starting run and decides: a complete
// CSI-u sequence (translated if shift+enter), a complete non-CSI escape
// (alt+key or the ESCAPE KEY ITSELF, passed through), a complete legacy
// CSI (passed through), or an incomplete CSI candidate (complete=false).
//
// A lone trailing ESC is flushed immediately, never held: bubbletea's
// parser treats ESC-alone as KeyEscape the moment it sees it, and the
// modal TUI depends on that (esc leaves insert mode). Holding it broke
// the escape key entirely (found live 2026-09-12). The trade-off: a
// CSI-u sequence must therefore arrive within one read of its ESC — the
// pty delivers terminal writes whole, which is the same boundary
// assumption bubbletea's own alt+key handling already makes.
func classifyCSI(data []byte) (csi, bool) {
	if len(data) == 1 {
		// A lone ESC: the escape key, complete as-is.
		return csi{n: 1}, true
	}
	if data[1] != '[' {
		// ESC followed by a normal byte: alt+key, a complete 2-byte
		// sequence.
		return csi{n: 2}, true
	}
	// A CSI candidate: parameters/intermediates until a final byte in
	// the 0x40..0x7e range.
	for j := 2; j < len(data); j++ {
		b := data[j]
		if b >= 0x40 && b <= 0x7e {
			final := b
			seq := csi{n: j + 1}
			if final == 'u' {
				// mods is 1 plus the sum of kitty's bit flags, so shift
				// (bit 0) is present exactly when mods-1 is odd.
				if code, mods, ok := parseU(data[2:j]); ok && code == 13 && mods > 1 && (mods-1)&1 != 0 {
					seq.newline = true
				}
			}
			return seq, true
		}
		if b < 0x20 || b > 0x3f {
			// Not a CSI after all: ESC plus garbage. Flush the two
			// leading bytes and let the rest be reprocessed.
			return csi{n: 2}, true
		}
	}
	return csi{}, false // ran out of bytes mid-sequence
}

// parseU parses "13;2" from a CSI-u body (after the '['), returning the
// code and the modifier bits. The modifier field is 1 plus the sum of
// kitty's bit flags, so shift sets bit 0.
func parseU(body []byte) (code int, mods int, ok bool) {
	semi := -1
	for i, b := range body {
		if b == ';' {
			semi = i
			break
		}
	}
	if semi <= 0 {
		return 0, 0, false
	}
	code = atoi(body[:semi])
	if code <= 0 {
		return 0, 0, false
	}
	mods = atoi(body[semi+1:])
	if mods <= 0 {
		return 0, 0, false
	}
	return code, mods, true
}

// atoi parses a small decimal byte string.
func atoi(b []byte) int {
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
