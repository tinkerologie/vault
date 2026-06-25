package scheduler

import "testing"

func TestValidateSchedule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    string
		wantErr bool
	}{
		{"empty is manual", "", false},
		{"whitespace trims to manual", "   ", false},
		{"standard 5-field", "0 3 * * *", false},
		{"step values", "*/15 * * * *", false},
		{"last-day-of-month", "0 2 L * *", false},
		{"descriptor", "@daily", false},
		{"prose is invalid", "not a cron", true},
		{"too few fields", "0 3 * *", true},
		{"out of range", "99 99 * * *", true},
		{"unknown descriptor", "@bogus", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSchedule(tt.spec)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateSchedule(%q) = nil, want error", tt.spec)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateSchedule(%q) = %v, want nil", tt.spec, err)
			}
		})
	}
}
