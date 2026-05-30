package anomaly

import "time"

// Clock abstracts time.Now() for deterministic testing.
type Clock interface{ Now() time.Time }

// RealClock delegates to the system clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a controllable clock for tests.
type FakeClock struct{ t time.Time }

func NewFakeClock(t time.Time) *FakeClock    { return &FakeClock{t: t} }
func (f *FakeClock) Now() time.Time          { return f.t }
func (f *FakeClock) Advance(d time.Duration) { f.t = f.t.Add(d) }
