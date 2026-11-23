package main

// trendMomentum returns a bounded acceleration signal comparing the current
// most recent 24-hour window with the preceding 24-hour window. Negative growth is
// clamped to zero; a topic with no prior-window posts receives full momentum.
func trendMomentum(current, previous int64) float64 {
	if current <= 0 {
		return 0
	}
	if previous <= 0 {
		return 1
	}
	largest := current
	if previous > largest {
		largest = previous
	}
	if largest <= 0 {
		return 0
	}
	momentum := float64(current-previous) / float64(largest)
	if momentum < 0 {
		return 0
	}
	if momentum > 1 {
		return 1
	}
	return momentum
}

func trendReasons(momentum float64) []string {
	if momentum >= 0.6 {
		return []string{"accelerating"}
	}
	return []string{}
}
