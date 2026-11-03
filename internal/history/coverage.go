package history

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Coverage is the span of swap data already on disk for one pool. It is read
// from the CSV's first and last rows only, so reporting it costs two small
// reads rather than parsing the whole file.
type Coverage struct {
	PoolID string `json:"poolId"`
	// PoolIDs is every amazing market id bound to this pool address. One
	// address carries several ids (v3 and v4 alias the same Base pool), and a
	// leg may name any of them.
	PoolIDs []string `json:"poolIds"`
	Pair    string   `json:"pair"`
	ChainID int      `json:"chainId"`
	FromTS  int64    `json:"fromTs"`
	ToTS    int64    `json:"toTs"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	// FullFrom/FullTo bound the UTC days the CSV covers end to end. The first
	// and last days of a dump are usually partial, and a backtest over a
	// partial day silently under-counts its swaps, so these are the dates the
	// UI should offer.
	FullFrom string `json:"fullFrom"`
	FullTo   string `json:"fullTo"`
	Days     int    `json:"days"`
	Bytes    int64  `json:"bytes"`
}

const isoDay = "2006-01-02"

// Coverage reports what each bound pool has on disk, newest span first. Pools
// with no CSV, or whose CSV holds no complete UTC day, are left out.
func (s *Store) Coverage() []Coverage {
	out := make([]Coverage, 0, 4)
	for _, b := range UniqueBindings() {
		path := s.csvPath(b.Spec)
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		from, to, err := csvBounds(path)
		if err != nil {
			s.log.Warn("coverage: unreadable csv", "pool", b.ID, "path", path, "err", err)
			continue
		}
		c, ok := coverageOf(b, from, to)
		if !ok {
			continue
		}
		c.Bytes = fi.Size()
		out = append(out, c)
	}
	return out
}

func coverageOf(b Binding, from, to int64) (Coverage, bool) {
	if to < from {
		return Coverage{}, false
	}
	// First UTC midnight at or after `from`.
	fullFrom := from / 86400 * 86400
	if fullFrom < from {
		fullFrom += 86400
	}
	// Last UTC day whose final second is at or before `to`.
	fullTo := int64(-1)
	if to >= 86399 {
		fullTo = (to - 86399) / 86400 * 86400
	}
	if fullTo < fullFrom {
		return Coverage{}, false
	}
	return Coverage{
		PoolID:   b.ID,
		PoolIDs:  idsForAddress(b.Spec.Address),
		Pair:     b.Spec.Token0Sym + "/" + b.Spec.Token1Sym,
		ChainID:  b.Spec.ChainID,
		FromTS:   from,
		ToTS:     to,
		From:     time.Unix(from, 0).UTC().Format(isoDay),
		To:       time.Unix(to, 0).UTC().Format(isoDay),
		FullFrom: time.Unix(fullFrom, 0).UTC().Format(isoDay),
		FullTo:   time.Unix(fullTo, 0).UTC().Format(isoDay),
		Days:     int((fullTo-fullFrom)/86400) + 1,
	}, true
}

// idsForAddress lists every amazing market id bound to one pool address.
func idsForAddress(addr string) []string {
	want := strings.ToLower(addr)
	out := []string{}
	for _, b := range Bindings() {
		if strings.ToLower(b.Spec.Address) == want {
			out = append(out, b.ID)
		}
	}
	return out
}

// tailBytes is how much of the file's end to read when looking for the last
// row. Store rows are well under 200 bytes, so this always spans several.
const tailBytes = 1 << 16

// csvBounds returns the first and last timestamps in a store-format CSV
// without reading the body. Rows are written in timestamp order, so the first
// and last data rows bound the file.
func csvBounds(path string) (int64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	head, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return 0, 0, err
	}
	col := colIndex(strings.Split(strings.TrimRight(head, "\r\n"), ","))
	tsCol := col["timestamp"]
	if tsCol < 0 {
		tsCol = col["blocktimestamp"]
	}
	if tsCol < 0 {
		return 0, 0, fmt.Errorf("no timestamp column")
	}

	first, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return 0, 0, err
	}
	fromTS, err := tsOfLine(first, tsCol)
	if err != nil {
		return 0, 0, fmt.Errorf("first row: %w", err)
	}

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, 0, err
	}
	at := size - tailBytes
	if at < 0 {
		at = 0
	}
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		return 0, 0, err
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return 0, 0, err
	}
	// Drop the leading fragment when the read started mid-row; whatever is
	// left ends with the file's final complete row.
	lines := strings.Split(strings.TrimRight(string(tail), "\r\n"), "\n")
	if at > 0 && len(lines) > 1 {
		lines = lines[1:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if toTS, err := tsOfLine(lines[i], tsCol); err == nil {
			if toTS < fromTS {
				return 0, 0, fmt.Errorf("csv not in timestamp order: %d before %d", toTS, fromTS)
			}
			return fromTS, toTS, nil
		}
	}
	return 0, 0, fmt.Errorf("no readable last row")
}

func tsOfLine(line string, tsCol int) (int64, error) {
	f := strings.Split(strings.TrimRight(line, "\r\n"), ",")
	if tsCol >= len(f) {
		return 0, fmt.Errorf("only %d columns", len(f))
	}
	return strconv.ParseInt(strings.TrimSpace(f[tsCol]), 10, 64)
}
