package history

// MissingRanges returns timestamp windows in [start, end] not covered by
// existing CSV bounds (inclusive). A 60s pad avoids re-fetching the edge swap.
func MissingRanges(bounds *[2]int64, start, end int64) [][2]int64 {
	if start > end {
		return nil
	}
	if bounds == nil {
		return [][2]int64{{start, end}}
	}
	lo, hi := bounds[0], bounds[1]
	var out [][2]int64
	if start < lo {
		a, b := start, min64(end, lo-60)
		if a <= b {
			out = append(out, [2]int64{a, b})
		}
	}
	if end > hi {
		a, b := max64(start, hi+60), end
		if a <= b {
			out = append(out, [2]int64{a, b})
		}
	}
	return out
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
