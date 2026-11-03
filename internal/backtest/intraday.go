package backtest

import (
	"math"

	"amazing/internal/history"
	"amazing/internal/simulate"
)

// HoursPerDay is the resolution of the intraday price paths a hedge steps
// along. Index j of an hourly path is the price j hours after the close of
// day 0, so index d*HoursPerDay is the close of day d — the same price the
// daily series holds at index d.
const HoursPerDay = 24

// hourlyCloses is the last swap price at the end of each hour from the close
// of the first day (fromTS's UTC day) through the close of day `days`, with
// quiet hours carrying the previous price. It lines up with DailyCloses.
func hourlyCloses(rows []history.Row, fromTS int64, days int) []float64 {
	n := days*HoursPerDay + 1
	out := make([]float64, n)
	if len(rows) == 0 {
		return out
	}
	last := rows[0].Price
	start := fromTS + 86400 - 1
	ri := 0
	for j := 0; j < n; j++ {
		end := start + int64(j)*3600
		for ri < len(rows) && rows[ri].Timestamp <= end {
			if rows[ri].Price > 0 {
				last = rows[ri].Price
			}
			ri++
		}
		out[j] = last
	}
	return out
}

// bridgeHourly fills each day of a daily relative path with hourly points
// drawn as a geometric Brownian bridge at annual volatility sigma. The daily
// points are kept exactly, so anything measured at the closes is unchanged;
// only what happens between closes (an intraday re-hedge) sees the new steps.
func bridgeHourly(rel []float64, sigma float64, seed int64) []float64 {
	days := len(rel) - 1
	if days < 1 {
		return append([]float64(nil), rel...)
	}
	rnd := simulate.Mulberry32(simulate.ToUint32(float64(int32(seed))*131 + 17))
	s := sigma * math.Sqrt(1.0/(365*HoursPerDay))
	out := make([]float64, days*HoursPerDay+1)
	out[0] = rel[0]
	for d := 1; d <= days; d++ {
		a, b := math.Log(rel[d-1]), math.Log(rel[d])
		x := a
		for k := 1; k < HoursPerDay; k++ {
			left := float64(HoursPerDay - k + 1)
			x += (b-x)/left + s*math.Sqrt((left-1)/left)*simulate.Gauss(rnd)
			out[(d-1)*HoursPerDay+k] = math.Exp(x)
		}
		out[d*HoursPerDay] = rel[d]
	}
	return out
}

// flatHourly repeats each daily point through the following day's hours, for
// an underlying with no intraday data: the path only moves at the closes.
func flatHourly(rel []float64) []float64 {
	days := len(rel) - 1
	if days < 1 {
		return append([]float64(nil), rel...)
	}
	out := make([]float64, days*HoursPerDay+1)
	for d := 1; d <= days; d++ {
		for k := 0; k < HoursPerDay; k++ {
			out[(d-1)*HoursPerDay+k] = rel[d-1]
		}
	}
	out[days*HoursPerDay] = rel[days]
	return out
}
