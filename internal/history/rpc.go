package history

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type rpcLog struct {
	BlockNumber     string `json:"blockNumber"`
	BlockTimestamp  string `json:"blockTimestamp"`
	Data            string `json:"data"`
	TransactionHash string `json:"transactionHash"`
}

type rpcBlock struct {
	Timestamp string `json:"timestamp"`
}

type rpcClient struct {
	url  string
	http *http.Client
	log  *slog.Logger
	mu   sync.Mutex
	id   int
}

func newRPC(url string, log *slog.Logger) *rpcClient {
	return &rpcClient{
		url:  url,
		http: &http.Client{Timeout: 60 * time.Second},
		log:  log,
	}
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int    `json:"id"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *rpcClient) call(ctx context.Context, method string, params []any, out any) error {
	return c.callN(ctx, method, params, out, 10, 2*time.Second)
}

func (c *rpcClient) callN(ctx context.Context, method string, params []any, out any, attempts int, backoff time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	c.mu.Lock()
	c.id++
	id := c.id
	c.mu.Unlock()
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", Method: method, Params: params, ID: id})
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := c.http.Do(req)
		if err != nil {
			last = err
			if attempt+1 < attempts {
				time.Sleep(backoff)
			}
			continue
		}
		raw, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			last = err
			if attempt+1 < attempts {
				time.Sleep(backoff)
			}
			continue
		}
		if res.StatusCode >= 500 {
			last = fmt.Errorf("rpc status %d", res.StatusCode)
			if attempt+1 < attempts {
				time.Sleep(backoff)
			}
			continue
		}
		var resp rpcResp
		if err := json.Unmarshal(raw, &resp); err != nil {
			last = err
			if attempt+1 < attempts {
				time.Sleep(backoff)
			}
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("rpc %s: %s", method, resp.Error.Message)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(resp.Result, out)
	}
	return fmt.Errorf("rpc %s failed after retries: %w", method, last)
}

func (c *rpcClient) head(ctx context.Context) (uint64, error) {
	var hex string
	if err := c.call(ctx, "eth_blockNumber", []any{}, &hex); err != nil {
		return 0, err
	}
	return parseHexUint(hex)
}

func (c *rpcClient) timestamp(ctx context.Context, block uint64, cache map[uint64]int64) (int64, error) {
	if ts, ok := cache[block]; ok {
		return ts, nil
	}
	var blk rpcBlock
	if err := c.call(ctx, "eth_getBlockByNumber", []any{hexUint(block), false}, &blk); err != nil {
		return 0, err
	}
	n, err := parseHexUint(blk.Timestamp)
	if err != nil {
		return 0, err
	}
	ts := int64(n)
	cache[block] = ts
	return ts, nil
}

func (c *rpcClient) logs(ctx context.Context, addr string, from, to uint64) ([]rpcLog, error) {
	var out []rpcLog
	err := c.call(ctx, "eth_getLogs", []any{map[string]any{
		"address":   addr,
		"fromBlock": hexUint(from),
		"toBlock":   hexUint(to),
		"topics":    []string{SwapTopic},
	}}, &out)
	return out, err
}

func blockAtOrAfter(ctx context.Context, c *rpcClient, target int64, lo, hi uint64, cache map[uint64]int64) (uint64, error) {
	if lo > hi {
		return lo, nil
	}
	tsHi, err := c.timestamp(ctx, hi, cache)
	if err != nil {
		return 0, err
	}
	if tsHi < target {
		return hi, nil
	}
	tsLo, err := c.timestamp(ctx, lo, cache)
	if err != nil {
		return 0, err
	}
	if tsLo >= target {
		return lo, nil
	}
	for lo < hi {
		mid := (lo + hi) / 2
		ts, err := c.timestamp(ctx, mid, cache)
		if err != nil {
			return 0, err
		}
		if ts < target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}

func blockAtOrBefore(ctx context.Context, c *rpcClient, target int64, lo, hi uint64, cache map[uint64]int64) (uint64, error) {
	if lo > hi {
		return hi, nil
	}
	tsLo, err := c.timestamp(ctx, lo, cache)
	if err != nil {
		return 0, err
	}
	if tsLo > target {
		return lo, nil
	}
	tsHi, err := c.timestamp(ctx, hi, cache)
	if err != nil {
		return 0, err
	}
	if tsHi <= target {
		return hi, nil
	}
	for lo < hi {
		mid := (lo + hi + 1) / 2
		ts, err := c.timestamp(ctx, mid, cache)
		if err != nil {
			return 0, err
		}
		if ts > target {
			hi = mid - 1
		} else {
			lo = mid
		}
	}
	return lo, nil
}

func fetchRange(ctx context.Context, c *rpcClient, spec Spec, startTS, endTS int64, onChunk func(done, total uint64, saved int)) ([]Row, error) {
	head, err := c.head(ctx)
	if err != nil {
		return nil, err
	}
	cache := map[uint64]int64{}
	searchLo := spec.StartBlock
	if searchLo == 0 {
		searchLo = 1
	}
	if searchLo > head {
		return nil, nil
	}
	fromBlock, err := blockAtOrAfter(ctx, c, startTS, searchLo, head, cache)
	if err != nil {
		return nil, err
	}
	toBlock, err := blockAtOrBefore(ctx, c, endTS, searchLo, head, cache)
	if err != nil {
		return nil, err
	}
	if fromBlock > toBlock {
		return nil, nil
	}

	addr := spec.Address
	if !strings.HasPrefix(addr, "0x") {
		addr = "0x" + addr
	}
	step := uint64(5000)
	var all []Row
	cur := fromBlock
	total := toBlock - fromBlock + 1
	for cur <= toBlock {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := cur + step - 1
		if end > toBlock {
			end = toBlock
		}
		logs, err := c.logs(ctx, addr, cur, end)
		if err != nil {
			msg := strings.ToLower(err.Error())
			if step > 100 && (strings.Contains(msg, "too many") || strings.Contains(msg, "limit") || strings.Contains(msg, "range")) {
				step = maxU64(step/2, 100)
				continue
			}
			return nil, err
		}
		for _, lg := range logs {
			block, err := parseHexUint(lg.BlockNumber)
			if err != nil {
				continue
			}
			ts := int64(0)
			if lg.BlockTimestamp != "" {
				if n, err := parseHexUint(lg.BlockTimestamp); err == nil {
					ts = int64(n)
				}
			}
			if ts == 0 {
				ts, err = c.timestamp(ctx, block, cache)
				if err != nil {
					continue
				}
			}
			if ts < startTS || ts > endTS {
				continue
			}
			data, err := hexBytes(lg.Data)
			if err != nil {
				if c.log != nil {
					c.log.Warn("swap decode", "tx", lg.TransactionHash, "err", err)
				}
				continue
			}
			a0, a1, sqrt, liq, tick, err := decodeSwapData(data)
			if err != nil {
				continue
			}
			price := SqrtPriceX96ToPrice(sqrt, spec.Decimals0, spec.Decimals1)
			if spec.InvertPrice && price != 0 {
				price = 1 / price
			}
			all = append(all, Row{
				Amount0:      scaleAmount(a0, spec.Decimals0),
				Amount1:      scaleAmount(a1, spec.Decimals1),
				Timestamp:    ts,
				Liquidity:    bigToFloat(liq),
				SqrtPriceX96: sqrt.String(),
				Tick:         int(tick),
				Price:        price,
			})
		}
		if c.log != nil {
			c.log.Info("fetched swaps", "from", cur, "to", end, "n", len(logs), "kept", len(all))
		}
		if onChunk != nil {
			onChunk(end-fromBlock+1, total, len(all))
		}
		cur = end + 1
	}
	return all, nil
}

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
