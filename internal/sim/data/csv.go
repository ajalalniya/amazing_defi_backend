// Package data supplies price bars: OHLCV CSV files, bars built from swap
// trades, and synthetic GBM and regime-switching GBM paths.
//
// Every series it returns is evenly spaced: CSV data is resampled to the bar
// size and gaps are filled with flat bars (close carried forward, zero
// volume), because detectors count lookbacks in bars.
package data

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"amazing/internal/sim/core"
)

// LoadCSV reads an OHLCV file and resamples it to barSize. See ReadCSV.
func LoadCSV(path string, barSize time.Duration) ([]core.Bar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("load bars: %w", err)
	}
	defer f.Close()
	bars, err := ReadCSV(f, barSize)
	if err != nil {
		return nil, fmt.Errorf("load bars %s: %w", path, err)
	}
	return bars, nil
}

// ReadCSV parses an OHLCV CSV with a header row. Columns (case-insensitive):
//
//	timestamp  required: RFC 3339, "2006-01-02 15:04:05" (UTC), or unix
//	           seconds / milliseconds
//	close      required
//	open, high, low   optional, default close
//	volume     optional, USD
//	liquidity  optional, the pool's active liquidity in human units
//	iv         optional, annualised implied vol
//	regime     optional, the true regime of a synthetic series
//
// Rows must be in time order. Bars are resampled to barSize (first open, max
// high, min low, last close, summed volume) and gaps filled flat.
func ReadCSV(r io.Reader, barSize time.Duration) ([]core.Bar, error) {
	if barSize <= 0 {
		return nil, errors.New("bar size must be > 0")
	}
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	tsCol, ok := col["timestamp"]
	if !ok {
		if tsCol, ok = col["time"]; !ok {
			return nil, errors.New(`header needs a "timestamp" column`)
		}
	}
	closeCol, ok := col["close"]
	if !ok {
		return nil, errors.New(`header needs a "close" column`)
	}
	get := func(rec []string, name string, line int) (float64, bool, error) {
		i, ok := col[name]
		if !ok || i >= len(rec) || strings.TrimSpace(rec[i]) == "" {
			return 0, false, nil
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rec[i]), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false, fmt.Errorf("line %d: %s %q is not a number", line, name, rec[i])
		}
		return v, true, nil
	}
	var raw []core.Bar
	line := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if tsCol >= len(rec) || closeCol >= len(rec) {
			return nil, fmt.Errorf("line %d: missing timestamp or close", line)
		}
		t, err := parseTime(rec[tsCol])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		b := core.Bar{Time: t, TrueRegime: core.NoRegime}
		var have bool
		if b.Close, have, err = get(rec, "close", line); err != nil || !have || b.Close <= 0 {
			if err == nil {
				err = fmt.Errorf("line %d: close must be a positive number", line)
			}
			return nil, err
		}
		b.Open, b.High, b.Low = b.Close, b.Close, b.Close
		for _, f := range []struct {
			name string
			dst  *float64
		}{{"open", &b.Open}, {"high", &b.High}, {"low", &b.Low}, {"volume", &b.Volume}, {"liquidity", &b.Liquidity}, {"iv", &b.IV}} {
			v, ok, err := get(rec, f.name, line)
			if err != nil {
				return nil, err
			}
			if ok {
				*f.dst = v
			}
		}
		if i, ok := col["regime"]; ok && i < len(rec) {
			if b.TrueRegime, err = core.ParseRegime(rec[i]); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
		}
		if n := len(raw); n > 0 && t.Before(raw[n-1].Time) {
			return nil, fmt.Errorf("line %d: timestamp %s is before the previous row; rows must be in time order", line, t.Format(time.RFC3339))
		}
		raw = append(raw, b)
	}
	if len(raw) == 0 {
		return nil, errors.New("no rows")
	}
	return Resample(raw, barSize), nil
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			return time.UnixMilli(n).UTC(), nil
		}
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("timestamp %q: want RFC 3339, \"2006-01-02 15:04:05\" or unix seconds", s)
}

// Resample aggregates time-ordered bars into evenly spaced bars of size d,
// stamped at each bucket's start, and fills empty buckets with flat bars at
// the previous close. Liquidity, IV and the true regime take the bucket's
// last value.
func Resample(in []core.Bar, d time.Duration) []core.Bar {
	if len(in) == 0 {
		return nil
	}
	start := in[0].Time.Truncate(d)
	last := in[len(in)-1].Time.Truncate(d)
	n := int(last.Sub(start)/d) + 1
	out := make([]core.Bar, 0, n)
	i := 0
	prev := in[0]
	for k := 0; k < n; k++ {
		t0 := start.Add(time.Duration(k) * d)
		t1 := t0.Add(d)
		b := core.Bar{Time: t0, Open: prev.Close, High: prev.Close, Low: prev.Close, Close: prev.Close,
			Liquidity: prev.Liquidity, IV: prev.IV, TrueRegime: prev.TrueRegime}
		first := true
		for i < len(in) && in[i].Time.Before(t1) {
			x := in[i]
			if first {
				b.Open, b.High, b.Low = x.Open, x.High, x.Low
				first = false
			}
			b.High = math.Max(b.High, x.High)
			b.Low = math.Min(b.Low, x.Low)
			b.Close = x.Close
			b.Volume += x.Volume
			if x.Liquidity > 0 {
				b.Liquidity = x.Liquidity
			}
			if x.IV > 0 {
				b.IV = x.IV
			}
			b.TrueRegime = x.TrueRegime
			prev = x
			i++
		}
		out = append(out, b)
	}
	return out
}

// Trade is one swap: its time, price (USDC per ETH), USD volume and the
// pool's active liquidity after it (human units, 0 if unknown).
type Trade struct {
	Time      time.Time
	Price     float64
	VolumeUSD float64
	Liquidity float64
}

// FromTrades builds evenly spaced OHLCV bars of size d from time-ordered
// trades.
func FromTrades(trades []Trade, d time.Duration) []core.Bar {
	raw := make([]core.Bar, 0, len(trades))
	for _, t := range trades {
		if t.Price <= 0 {
			continue
		}
		raw = append(raw, core.Bar{Time: t.Time, Open: t.Price, High: t.Price, Low: t.Price, Close: t.Price,
			Volume: t.VolumeUSD, Liquidity: t.Liquidity, TrueRegime: core.NoRegime})
	}
	return Resample(raw, d)
}

// WriteCSV writes bars in the format ReadCSV reads.
func WriteCSV(w io.Writer, bars []core.Bar) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"timestamp", "open", "high", "low", "close", "volume", "liquidity", "iv", "regime"}); err != nil {
		return err
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	rec := make([]string, 9)
	for _, b := range bars {
		rec[0] = b.Time.UTC().Format(time.RFC3339)
		rec[1], rec[2], rec[3], rec[4] = f(b.Open), f(b.High), f(b.Low), f(b.Close)
		rec[5], rec[6], rec[7] = f(b.Volume), f(b.Liquidity), f(b.IV)
		rec[8] = ""
		if b.TrueRegime.Valid() {
			rec[8] = b.TrueRegime.String()
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
