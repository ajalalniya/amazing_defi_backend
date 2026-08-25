package market

import (
	"log/slog"
	"strings"
	"sync"
)

var unknownQuotes sync.Map

// QuoteUSD is the USD price of one unit of a pool's quote token (token1).
// USDC/USDT → 1; ETH → ETH mark; WBTC → BTC mark. Unknown tokens fall back
// to 1 after a single warn log — the prototype silently returned 1.
func QuoteUSD(token1 string, ethMark, btcMark float64, log *slog.Logger) float64 {
	switch strings.ToUpper(token1) {
	case "USDC", "USDT", "USD", "DAI":
		return 1
	case "ETH", "WETH":
		if ethMark > 0 {
			return ethMark
		}
		return 1
	case "WBTC", "BTC":
		if btcMark > 0 {
			return btcMark
		}
		return 1
	default:
		if log != nil {
			if _, loaded := unknownQuotes.LoadOrStore(token1, true); !loaded {
				log.Warn("unknown quote token, falling back to 1", "token", token1)
			}
		}
		return 1
	}
}

// MarksFromPerps pulls ETH and BTC USD marks from the perp list.
func MarksFromPerps(perps []Perp) (eth, btc float64) {
	for _, p := range perps {
		switch p.ID {
		case "ETH-USD":
			eth = p.Price
		case "BTC-USD":
			btc = p.Price
		}
	}
	return eth, btc
}
