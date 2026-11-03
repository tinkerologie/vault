// Package termsafe is a Vault patch (see VAULT_PATCHES.md). DNS-SD names,
// hosts and TXT values come from any host on the local link, so the command
// line tools escape control bytes before printing them to a terminal.
package termsafe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// String returns s with control, format (such as bidi overrides) and invalid
// bytes shown as Go-style escapes, so the terminal never interprets them.
func String(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unicode.IsGraphic(r) && !unicode.Is(unicode.Cf, r):
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
