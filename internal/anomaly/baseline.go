package anomaly

import (
	"math"
	"sort"
)

// Median returns the median of xs. It operates on a defensive copy and never
// mutates the caller's slice. Returns 0 for a nil or empty input.
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]float64, len(xs))
	copy(cp, xs)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// MAD returns the Median Absolute Deviation: the median of |xᵢ − median(xs)|.
// Returns 0 for a nil or empty input.
func MAD(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	m := Median(xs)
	devs := make([]float64, len(xs))
	for i, v := range xs {
		devs[i] = math.Abs(v - m)
	}
	return Median(devs)
}

// ModifiedZScore returns the Iglewicz–Hoaglin modified z-score for x relative
// to a precomputed median and MAD.
//
// The scaling constant 0.6745 is the 0.75 quantile of the standard normal
// distribution, chosen so that ModifiedZScore ≈ ordinary z-score when the
// data are normal. Values with |score| > 3.5 are conventionally flagged as
// outliers (Iglewicz & Hoaglin, 1993).
//
// When mad == 0 (all observations are identical to the median):
//   - returns +Inf  if x > median   (definite outlier — further away)
//   - returns -Inf  if x < median   (definite outlier — further away)
//   - returns 0     if x == median  (identical to baseline — not flagged)
//
// The caller is responsible for deciding how to handle ±Inf values.
func ModifiedZScore(x, median, mad float64) float64 {
	const k = 0.6745 // 0.75-quantile of the standard normal (Iglewicz–Hoaglin)
	if mad == 0 {
		switch {
		case x > median:
			return math.Inf(1)
		case x < median:
			return math.Inf(-1)
		default:
			return 0
		}
	}
	return k * (x - median) / mad
}

// MedianMultiplier returns x / median. Returns 0 when median == 0 to avoid
// a division-by-zero panic; the caller interprets 0 as "no baseline".
func MedianMultiplier(x, median float64) float64 {
	if median == 0 {
		return 0
	}
	return x / median
}
