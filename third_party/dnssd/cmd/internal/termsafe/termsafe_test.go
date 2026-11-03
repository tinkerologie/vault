package termsafe

import "testing"

func TestString(t *testing.T) {
	for in, want := range map[string]string{
		"living room._hap": "living room._hap",
		"Büro ☕":           "Büro ☕",
		"\x1b]0;owned\x07": `\x1b]0;owned\x07`,
		"a\r\nb":           `a\x0d\x0ab`,
		"del\x7f":          `del\x7f`,
		"c1\u009b31m":      `c1\u009b31m`,
		"bidi\u202e":       `bidi\u202e`,
		"tag\U000e0001":    `tag\U000e0001`,
		"bad\xff":          `bad\xff`,
	} {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}
