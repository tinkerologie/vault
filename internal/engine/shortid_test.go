package engine

import "testing"

// TestShortID pins the #170 fix: stored item IDs can be short or empty
// (imported jobs record no container ID) and must not panic the run.
func TestShortID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "<none>"},
		{"abc", "abc"},
		{"0123456789ab", "0123456789ab"},
		{"0123456789abcdef0123456789abcdef", "0123456789ab"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := ShortID(c.in); got != c.want {
				t.Errorf("ShortID(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
