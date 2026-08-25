package portfolio

import (
	"context"
	"strconv"
	"sync"
	"time"

	"amazing/internal/market"
)

// Memory is an in-memory PositionStore.
type Memory struct {
	mu     sync.RWMutex
	byID   map[string]market.Position
	seq    int
	demo   bool // include demo seeds for every owner query (mock UX)
}

func NewMemory(seed []market.Position, includeDemo bool) *Memory {
	m := &Memory{byID: make(map[string]market.Position), seq: 100, demo: includeDemo}
	for _, p := range seed {
		m.byID[p.ID] = p
	}
	return m
}

func (m *Memory) List(ctx context.Context, owner string) ([]market.Position, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]market.Position, 0, len(m.byID))
	for _, p := range m.byID {
		if owner == "" || p.Owner == owner || (m.demo && p.Owner == DemoOwner) {
			out = append(out, p)
		}
	}
	// stable order: by OpenedAt then ID (no map-iteration in math paths; this is listing)
	sortPositions(out)
	return out, nil
}

func (m *Memory) Get(ctx context.Context, id string) (market.Position, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.byID[id]
	if !ok {
		return market.Position{}, ErrNotFound
	}
	return p, nil
}

func (m *Memory) Insert(ctx context.Context, p market.Position) (market.Position, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	if p.ID == "" {
		p.ID = "u" + strconv.Itoa(m.seq)
	}
	if p.OpenedAt.IsZero() {
		p.OpenedAt = time.Now().UTC()
	}
	m.byID[p.ID] = p
	return p, nil
}

func (m *Memory) Delete(ctx context.Context, id, owner string) (market.Position, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return market.Position{}, ErrNotFound
	}
	if owner != "" && p.Owner != owner && p.Owner != DemoOwner {
		return market.Position{}, ErrForbidden
	}
	delete(m.byID, id)
	return p, nil
}

func sortPositions(ps []market.Position) {
	for i := 1; i < len(ps); i++ {
		j := i
		for j > 0 && lessPos(ps[j], ps[j-1]) {
			ps[j], ps[j-1] = ps[j-1], ps[j]
			j--
		}
	}
}

func lessPos(a, b market.Position) bool {
	if !a.OpenedAt.Equal(b.OpenedAt) {
		return a.OpenedAt.Before(b.OpenedAt)
	}
	return a.ID < b.ID
}
