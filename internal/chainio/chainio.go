package chainio

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"time"

	"amazing/internal/signer"
)

var (
	ErrGasCeiling   = errors.New("chainio: gas above ceiling")
	ErrSimulateFail = errors.New("chainio: simulation reverted")
	ErrNotLeader    = errors.New("chainio: not the executing instance")
)

// Lifecycle is the persisted state machine per broadcast attempt.
type Lifecycle string

const (
	Queued     Lifecycle = "queued"
	Simulated  Lifecycle = "simulated"
	Signed     Lifecycle = "signed"
	Broadcast  Lifecycle = "broadcast"
	Mined      Lifecycle = "mined"
	Confirmed  Lifecycle = "confirmed"
	Finalised  Lifecycle = "finalised"
	Failed     Lifecycle = "failed"
	Reverted   Lifecycle = "reverted"
	Replaced   Lifecycle = "replaced"
)

type Attempt struct {
	ID        string
	JobID     string
	Epoch     int64
	State     Lifecycle
	TxHash    string
	Nonce     uint64
	UpdatedAt time.Time
}

type NonceManager interface {
	Next(ctx context.Context, addr signer.Address) (uint64, error)
	Ack(ctx context.Context, addr signer.Address, nonce uint64, hash string) error
}

type MemoryNonce struct {
	mu   sync.Mutex
	next map[signer.Address]uint64
}

func NewMemoryNonce() *MemoryNonce {
	return &MemoryNonce{next: map[signer.Address]uint64{}}
}

func (m *MemoryNonce) Next(_ context.Context, addr signer.Address) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.next[addr]
	m.next[addr] = n + 1
	return n, nil
}

func (m *MemoryNonce) Ack(context.Context, signer.Address, uint64, string) error { return nil }

type GasPolicy struct {
	MaxFeeCapWei *big.Int
	TipCapWei    *big.Int
}

func (g GasPolicy) Check(feeCap *big.Int) error {
	if g.MaxFeeCapWei != nil && feeCap != nil && feeCap.Cmp(g.MaxFeeCapWei) > 0 {
		return ErrGasCeiling
	}
	return nil
}

// Broadcaster is a no-op in phase 1 (dry-run stops before broadcast).
type Broadcaster interface {
	Simulate(ctx context.Context, tx *signer.Tx) error
	Send(ctx context.Context, signed []byte) (hash string, err error)
}

type DryRun struct{}

func (DryRun) Simulate(context.Context, *signer.Tx) error { return nil }
func (DryRun) Send(context.Context, []byte) (string, error) {
	return "", errors.New("chainio: dry-run — not broadcasting")
}

type Receipt struct {
	Hash          string
	Block         uint64
	Confirmations int
	Reorged       bool
	Status        uint64
}

type Watcher interface {
	Wait(ctx context.Context, hash string, confirmations int) (Receipt, error)
}
