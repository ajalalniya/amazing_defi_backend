package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Hub struct {
	log     *slog.Logger
	srv     *Server
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

type wsClient struct {
	send   chan []byte
	owner  string
	cancel context.CancelFunc
}

func NewHub(log *slog.Logger, srv *Server) *Hub {
	return &Hub{log: log, srv: srv, clients: map[*wsClient]struct{}{}}
}

func (h *Hub) Start(ctx context.Context) {
	tick := time.NewTicker(2500 * time.Millisecond)
	ping := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			h.Drain()
			return
		case <-tick.C:
			h.push(ctx)
		case <-ping.C:
			h.broadcastRaw(nil) // ping via empty: clients ping themselves
		}
	}
}

func (h *Hub) Drain() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		c.cancel()
		delete(h.clients, c)
	}
}

func (h *Hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	n := len(h.clients)
	h.mu.Unlock()
	h.srv.metrics.wsClients.Store(int64(n))
}

func (h *Hub) remove(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	n := len(h.clients)
	h.mu.Unlock()
	h.srv.metrics.wsClients.Store(int64(n))
}

func (h *Hub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- payload:
		default:
			c.cancel()
		}
	}
}

func (h *Hub) broadcastRaw(_ []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- nil: // nil means ping
		default:
			c.cancel()
		}
	}
}

func (h *Hub) push(ctx context.Context) {
	pools, err := h.srv.provider.Pools(ctx)
	if err != nil {
		return
	}
	perps, err := h.srv.provider.Perps(ctx)
	if err != nil {
		return
	}
	ticks := make([]map[string]any, 0, len(pools)+len(perps))
	for _, p := range perps {
		ticks = append(ticks, map[string]any{"id": p.ID, "price": p.Price, "change24h": p.Change})
	}
	for _, p := range pools {
		ticks = append(ticks, map[string]any{"id": p.ID, "price": p.Price, "change24h": p.Change})
	}
	priceFrame, _ := json.Marshal(map[string]any{"type": "prices", "data": ticks})
	h.broadcast(priceFrame)

	h.mu.Lock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		snap, err := h.srv.snapshot(ctx, c.owner)
		if err != nil {
			continue
		}
		frame, _ := json.Marshal(map[string]any{"type": "portfolio", "data": snap.Rollups})
		select {
		case c.send <- frame:
		default:
			c.cancel()
		}
	}
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		s.log.Warn("ws accept", "err", err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	cl := &wsClient{send: make(chan []byte, 16), owner: r.URL.Query().Get("owner"), cancel: cancel}
	s.hub.add(cl)

	go func() {
		defer func() {
			s.hub.remove(cl)
			cancel()
			_ = c.Close(websocket.StatusNormalClosure, "")
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-cl.send:
				if !ok {
					return
				}
				wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
				if msg == nil {
					err = c.Ping(wctx)
				} else {
					err = c.Write(wctx, websocket.MessageText, msg)
				}
				wcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	// honour client close / context cancellation
	go func() {
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()
}
