package anomaly

import (
	"math"
	"testing"
)

// TestBaselineMedian covers nil, single, even-length, odd-length, and
// verifies the input slice is NOT mutated.
func TestBaselineMedian(t *testing.T) {
	t.Run("nil returns 0", func(t *testing.T) {
		if got := Median(nil); got != 0 {
			t.Errorf("Median(nil) = %v, want 0", got)
		}
	})

	t.Run("empty returns 0", func(t *testing.T) {
		if got := Median([]float64{}); got != 0 {
			t.Errorf("Median([]) = %v, want 0", got)
		}
	})

	t.Run("single value", func(t *testing.T) {
		if got := Median([]float64{5}); got != 5 {
			t.Errorf("Median([5]) = %v, want 5", got)
		}
	})

	t.Run("two values", func(t *testing.T) {
		if got := Median([]float64{1, 3}); got != 2 {
			t.Errorf("Median([1,3]) = %v, want 2", got)
		}
	})

	t.Run("even length [1,2,5,6]", func(t *testing.T) {
		if got := Median([]float64{1, 2, 5, 6}); got != 3.5 {
			t.Errorf("Median([1,2,5,6]) = %v, want 3.5", got)
		}
	})

	t.Run("odd length [1,2,3,4,5]", func(t *testing.T) {
		if got := Median([]float64{1, 2, 3, 4, 5}); got != 3 {
			t.Errorf("Median([1,2,3,4,5]) = %v, want 3", got)
		}
	})

	t.Run("unsorted input sorted internally", func(t *testing.T) {
		// Input is deliberately unsorted; Median must produce the correct result.
		if got := Median([]float64{5, 1, 3}); got != 3 {
			t.Errorf("Median([5,1,3]) = %v, want 3", got)
		}
	})

	t.Run("input slice not mutated", func(t *testing.T) {
		original := []float64{5, 1, 3}
		snap := make([]float64, len(original))
		copy(snap, original)
		Median(original)
		for i, v := range original {
			if v != snap[i] {
				t.Errorf("Median mutated input at index %d: got %v, want %v", i, v, snap[i])
			}
		}
	})

	t.Run("all identical", func(t *testing.T) {
		if got := Median([]float64{7, 7, 7, 7}); got != 7 {
			t.Errorf("Median([7,7,7,7]) = %v, want 7", got)
		}
	})

	t.Run("one extreme outlier does not affect median", func(t *testing.T) {
		// [1, 2, 3, 4, 1000] — median is still 3
		if got := Median([]float64{1, 2, 3, 4, 1000}); got != 3 {
			t.Errorf("Median([1,2,3,4,1000]) = %v, want 3", got)
		}
	})
}

// TestBaselineMAD_IglewiczHoaglinWorkedExample verifies the MAD computation
// against a hand-computed fixture.
//
// Dataset: [2.1, 2.4, 2.5, 2.6, 2.7, 2.7, 2.8, 2.9, 3.0, 3.1] (10 values)
//
// Hand-computation:
//   - Sorted median: mean of 5th and 6th = (2.7+2.7)/2 = 2.7
//   - Absolute deviations from 2.7:
//     0.6, 0.3, 0.2, 0.1, 0.0, 0.0, 0.1, 0.2, 0.3, 0.4
//   - Sorted deviations: [0.0, 0.0, 0.1, 0.1, 0.2, 0.2, 0.3, 0.3, 0.4, 0.6]
//   - MAD = mean of 5th and 6th = (0.2+0.2)/2 = 0.2
//
// Note: the task specification suggested MAD ≈ 0.25, but direct computation
// gives 0.2. We trust the arithmetic and use 0.2 here.
func TestBaselineMAD_IglewiczHoaglinWorkedExample(t *testing.T) {
	xs := []float64{2.1, 2.4, 2.5, 2.6, 2.7, 2.7, 2.8, 2.9, 3.0, 3.1}

	const delta = 0.0001

	med := Median(xs)
	wantMed := 2.7
	if math.Abs(med-wantMed) > delta {
		t.Errorf("Median = %v, want %v (delta %v)", med, wantMed, delta)
	}

	mad := MAD(xs)
	wantMAD := 0.2 // hand-computed; spec suggested 0.25 but arithmetic gives 0.2
	if math.Abs(mad-wantMAD) > delta {
		t.Errorf("MAD = %v, want %v (delta %v)", mad, wantMAD, delta)
	}
}

// TestBaselineMAD covers nil/empty, single value, all-identical (MAD=0),
// and a small set with a known result.
func TestBaselineMAD(t *testing.T) {
	t.Run("nil returns 0", func(t *testing.T) {
		if got := MAD(nil); got != 0 {
			t.Errorf("MAD(nil) = %v, want 0", got)
		}
	})

	t.Run("empty returns 0", func(t *testing.T) {
		if got := MAD([]float64{}); got != 0 {
			t.Errorf("MAD([]) = %v, want 0", got)
		}
	})

	t.Run("single value MAD=0", func(t *testing.T) {
		// Deviation of a single element from itself is 0.
		if got := MAD([]float64{42}); got != 0 {
			t.Errorf("MAD([42]) = %v, want 0", got)
		}
	})

	t.Run("two values", func(t *testing.T) {
		// xs=[1,3], median=2, deviations=[1,1], MAD=1
		if got := MAD([]float64{1, 3}); got != 1 {
			t.Errorf("MAD([1,3]) = %v, want 1", got)
		}
	})

	t.Run("all identical MAD=0", func(t *testing.T) {
		if got := MAD([]float64{5, 5, 5, 5, 5}); got != 0 {
			t.Errorf("MAD([5,5,5,5,5]) = %v, want 0", got)
		}
	})

	t.Run("one extreme outlier", func(t *testing.T) {
		// xs=[1,2,3,4,1000], median=3
		// deviations=[2,1,0,1,997], sorted=[0,1,1,2,997], MAD=1
		if got := MAD([]float64{1, 2, 3, 4, 1000}); got != 1 {
			t.Errorf("MAD([1,2,3,4,1000]) = %v, want 1", got)
		}
	})
}

// TestBaselineModifiedZScore covers the standard case, mad=0 branches, and
// the identity (x == median, mad == 0 → 0).
func TestBaselineModifiedZScore(t *testing.T) {
	const k = 0.6745
	const delta = 1e-9

	t.Run("standard case ModifiedZScore(2,1,1)≈0.6745", func(t *testing.T) {
		got := ModifiedZScore(2, 1, 1)
		if math.Abs(got-k) > delta {
			t.Errorf("ModifiedZScore(2,1,1) = %v, want %v", got, k)
		}
	})

	t.Run("negative deviation", func(t *testing.T) {
		// x < median: should be negative
		got := ModifiedZScore(0, 1, 1)
		want := k * (0 - 1) / 1
		if math.Abs(got-want) > delta {
			t.Errorf("ModifiedZScore(0,1,1) = %v, want %v", got, want)
		}
	})

	t.Run("x == median → 0 regardless of mad", func(t *testing.T) {
		if got := ModifiedZScore(3, 3, 2); got != 0 {
			t.Errorf("ModifiedZScore(3,3,2) = %v, want 0", got)
		}
	})

	t.Run("mad=0 x>median → +Inf", func(t *testing.T) {
		got := ModifiedZScore(5, 1, 0)
		if !math.IsInf(got, 1) {
			t.Errorf("ModifiedZScore(5,1,0) = %v, want +Inf", got)
		}
	})

	t.Run("mad=0 x<median → -Inf", func(t *testing.T) {
		got := ModifiedZScore(0, 1, 0)
		if !math.IsInf(got, -1) {
			t.Errorf("ModifiedZScore(0,1,0) = %v, want -Inf", got)
		}
	})

	t.Run("mad=0 x==median → 0", func(t *testing.T) {
		got := ModifiedZScore(7, 7, 0)
		if got != 0 {
			t.Errorf("ModifiedZScore(7,7,0) = %v, want 0", got)
		}
	})

	t.Run("large deviation produces high score", func(t *testing.T) {
		// Far outlier: x=100, median=1, mad=1 → score = 0.6745 * 99
		got := ModifiedZScore(100, 1, 1)
		want := k * 99
		if math.Abs(got-want) > delta {
			t.Errorf("ModifiedZScore(100,1,1) = %v, want %v", got, want)
		}
	})
}

// TestBaselineMedianMultiplier covers the standard ratio, the zero-denominator
// guard, and a few boundary values.
func TestBaselineMedianMultiplier(t *testing.T) {
	t.Run("(600,100)→6", func(t *testing.T) {
		if got := MedianMultiplier(600, 100); got != 6 {
			t.Errorf("MedianMultiplier(600,100) = %v, want 6", got)
		}
	})

	t.Run("(x,0)→0", func(t *testing.T) {
		if got := MedianMultiplier(999, 0); got != 0 {
			t.Errorf("MedianMultiplier(999,0) = %v, want 0", got)
		}
	})

	t.Run("(0,0)→0", func(t *testing.T) {
		if got := MedianMultiplier(0, 0); got != 0 {
			t.Errorf("MedianMultiplier(0,0) = %v, want 0", got)
		}
	})

	t.Run("(0,100)→0", func(t *testing.T) {
		if got := MedianMultiplier(0, 100); got != 0 {
			t.Errorf("MedianMultiplier(0,100) = %v, want 0", got)
		}
	})

	t.Run("(50,100)→0.5", func(t *testing.T) {
		if got := MedianMultiplier(50, 100); got != 0.5 {
			t.Errorf("MedianMultiplier(50,100) = %v, want 0.5", got)
		}
	})

	t.Run("identical (100,100)→1", func(t *testing.T) {
		if got := MedianMultiplier(100, 100); got != 1 {
			t.Errorf("MedianMultiplier(100,100) = %v, want 1", got)
		}
	})
}
