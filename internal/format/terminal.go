package format

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Terminal returns s safe to print to a terminal or a log line: control
// characters (including newlines and escape sequences), format characters such
// as bidi overrides, and invalid UTF-8 appear as visible Go-style escapes. Use
// it for text that comes from outside Vault, such as backup metadata. Other
// text, backslashes included, is unchanged.
func Terminal(s string) string {
	safe := true
	for _, r := range s {
		if !terminalSafe(r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case terminalSafe(r):
			b.WriteRune(r)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
		i += size
	}
	return b.String()
}

func terminalSafe(r rune) bool {
	return r != utf8.RuneError && unicode.IsGraphic(r) && !unicode.Is(unicode.Cf, r)
}
