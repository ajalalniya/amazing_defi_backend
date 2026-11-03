package history

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ShiftDecimal divides a base-10 integer string by 10^decimals exactly, by
// moving the decimal point rather than going through float64. Raw Uniswap
// amount0 values run to 22 digits — past float64's 2^53 exact range — so
// parsing and dividing would round twice and lose the low digits.
//
//	ShiftDecimal("100000000000000", 18) == "0.0001"
//	ShiftDecimal("-332996", 6)          == "-0.332996"
func ShiftDecimal(s string, decimals int) (string, error) {
	s = strings.TrimSpace(s)
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if s == "" {
		return "", fmt.Errorf("not an integer: empty")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return "", fmt.Errorf("not an integer: %q", s)
		}
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0", nil // ±0 normalises to 0
	}
	if decimals <= 0 {
		if neg {
			return "-" + s, nil
		}
		return s, nil
	}
	intPart, frac := "0", ""
	if len(s) <= decimals {
		frac = strings.Repeat("0", decimals-len(s)) + s
	} else {
		intPart, frac = s[:len(s)-decimals], s[len(s)-decimals:]
	}
	out := intPart
	if frac = strings.TrimRight(frac, "0"); frac != "" {
		out = intPart + "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out, nil
}

// RawStats summarises a ConvertRawCSV run.
type RawStats struct {
	Rows     int64
	From     int64
	To       int64
	Unsorted int64
	Skipped  int64
}

// ConvertRawCSV rewrites a raw on-chain swap dump into the store's CSV format.
//
// A raw dump carries amount0/amount1 as unscaled integers and may name the
// time column blockTimestamp; the store wants human token units (see Row) and
// the canonical csvHeader. liquidity, sqrtPriceX96, tick and price are already
// in the store's convention and pass through verbatim, keeping full precision.
//
// Rows stream through one at a time, so a multi-GB dump converts in constant
// memory. Rows whose amounts or timestamp do not parse are skipped and counted.
func ConvertRawCSV(in io.Reader, out io.Writer, spec Spec) (RawStats, error) {
	var st RawStats
	r := csv.NewReader(in)
	r.ReuseRecord = true
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err == io.EOF {
		return st, fmt.Errorf("empty csv")
	}
	if err != nil {
		return st, err
	}
	idx := colIndex(head)
	tsCol := idx["timestamp"]
	if tsCol < 0 {
		tsCol = idx["blocktimestamp"]
	}
	if tsCol < 0 {
		return st, fmt.Errorf("csv needs a timestamp or blockTimestamp column")
	}
	a0Col, a1Col := idx["amount0"], idx["amount1"]
	if a0Col < 0 || a1Col < 0 {
		return st, fmt.Errorf("csv needs amount0 and amount1 columns")
	}

	w := csv.NewWriter(out)
	defer w.Flush()
	if err := w.Write(strings.Split(csvHeader, ",")); err != nil {
		return st, err
	}
	get := func(rec []string, i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return rec[i]
	}
	rec4 := make([]string, 7)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, err
		}
		ts, tsErr := strconv.ParseInt(strings.TrimSpace(get(rec, tsCol)), 10, 64)
		a0, a0Err := ShiftDecimal(get(rec, a0Col), spec.Decimals0)
		a1, a1Err := ShiftDecimal(get(rec, a1Col), spec.Decimals1)
		if tsErr != nil || a0Err != nil || a1Err != nil {
			st.Skipped++
			continue
		}
		if st.Rows == 0 {
			st.From, st.To = ts, ts
		} else {
			if ts < st.To {
				st.Unsorted++
			}
			if ts < st.From {
				st.From = ts
			}
			if ts > st.To {
				st.To = ts
			}
		}
		rec4[0], rec4[1], rec4[2] = a0, a1, get(rec, tsCol)
		rec4[3] = get(rec, idx["liquidity"])
		rec4[4] = get(rec, idx["sqrtpricex96"])
		rec4[5] = get(rec, idx["tick"])
		rec4[6] = get(rec, idx["price"])
		if err := w.Write(rec4); err != nil {
			return st, err
		}
		st.Rows++
	}
	w.Flush()
	return st, w.Error()
}
