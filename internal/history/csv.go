package history

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const csvHeader = "amount0,amount1,timestamp,liquidity,sqrtPriceX96,tick,price"

func (s *Store) csvPath(spec Spec) string {
	return filepath.Join(s.Dir, "swaps_"+spec.FileKey()+".csv")
}

func loadCSV(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return parseCSV(f)
}

// parseCSV reads store-format swap rows. Column order is taken from the
// header, and blockTimestamp is accepted as an alias for timestamp so raw
// on-chain dumps converted by ConvertRawCSV load unchanged.
func parseCSV(in io.Reader) ([]Row, error) {
	r := csv.NewReader(in)
	r.ReuseRecord = true
	head, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	idx := colIndex(head)
	tsCol := idx["timestamp"]
	if tsCol < 0 {
		tsCol = idx["blocktimestamp"]
	}
	if tsCol < 0 {
		return nil, fmt.Errorf("csv: need a timestamp or blockTimestamp column")
	}
	var rows []Row
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		ts, err := strconv.ParseInt(rec[tsCol], 10, 64)
		if err != nil {
			continue
		}
		row := Row{Timestamp: ts}
		if i := idx["amount0"]; i >= 0 {
			row.Amount0, _ = strconv.ParseFloat(rec[i], 64)
		}
		if i := idx["amount1"]; i >= 0 {
			row.Amount1, _ = strconv.ParseFloat(rec[i], 64)
		}
		if i := idx["liquidity"]; i >= 0 {
			row.Liquidity, _ = strconv.ParseFloat(rec[i], 64)
		}
		if i := idx["sqrtpricex96"]; i >= 0 {
			row.SqrtPriceX96 = rec[i]
		}
		if i := idx["tick"]; i >= 0 {
			t, _ := strconv.Atoi(rec[i])
			row.Tick = t
		}
		if i := idx["price"]; i >= 0 {
			row.Price, _ = strconv.ParseFloat(rec[i], 64)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func colIndex(head []string) map[string]int {
	out := map[string]int{
		"amount0": -1, "amount1": -1, "timestamp": -1, "blocktimestamp": -1,
		"liquidity": -1, "sqrtpricex96": -1, "tick": -1, "price": -1,
	}
	for i, h := range head {
		out[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return out
}

func boundsOf(rows []Row) *[2]int64 {
	if len(rows) == 0 {
		return nil
	}
	lo, hi := rows[0].Timestamp, rows[0].Timestamp
	for _, r := range rows {
		if r.Timestamp < lo {
			lo = r.Timestamp
		}
		if r.Timestamp > hi {
			hi = r.Timestamp
		}
	}
	return &[2]int64{lo, hi}
}

func mergeRows(a, b []Row) []Row {
	all := append(append([]Row{}, a...), b...)
	sort.Slice(all, func(i, j int) bool {
		if all[i].Timestamp != all[j].Timestamp {
			return all[i].Timestamp < all[j].Timestamp
		}
		return all[i].SqrtPriceX96 < all[j].SqrtPriceX96
	})
	out := all[:0]
	var last Row
	has := false
	for _, r := range all {
		if has && r.Timestamp == last.Timestamp && r.SqrtPriceX96 == last.SqrtPriceX96 &&
			r.Amount0 == last.Amount0 && r.Amount1 == last.Amount1 {
			continue
		}
		out = append(out, r)
		last = r
		has = true
	}
	return out
}

func writeCSV(path string, rows []Row) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.Write(strings.Split(csvHeader, ",")); err != nil {
		f.Close()
		return err
	}
	for _, r := range rows {
		rec := []string{
			fmtFloat(r.Amount0),
			fmtFloat(r.Amount1),
			strconv.FormatInt(r.Timestamp, 10),
			fmtFloat(r.Liquidity),
			r.SqrtPriceX96,
			strconv.Itoa(r.Tick),
			fmtFloat(r.Price),
		}
		if err := w.Write(rec); err != nil {
			f.Close()
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fmtFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', 16, 64)
}

func appendCSV(path string, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := os.Stat(path)
	first := os.IsNotExist(err)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if first {
		if err := w.Write(strings.Split(csvHeader, ",")); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if err := w.Write([]string{
			fmtFloat(r.Amount0),
			fmtFloat(r.Amount1),
			strconv.FormatInt(r.Timestamp, 10),
			fmtFloat(r.Liquidity),
			r.SqrtPriceX96,
			strconv.Itoa(r.Tick),
			fmtFloat(r.Price),
		}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// LoadSwaps reads a swap CSV in the store's format (or a raw dump whose time
// column is blockTimestamp). A missing file is an error here, unlike inside
// the store, where it just means nothing has been fetched yet.
func LoadSwaps(path string) ([]Row, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return loadCSV(path)
}

// CSVPath is where the store keeps a pool's swaps.
func (s *Store) CSVPath(spec Spec) string { return s.csvPath(spec) }
