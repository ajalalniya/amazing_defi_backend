package history

import (
	"fmt"
	"os"
	"time"
)

// dailyCloses is one CSV's close for every UTC day it spans, remembered until
// the file changes on disk.
type dailyCloses struct {
	modTime  time.Time
	size     int64
	firstDay int64 // UTC midnight of closes[0]
	closes   []float64
}

// DailyCloseAt is a pool's close on the UTC day of date, read from the swap
// CSV on disk only: it never downloads, so a date outside the CSV reports
// ok=false. It is the same close an on-chain backtest from that date opens
// its LP legs at. Loading a CSV takes seconds, so the closes are cached per
// file until its size or modification time changes.
func (s *Store) DailyCloseAt(poolID string, date time.Time) (price float64, ok bool, err error) {
	spec, found := SpecFor(poolID)
	if !found {
		return 0, false, nil
	}
	path := s.csvPath(spec)
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}

	key := spec.FileKey()
	s.mu.Lock()
	c, cached := s.closes[key]
	s.mu.Unlock()
	if !cached || !c.modTime.Equal(st.ModTime()) || c.size != st.Size() {
		// The pool lock keeps this from racing a backtest appending to the CSV.
		lk := s.poolLock(key)
		lk.Lock()
		rows, err := loadCSV(path)
		lk.Unlock()
		if err != nil {
			return 0, false, err
		}
		c = dailyCloses{modTime: st.ModTime(), size: st.Size()}
		if len(rows) > 0 {
			c.firstDay = rows[0].Timestamp - rows[0].Timestamp%86400
			c.closes = DailyCloses(rows, c.firstDay, rows[len(rows)-1].Timestamp)
		}
		s.mu.Lock()
		if s.closes == nil {
			s.closes = map[string]dailyCloses{}
		}
		s.closes[key] = c
		s.mu.Unlock()
	}

	day := date.UTC().Truncate(24 * time.Hour).Unix()
	i := (day - c.firstDay) / 86400
	if day < c.firstDay || i >= int64(len(c.closes)) || c.closes[i] <= 0 {
		return 0, false, nil
	}
	return c.closes[i], true, nil
}

// ParseDay parses a YYYY-MM-DD date as UTC midnight.
func ParseDay(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q, want YYYY-MM-DD", s)
	}
	return t, nil
}
