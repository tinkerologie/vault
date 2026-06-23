package scheduler

import "testing"

func TestValidateSchedule(t *testing.T) {
	t.Parallel()

	valid := []string{
		"",             // manual-only job
		"   ",          // whitespace trims to empty → manual
		"0 3 * * *",    // standard 5-field
		"*/15 * * * *", // step values
		"0 2 L * *",    // last-day-of-month special case
		"@daily",       // descriptor
	}
	for _, s := range valid {
		if err := ValidateSchedule(s); err != nil {
			t.Errorf("ValidateSchedule(%q) = %v, want nil", s, err)
		}
	}

	invalid := []string{
		"not a cron",
		"0 3 * *",     // only 4 fields
		"99 99 * * *", // out-of-range
		"@bogus",      // unknown descriptor
	}
	for _, s := range invalid {
		if err := ValidateSchedule(s); err == nil {
			t.Errorf("ValidateSchedule(%q) = nil, want error", s)
		}
	}
}
