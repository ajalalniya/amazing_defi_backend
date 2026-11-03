package history

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Store keeps per-pool swap CSVs and fills timestamp gaps from RPC.
type Store struct {
	Dir     string
	RPCs    map[int]string
	log     *slog.Logger
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	clients map[int]*rpcClient
	fetchFn func(ctx context.Context, spec Spec, start, end int64) ([]Row, error)
	Graph   *Graph
	snaps   map[string]snapCache
	closes  map[string]dailyCloses
}

func New(dir string, rpcs map[int]string, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	if dir == "" {
		dir = "data"
	}
	if rpcs == nil {
		rpcs = map[int]string{}
	}
	_ = os.MkdirAll(dir, 0o755)
	return &Store{Dir: dir, RPCs: rpcs, log: log, locks: map[string]*sync.Mutex{}, clients: map[int]*rpcClient{}, snaps: map[string]snapCache{}, closes: map[string]dailyCloses{}}
}

func (s *Store) poolLock(key string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[key] == nil {
		s.locks[key] = &sync.Mutex{}
	}
	return s.locks[key]
}

// Window is the on-chain series used by a historical backtest.
type Window struct {
	Spec        Spec
	Swaps       []Row
	DailyPrices []float64
	From        int64
	To          int64
}

// Ensure loads the pair CSV and fetches any timestamps not yet on disk.
func (s *Store) Ensure(ctx context.Context, poolID string, fromTS, toTS int64) (Window, error) {
	spec, ok := SpecFor(poolID)
	if !ok {
		return Window{}, fmt.Errorf("no on-chain pool mapping for %s (historical mode supports ETH/USDC 0.05%% on Base)", poolID)
	}
	// An RPC is only needed to fill gaps. Checking for one up front made a
	// window the CSV already covers fail with no configured RPC; fetchGap
	// surfaces the same error if a gap actually has to be downloaded.
	if fromTS > toTS {
		return Window{}, fmt.Errorf("invalid window")
	}

	lk := s.poolLock(spec.FileKey())
	lk.Lock()
	defer lk.Unlock()

	path := s.csvPath(spec)
	// Loading a large CSV takes seconds; say so, or the UI sits on its first
	// message looking like a stalled download.
	Report(ctx, Progress{Stage: "load", Percent: 3, Message: "Reading swaps from disk"})
	existing, err := loadCSV(path)
	if err != nil {
		return Window{}, err
	}
	b := boundsOf(existing)
	gaps := MissingRanges(b, fromTS, toTS)
	// One message for the outcome: the stream throttles reports that arrive
	// within 120ms of each other, and a follow-up would be dropped.
	if len(gaps) == 0 {
		Report(ctx, Progress{
			Stage: "load", Percent: 88,
			Message: "Using swaps on disk — no download needed",
			Detail:  fmt.Sprintf("%s · %d swaps loaded", poolID, len(existing)),
		})
	} else {
		Report(ctx, Progress{
			Stage: "download", Percent: 5,
			Message: fmt.Sprintf("%d missing range(s) to download", len(gaps)),
			Detail:  poolID,
		})
	}
	for i, g := range gaps {
		if err := ctx.Err(); err != nil {
			return Window{}, err
		}
		lo, hi := gapPercent(i, len(gaps))
		s.log.Info("fetching missing swaps", "pool", poolID, "from", g[0], "to", g[1], "had", b)
		Report(ctx, Progress{
			Stage: "download", Percent: lo,
			Message: "Downloading swaps",
			Detail:  fmt.Sprintf("segment %d/%d", i+1, len(gaps)),
		})
		fresh, err := s.fetchGap(ctx, spec, g[0], g[1], func(done, total uint64, saved int) {
			pct := lo
			if total > 0 {
				pct = lo + int(float64(hi-lo)*float64(done)/float64(total))
			}
			Report(ctx, Progress{
				Stage: "download", Percent: pct,
				Message: "Downloading swaps",
				Detail:  fmt.Sprintf("blocks %d / %d · %d swaps saved", done, total, saved),
			})
		})
		if err != nil {
			return Window{}, fmt.Errorf("rpc fetch %d..%d: %w", g[0], g[1], err)
		}
		if err := appendCSV(path, fresh); err != nil {
			return Window{}, err
		}
		existing = mergeRows(existing, fresh)
	}
	if len(gaps) > 0 {
		existing = mergeRows(existing, nil)
		if err := writeCSV(path, existing); err != nil {
			return Window{}, err
		}
	}

	inWin := filterWindow(existing, fromTS, toTS)
	if len(inWin) == 0 {
		return Window{}, fmt.Errorf("no swap data for %s in %d..%d after fetch", poolID, fromTS, toTS)
	}
	Report(ctx, Progress{Stage: "load", Percent: 90, Message: "Building daily prices"})
	daily := DailyCloses(inWin, fromTS, toTS)
	return Window{Spec: spec, Swaps: inWin, DailyPrices: daily, From: fromTS, To: toTS}, nil
}

func gapPercent(i, n int) (lo, hi int) {
	if n <= 1 {
		return 8, 88
	}
	span := 80
	lo = 8 + span*i/n
	hi = 8 + span*(i+1)/n
	return lo, hi
}

func (s *Store) rpcFor(chainID int) (*rpcClient, error) {
	url := strings.TrimSpace(s.RPCs[chainID])
	if url == "" {
		return nil, fmt.Errorf("%s is not set for chain %d (%s)", RPCEnv(chainID), chainID, ChainName(chainID))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[chainID] == nil {
		s.clients[chainID] = newRPC(url, s.log)
	}
	return s.clients[chainID], nil
}

func (s *Store) fetchGap(ctx context.Context, spec Spec, start, end int64, onChunk func(done, total uint64, saved int)) ([]Row, error) {
	if s.fetchFn != nil {
		return s.fetchFn(ctx, spec, start, end)
	}
	c, err := s.rpcFor(spec.ChainID)
	if err != nil {
		return nil, err
	}
	return fetchRange(ctx, c, spec, start, end, onChunk)
}

func filterWindow(rows []Row, from, to int64) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if r.Timestamp >= from && r.Timestamp <= to {
			out = append(out, r)
		}
	}
	return out
}

// DailyCloses returns days+1 prices (UTC day buckets from fromTS through toTS).
func DailyCloses(rows []Row, fromTS, toTS int64) []float64 {
	if toTS < fromTS {
		return nil
	}
	days := int((toTS-fromTS)/86400) + 1
	out := make([]float64, days)
	var last float64
	if len(rows) > 0 {
		last = rows[0].Price
	}
	ri := 0
	for d := 0; d < days; d++ {
		dayEnd := fromTS + int64(d+1)*86400 - 1
		if d == days-1 {
			dayEnd = toTS
		}
		for ri < len(rows) && rows[ri].Timestamp <= dayEnd {
			if rows[ri].Price > 0 {
				last = rows[ri].Price
			}
			ri++
		}
		out[d] = last
	}
	return out
}
