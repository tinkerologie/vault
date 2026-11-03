package format

import (
	"strings"
	"testing"
)

func TestTerminal(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":                      "",
		`C:\backups\Füße ☕.tar`: `C:\backups\Füße ☕.tar`,
		"\x1b[2Jgone":           `\x1b[2Jgone`,
		"\x1b]0;title\x07":      `\x1b]0;title\x07`,
		"a\nFAKE: ok\r":         `a\x0aFAKE: ok\x0d`,
		"tab\there\x7f":         `tab\x09here\x7f`,
		"c1\u009b31m":           `c1\u009b31m`,
		"name\u202egpj.exe":     `name\u202egpj.exe`,
		"tag\U000e0001":         `tag\U000e0001`,
		"bad\xff":               `bad\xff`,
	} {
		if got := Terminal(in); got != want {
			t.Errorf("Terminal(%q) = %q, want %q", in, got, want)
		}
	}
	if s := Terminal(strings.Repeat("x", 10)); s != strings.Repeat("x", 10) {
		t.Error("clean text changed")
	}
}
