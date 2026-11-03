package crypto

import "testing"

func TestCheckPassphraseStrength(t *testing.T) {
	t.Parallel()
	for _, weak := range []string{
		"", "short", "hunter42!", "elevenchars",
		"aaaaaaaaaaaaaaaa", "abcabcabcabcabc",
		"123456789012", "abcdefghijklmnop", "zyxwvutsrqpo",
		"password1234", "Password1234!", "P@ssw0rd2026!", "P@ssw0rd12345!", "W3lc0me12345!", "Summer2026!!!", "qwertyuiop12",
		"letmein12345", "unraidbackup1", "correcthorsebatterystaple",
		"987654321098", "4815162342481516", "my-test-passphrase",
		"٣٧١٩٤٨٢٦٠٥١٣", "qazwsxedcrfv", "1qaz2wsx3edc4", "zaq12wsxcde3",
	} {
		if err := CheckPassphraseStrength(weak); err == nil {
			t.Errorf("%q accepted", weak)
		}
	}
	for _, ok := range []string{
		"violet tractor spoons", "gT7#kL9!xQ2$", "8#2$9!4@7%1^", "60912783450912873465", "Zürich-Kaffee-Löffel",
		"correct horse battery", "パスフレーズは長いです安全",
	} {
		if err := CheckPassphraseStrength(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
}
