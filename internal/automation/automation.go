package automation

import (
	"context"
	"sync"
	"time"
)

type Kind string

const (
	Rebalance Kind = "rebalance"
	AutoHedge Kind = "auto-hedge"
	Harvest   Kind = "harvest"
	StopLoss  Kind = "stop-loss"
)

type Strategy struct {
	ID          string         `json:"id"`
	Kind        Kind           `json:"kind"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Params      map[string]any `json:"params"`
}

func Catalog() []Strategy {
	return []Strategy{
		{ID: "rebalance", Kind: Rebalance, Title: "Rebalance LP range", Description: "Mint a new range and burn the old when price leaves the current band.", Params: map[string]any{"widthPct": map[string]any{"type": "number", "default": 10}}},
		{ID: "auto-hedge", Kind: AutoHedge, Title: "Auto-hedge LP delta", Description: "Open or resize a GMX short when LP delta drifts past a threshold.", Params: map[string]any{"thresholdPct": map[string]any{"type": "number", "default": 15}, "leverage": map[string]any{"type": "number", "default": 3}}},
		{ID: "harvest", Kind: Harvest, Title: "Harvest and compound", Description: "Collect accrued fees on a schedule and add them back to the position.", Params: map[string]any{"cron": map[string]any{"type": "string", "default": "0 0 * * *"}}},
		{ID: "stop-loss", Kind: StopLoss, Title: "Stop-loss / take-profit", Description: "Close or de-lever a perp when liquidation distance narrows past a limit.", Params: map[string]any{"liqDistancePct": map[string]any{"type": "number", "default": 8}}},
	}
}

type Job struct {
	ID        string         `json:"id"`
	Owner     string         `json:"owner"`
	Strategy  string         `json:"strategy"`
	Paused    bool           `json:"paused"`
	Params    map[string]any `json:"params"`
	CreatedAt time.Time      `json:"createdAt"`
}

type Run struct {
	ID        string    `json:"id"`
	JobID     string    `json:"jobId"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"` // "evaluation" | "execution"
	Fired     bool      `json:"fired"`
	Reason    string    `json:"reason"`
	TxHash    string    `json:"txHash,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
}

type Store interface {
	List(ctx context.Context, owner string) ([]Job, error)
	Get(ctx context.Context, id string) (Job, error)
	Insert(ctx context.Context, j Job) (Job, error)
	Patch(ctx context.Context, j Job) (Job, error)
	Delete(ctx context.Context, id string) error
	Runs(ctx context.Context, jobID string) ([]Run, error)
	AppendRun(ctx context.Context, r Run) error
}

type Memory struct {
	mu   sync.Mutex
	jobs map[string]Job
	runs map[string][]Run
}

func NewMemory() *Memory {
	return &Memory{jobs: map[string]Job{}, runs: map[string][]Run{}}
}

func (m *Memory) List(_ context.Context, owner string) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Job{}
	for _, j := range m.jobs {
		if owner == "" || j.Owner == owner {
			out = append(out, j)
		}
	}
	return out, nil
}

func (m *Memory) Get(_ context.Context, id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, errNotFound
	}
	return j, nil
}

func (m *Memory) Insert(_ context.Context, j Job) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[j.ID] = j
	return j, nil
}

func (m *Memory) Patch(_ context.Context, j Job) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; !ok {
		return Job{}, errNotFound
	}
	m.jobs[j.ID] = j
	return j, nil
}

func (m *Memory) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, id)
	return nil
}

func (m *Memory) Runs(_ context.Context, jobID string) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Run(nil), m.runs[jobID]...), nil
}

func (m *Memory) AppendRun(_ context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[r.JobID] = append(m.runs[r.JobID], r)
	return nil
}

type notFound string

func (n notFound) Error() string { return string(n) }

var errNotFound = notFound("job not found")

func IsNotFound(err error) bool {
	_, ok := err.(notFound)
	return ok
}

type Grant struct {
	Owner     string    `json:"owner"`
	Scope     []string  `json:"scope"`
	CapUsd    float64   `json:"capUsd"`
	Expiry    time.Time `json:"expiry"`
	Revoke    Calldata  `json:"revoke"`
	Verified  bool      `json:"verified"`
}

type Calldata struct {
	To      string `json:"to"`
	Data    string `json:"data"`
	Value   string `json:"value"`
	ChainID int64  `json:"chainId"`
}

type OperatorStatus struct {
	Address     string `json:"address"`
	GasBalance  string `json:"gasBalance"`
	QueueDepth  int    `json:"queueDepth"`
	KillSwitch  bool   `json:"killSwitch"`
	KillReason  string `json:"killReason,omitempty"`
}
