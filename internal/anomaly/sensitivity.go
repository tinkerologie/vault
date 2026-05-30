package anomaly

// Sensitivity controls how aggressively anomaly detectors flag events.
type Sensitivity string

const (
	SensStrict     Sensitivity = "strict"
	SensBalanced   Sensitivity = "balanced"
	SensPermissive Sensitivity = "permissive"
)

// madK maps a sensitivity level to the MAD multiplier (k) used in the
// modified Z-score threshold. Lower k → more sensitive (flags more).
var madK = map[Sensitivity]float64{
	SensStrict:     2.5,
	SensBalanced:   3.5,
	SensPermissive: 5.0,
}

// reliabilityStreak maps a sensitivity level to the minimum consecutive
// failed runs required before flagging a reliability regression.
var reliabilityStreak = map[Sensitivity]int{
	SensStrict:     1,
	SensBalanced:   2,
	SensPermissive: 3,
}

// capacityWarnDays maps a sensitivity level to how many days of runway
// must remain before triggering a capacity exhaustion warning.
var capacityWarnDays = map[Sensitivity]float64{
	SensStrict:     30,
	SensBalanced:   14,
	SensPermissive: 7,
}

// Resolve picks the effective sensitivity: a non-empty per-scope override wins,
// else the global default; unknown/empty strings fall back to SensBalanced.
func Resolve(jobOverride, globalDefault string) Sensitivity {
	pick := jobOverride
	if pick == "" {
		pick = globalDefault
	}
	switch Sensitivity(pick) {
	case SensStrict, SensBalanced, SensPermissive:
		return Sensitivity(pick)
	}
	return SensBalanced
}

// K returns the MAD multiplier threshold for this sensitivity level.
func (s Sensitivity) K() float64 { return madK[s] }

// Streak returns the minimum consecutive-failure streak length before a reliability anomaly is raised.
func (s Sensitivity) Streak() int { return reliabilityStreak[s] }

// WarnDays returns the capacity runway warning threshold in days.
func (s Sensitivity) WarnDays() float64 { return capacityWarnDays[s] }
