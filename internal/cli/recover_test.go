package cli

import "testing"

func TestTrimLineEnding(t *testing.T) {
	for in, want := range map[string]string{
		"secret":     "secret",
		"secret\n":   "secret",
		"secret\r\n": "secret",
		"secret\n\n": "secret\n",
		"secret\r":   "secret\r",
		"":           "",
	} {
		if got := trimLineEnding(in); got != want {
			t.Errorf("trimLineEnding(%q) = %q, want %q", in, got, want)
		}
	}
}
