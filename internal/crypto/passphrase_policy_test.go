package crypto

import (
	"strings"
	"testing"
	"time"
)

func TestCheckPassphraseStrength(t *testing.T) {
	t.Parallel()
	for _, weak := range []string{
		"", "short", "hunter42!", "elevenchars",
		"aaaaaaaaaaaaaaaa", "abcabcabcabcabc",
		"123456789012", "abcdefghijklmnop", "zyxwvutsrqpo",
		"password1234", "Password1234!", "P@ssw0rd2026!", "!\"#$%&'()*+,-", "1-2-3-4-5-6-7-8-9-0-1", "a.b.c.d.e.f.g", "1 2 3 4 5 6 7 8", "P@ssw0rd12345!", "W3lc0me12345!", "Summer2026!!!", "qwertyuiop12",
		"letmein12345", "unraidbackup1", "correcthorsebatterystaple",
		"987654321098", "4815162342481516", "my-test-passphrase",
		"٣٧١٩٤٨٢٦٠٥١٣", "qazwsxedcrfv", "1qaz2wsx3edc4", "zaq12wsxcde3",
	} {
		if err := CheckPassphraseStrength(weak); err == nil {
			t.Errorf("%q accepted", weak)
		}
	}
	for _, ok := range []string{
		"violet tractor spoons", "gT7#kL9!xQ2$", "8#2$9!4@7%1^", "!@#$%^&*()_+", "60912783450912873465", "Zürich-Kaffee-Löffel",
		"correct horse battery", "パスフレーズは長いです安全",
	} {
		if err := CheckPassphraseStrength(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
}

// Repeated words must not make the check quadratic. This 4 MiB input takes well
// under the limit when linear; deleting per match blows far past it.
func TestCheckPassphraseStrength_LongRepeatedWordsStayCheap(t *testing.T) {
	t.Parallel()
	start := time.Now()
	if err := CheckPassphraseStrength(strings.Repeat("password", 1<<19)); err == nil {
		t.Error("repeated common word accepted")
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("took %s", d)
	}
}
