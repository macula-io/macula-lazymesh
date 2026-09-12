// Package termkeys implements the kitty keyboard protocol the way the
// TUI-ecosystem apps that support shift+enter do: request flag 8
// (report ALL keys as escape codes), then translate every incoming
// CSI-u sequence back into the legacy byte forms bubbletea understands.
//
// Why the earlier partial attempts failed (found live 2026-09-12): the
// kitty protocol's flag 4 (alternate keys) and flag 1 (disambiguate)
// only affect keys ALREADY reported as escape codes -- enter is a
// text-generating key, so it kept arriving as a plain \r no matter what
// combination of those flags was pushed. Flag 8 makes the terminal
// report EVERY key as CSI-u, including Enter/Tab/Backspace, which is
// what OpenCode and Claude Code request; the cost is that the wrapper
// must translate every key back, which is exactly what this package
// does.
package termkeys

import (
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
)

// Enable is the push sequence written to the terminal once, before the
// TUI starts: report all keys as escape codes (flag 8).
const Enable = "\x1b[>8u"

// Disable is the pop sequence written after the TUI exits, restoring
// the terminal's default key reporting.
const Disable = "\x1b[<u"

// Reader wraps stdin and translates kitty CSI-u key reports back into
// legacy byte sequences. It is a byte-stream rewriter: bubbletea still
// parses the output.
//
// Reader deliberately satisfies charmbracelet/x/term's File interface
// (ReadWriteCloser + Fd) by delegating to the wrapped file: bubbletea
// only puts the terminal in raw mode when the input reader it was given
// IS a term.File, and a wrapper that hid the fd would leave the
// terminal in cooked mode (the "typed chars echo at the cursor" bug
// found live 2026-09-12).
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
// bytes are hex-logged at debug level, so a live key-handling report
// can be answered with the terminal's actual bytes instead of a guess.
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
		if seq.translated != nil {
			r.out = append(r.out, seq.translated...)
		} else {
			r.out = append(r.out, buf[i:i+seq.n]...)
		}
		i += seq.n
	}
}

// csi is one classified escape-starting run: n bytes consumed, and the
// translated legacy bytes (nil = pass through unchanged).
type csi struct {
	n          int
	translated []byte
}

// classifyCSI looks at an escape-starting run and decides: a complete
// CSI-u sequence (translated via translateU), a complete non-CSI escape
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
			if b == 'u' {
				return csi{n: j + 1, translated: translateU(data[2:j])}, true
			}
			// A legacy CSI (arrows, mouse, bracketed paste markers):
			// pass through untouched.
			return csi{n: j + 1}, true
		}
		if b < 0x20 || b > 0x3f {
			// Not a CSI after all: ESC plus garbage. Flush the two
			// leading bytes and let the rest be reprocessed.
			return csi{n: 2}, true
		}
	}
	return csi{}, false // ran out of bytes mid-sequence
}

// splitCodeMods splits a CSI-u body ("13;2", "105;1", or just "105")
// into the key code and the modifier value (1 when omitted).
func splitCodeMods(body []byte) (code int, mods int) {
	mods = 1
	for i := 0; i < len(body); i++ {
		if body[i] == ';' {
			code = atoi(body[:i])
			mods = atoi(body[i+1:])
			if mods < 1 {
				mods = 1
			}
			return code, mods
		}
		if body[i] == ':' {
			// Alternate-key form (flag 4 also enabled): ignore the
			// alternate code.
			body = body[i+1:]
			i = -1
		}
	}
	code = atoi(body)
	if mods < 1 {
		mods = 1
	}
	return code, mods
}

// translateU converts one CSI-u key report into the legacy bytes
// bubbletea understands, or nil when the key has no legacy form (e.g.
// super/hyper-modified) -- the sequence then passes through unchanged
// and bubbletea drops it.
//
// The kitty modifier value is 1 plus the sum of bit flags (shift=1,
// alt=2, ctrl=4, super=8, hyper=16, meta=32), the same encoding the
// legacy CSI modifier parameter uses -- so the mods value carries over
// to legacy sequences verbatim.
func translateU(body []byte) []byte {
	code, mods := splitCodeMods(body)
	bits := mods - 1
	shift := bits&1 != 0
	alt := bits&2 != 0
	ctrl := bits&4 != 0
	if bits&^0x7 != 0 {
		return nil // super/hyper/meta/caps/num: no legacy encoding
	}

	switch {
	case code == 13: // ENTER
		switch {
		case shift:
			return []byte{0x0a} // ctrl+j byte: the compose newline binding
		case alt:
			return []byte{0x1b, 0x0d}
		default:
			return []byte{0x0d} // plain enter (ctrl included): submit
		}
	case code == 9: // TAB
		if shift {
			return []byte{0x1b, '[', 'Z'} // bubbletea's KeyShiftTab
		}
		return []byte{0x09}
	case code == 127: // BACKSPACE
		return []byte{0x7f}
	case code == 27: // ESCAPE
		return []byte{0x1b}
	case code >= 32 && code <= 126:
		b := byte(code)
		if alt {
			return []byte{0x1b, b}
		}
		if ctrl {
			return legacyCtrl(b)
		}
		return []byte{b} // shift is already folded into the codepoint
	}

	// Functional keys. The code numbers are kitty's canonical wire
	// numbers (kitty's own key_encoding.py generated tables): the
	// legacy-terminal CSI numbers where a key has one, the Unicode
	// Private Use Area number for the arrow keys and F3, which only
	// exist as letter-trailer sequences.
	switch code {
	case 2: // INSERT
		return legacyTilde(mods, 2)
	case 3: // DELETE
		return legacyTilde(mods, 3)
	case 5: // PAGE_UP
		return legacyTilde(mods, 5)
	case 6: // PAGE_DOWN
		return legacyTilde(mods, 6)
	case 7: // HOME
		return legacyCursor(mods, 'H')
	case 8: // END
		return legacyCursor(mods, 'F')
	case 11: // F1
		return legacyF(mods, 'P')
	case 12: // F2
		return legacyF(mods, 'Q')
	case 14: // F4
		return legacyF(mods, 'S')
	case 15: // F5
		return legacyTilde(mods, 15)
	case 17: // F6
		return legacyTilde(mods, 17)
	case 18: // F7
		return legacyTilde(mods, 18)
	case 19: // F8
		return legacyTilde(mods, 19)
	case 20: // F9
		return legacyTilde(mods, 20)
	case 21: // F10
		return legacyTilde(mods, 21)
	case 23: // F11
		return legacyTilde(mods, 23)
	case 24: // F12
		return legacyTilde(mods, 24)
	case 57350: // LEFT
		return legacyCursor(mods, 'D')
	case 57351: // RIGHT
		return legacyCursor(mods, 'C')
	case 57352: // UP
		return legacyCursor(mods, 'A')
	case 57353: // DOWN
		return legacyCursor(mods, 'B')
	case 57366: // F3
		return legacyF(mods, 'R')
	}
	return nil
}

// legacyCursor builds a cursor-key sequence: "CSI A" unmodified, or
// "CSI 1;modsA" with modifiers (the legacy modifier parameter uses the
// same 1-plus-bit-flags encoding).
func legacyCursor(mods int, final byte) []byte {
	if mods > 1 {
		return []byte(fmt.Sprintf("\x1b[1;%d%c", mods, final))
	}
	return []byte{0x1b, '[', final}
}

// legacyTilde builds a "CSI n~" / "CSI n;mods~" sequence.
func legacyTilde(mods, n int) []byte {
	if mods > 1 {
		return []byte(fmt.Sprintf("\x1b[%d;%d~", n, mods))
	}
	return []byte(fmt.Sprintf("\x1b[%d~", n))
}

// legacyF builds an F1-F4 sequence: "SS3 X" unmodified, or the xterm
// extended form "CSI 1;modsX" with modifiers.
func legacyF(mods int, final byte) []byte {
	if mods > 1 {
		return []byte(fmt.Sprintf("\x1b[1;%d%c", mods, final))
	}
	return []byte{0x1b, 'O', final}
}

// legacyCtrl maps an ASCII code with ctrl held to its legacy C0 byte:
// letters go to 1-26, and the historical terminal layout for the rest.
func legacyCtrl(code byte) []byte {
	switch {
	case code >= 'a' && code <= 'z':
		return []byte{code - 'a' + 1}
	case code >= 'A' && code <= 'Z':
		return []byte{code - 'A' + 1}
	case code == ' ' || code == '@' || code == '2':
		return []byte{0x00}
	case code == '3':
		return []byte{0x1b}
	case code == '4':
		return []byte{0x1c}
	case code == '5':
		return []byte{0x1d}
	case code == '6':
		return []byte{0x1e}
	case code == '7':
		return []byte{0x1f}
	case code == '8':
		return []byte{0x7f}
	case code == '[':
		return []byte{0x1b}
	case code == '\\':
		return []byte{0x1c}
	case code == ']':
		return []byte{0x1d}
	case code == '^':
		return []byte{0x1e}
	case code == '_':
		return []byte{0x1f}
	case code == '?':
		return []byte{0x7f}
	}
	return []byte{code}
}

// atoi parses a small decimal byte string; -1 on any non-digit.
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
