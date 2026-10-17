package memlimit

import (
	"math"
	"testing"
)

// fakeLimit replaces debug.SetMemoryLimit for one test and restores the
// package state afterwards.
func fakeLimit(t *testing.T, env bool, initial int64) *int64 {
	t.Helper()
	cur := initial
	prevSet, prevEnv := setMemoryLimit, envLimitSet
	mu.Lock()
	prevMB := settingMB
	settingMB = 0
	mu.Unlock()
	setMemoryLimit = func(n int64) int64 {
		old := cur
		if n >= 0 {
			cur = n
		}
		return old
	}
	envLimitSet = env
	t.Cleanup(func() {
		setMemoryLimit, envLimitSet = prevSet, prevEnv
		mu.Lock()
		settingMB = prevMB
		mu.Unlock()
	})
	return &cur
}

func TestParseMB(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"", 0, true},
		{" 0 ", 0, true},
		{"512", 512, true},
		{"1048576", 1048576, true},
		{"511", 0, false},
		{"1048577", 0, false},
		{"-1", 0, false},
		{"1.5", 0, false},
		{"lots", 0, false},
	} {
		got, err := ParseMB(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseMB(%q) = %d, %v; want %d, ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestApplyOffByDefault(t *testing.T) {
	fakeLimit(t, false, math.MaxInt64)
	st := Current()
	if st.Source != SourceOff || st.LimitBytes != 0 || st.SettingMB != 0 || st.EnvironmentOverride {
		t.Fatalf("Current() = %+v, want off", st)
	}
}

func TestApplySetAndLiveChange(t *testing.T) {
	cur := fakeLimit(t, false, math.MaxInt64)

	Apply(1024)
	if *cur != 1024<<20 {
		t.Fatalf("limit = %d, want %d", *cur, int64(1024<<20))
	}
	if st := Current(); st.Source != SourceSetting || st.LimitBytes != 1024<<20 || st.SettingMB != 1024 {
		t.Fatalf("Current() = %+v", st)
	}

	Apply(2048)
	if *cur != 2048<<20 {
		t.Fatalf("after change limit = %d", *cur)
	}

	Apply(0)
	if *cur != math.MaxInt64 {
		t.Fatalf("after off limit = %d, want MaxInt64", *cur)
	}
	if st := Current(); st.Source != SourceOff || st.LimitBytes != 0 {
		t.Fatalf("Current() after off = %+v", st)
	}
}

func TestApplyNeverOverridesEnvironment(t *testing.T) {
	cur := fakeLimit(t, true, 3<<30)

	Apply(1024)
	if *cur != 3<<30 {
		t.Fatalf("GOMEMLIMIT overridden: limit = %d", *cur)
	}
	st := Current()
	if st.Source != SourceEnvironment || !st.EnvironmentOverride || st.LimitBytes != 3<<30 || st.SettingMB != 1024 {
		t.Fatalf("Current() = %+v", st)
	}

	Apply(0)
	if *cur != 3<<30 {
		t.Fatalf("GOMEMLIMIT cleared: limit = %d", *cur)
	}
}

func TestSuggestedMB(t *testing.T) {
	for _, tc := range []struct {
		host uint64
		want int64
	}{
		{0, MinMB},
		{2 << 30, MinMB},   // 245 MiB -> floor
		{16 << 30, 1920},   // 1966 -> 1920
		{64 << 30, 7808},   // 7864 -> 7808
		{128 << 30, 15680}, // 15728 -> 15680
	} {
		if got := SuggestedMB(tc.host); got != tc.want {
			t.Errorf("SuggestedMB(%d) = %d, want %d", tc.host, got, tc.want)
		}
	}
}
