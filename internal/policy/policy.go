package policy

import (
	"context"
	"errors"
	"sync"
	"time"

	"amazing/internal/signer"
)

var ErrRejected = errors.New("policy: rejected")

type Decision struct {
	Allow   bool     `json:"allow"`
	Reasons []string `json:"reasons,omitempty"`
}

type Limits struct {
	AllowlistedTo        map[signer.Address]map[string]bool // to → selector
	MaxNotionalPerAction map[string]float64                 // owner → USD
	MaxTxPerHour         int
	MaxGasPerDayWei      *int64
	PinnedChainIDs       map[int64]bool
}

type Guard struct {
	mu         sync.Mutex
	kill       bool
	killReason string
	limits     Limits
	window     []time.Time
}

func NewGuard(limits Limits) *Guard {
	return &Guard{limits: limits}
}

func (g *Guard) Trip(reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.kill = true
	g.killReason = reason
}

func (g *Guard) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.kill = false
	g.killReason = ""
}

func (g *Guard) Tripped() (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.kill, g.killReason
}

// Check is the pre-signing guard. The signer must refuse anything that has
// not been marked Policed after a successful Check.
func (g *Guard) Check(ctx context.Context, tx *signer.Tx, owner string, notional float64, selector string) (Decision, error) {
	_ = ctx
	g.mu.Lock()
	defer g.mu.Unlock()
	d := Decision{Allow: true}
	if g.kill {
		d.Allow = false
		d.Reasons = append(d.Reasons, "kill switch: "+g.killReason)
		return d, nil
	}
	if tx == nil {
		return Decision{Allow: false, Reasons: []string{"nil tx"}}, nil
	}
	if len(g.limits.PinnedChainIDs) > 0 && tx.ChainID != nil && !g.limits.PinnedChainIDs[tx.ChainID.Int64()] {
		d.Allow = false
		d.Reasons = append(d.Reasons, "chain id not pinned")
	}
	if len(g.limits.AllowlistedTo) > 0 {
		sels, ok := g.limits.AllowlistedTo[tx.To]
		if !ok || (selector != "" && !sels[selector]) {
			d.Allow = false
			d.Reasons = append(d.Reasons, "target or selector not allowlisted")
		}
	}
	if cap, ok := g.limits.MaxNotionalPerAction[owner]; ok && notional > cap {
		d.Allow = false
		d.Reasons = append(d.Reasons, "notional cap exceeded")
	}
	if g.limits.MaxTxPerHour > 0 {
		cutoff := time.Now().Add(-time.Hour)
		n := 0
		kept := g.window[:0]
		for _, ts := range g.window {
			if ts.After(cutoff) {
				kept = append(kept, ts)
				n++
			}
		}
		g.window = kept
		if n >= g.limits.MaxTxPerHour {
			d.Allow = false
			d.Reasons = append(d.Reasons, "tx/hour rate limit")
		}
	}
	if d.Allow {
		tx.Policed = true
		g.window = append(g.window, time.Now())
	}
	return d, nil
}
