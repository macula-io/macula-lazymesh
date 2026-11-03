package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// termText is text that is safe to put on the operator's terminal. It can
// only come from termTextOf, and every sink that prints text the operator
// did not type -- a chat entry, a panel row, a popup -- takes a termText
// rather than a string, so a new string field cannot reach the screen
// without going through the one constructor (#95).
//
// The zero value is the empty string: safe, because it holds nothing.
type termText string

// termTextOf is THE constructor for terminal text (#95). It makes
// model- or peer-controlled text safe by SHOWING every character a
// terminal would act on, never deleting it: a deleted control takes what
// it hides with it, and a parser that skips "the rest of the sequence" is
// exactly how "; rm -rf ~" disappears behind an unterminated OSC (#83
// review). Each rune is mapped on its own, so nothing next to a control is
// ever consumed:
//   - C0 controls and DEL become their Unicode control pictures (ESC is ␛);
//   - C1 controls, and every format character (the Cf class: bidi
//     overrides and isolates, the zero-width and word-joiner runes, the
//     byte-order mark), line and paragraph separators, and bytes that are
//     not UTF-8, become <U+XXXX> or <XX>;
//   - a carriage return that is not part of a line ending becomes a line
//     break, so a row cannot be overwritten;
//   - tab and newline stay: they move the cursor within what is already on
//     screen, they do not make the terminal act.
func termTextOf(s string) termText {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, "<%02X>", s[i])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteByte('\n')
		case r < 0x20:
			b.WriteRune(0x2400 + r)
		case r == 0x7f:
			b.WriteRune('␡')
		case (r >= 0x80 && r <= 0x9f) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp):
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return termText(b.String())
}

// String is the constructor's own output as a plain string, for the
// libraries that take one (lipgloss, the table). Colour and layout are
// applied to what it returns -- after the text is already safe.
func (t termText) String() string { return string(t) }
