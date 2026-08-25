package signer

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"sync"
)

var (
	ErrNoKey        = errors.New("signer: no operator key loaded")
	ErrNotPoliced   = errors.New("signer: transaction was not approved by policy")
	ErrKillSwitch   = errors.New("signer: kill switch tripped")
)

// Address is a 20-byte hex address (0x-prefixed).
type Address string

// Tx is the unsigned payload the operator would sign. Kept free of
// go-ethereum types so this package has no HTTP or business-logic imports.
type Tx struct {
	ChainID   *big.Int
	Nonce     uint64
	To        Address
	Value     *big.Int
	Data      []byte
	Gas       uint64
	GasFeeCap *big.Int
	GasTipCap *big.Int
	// Policed must be set by internal/policy before SignTx will accept it.
	Policed bool
	JobID   string
}

type Signer interface {
	Address(ctx context.Context) (Address, error)
	SignTx(ctx context.Context, tx *Tx) (signed []byte, err error)
}

// Disabled is the phase-1 signer: no key in process, every SignTx fails.
type Disabled struct{}

func (Disabled) Address(context.Context) (Address, error) { return "", ErrNoKey }
func (Disabled) SignTx(context.Context, *Tx) ([]byte, error) {
	return nil, ErrNoKey
}

// Keystore is a local encrypted-keystore implementation for development.
// Production should use KMS so the private key never enters process memory.
type Keystore struct {
	mu   sync.Mutex
	addr Address
	key  *ecdsa.PrivateKey // nil until unlocked; never log or serialize
}

func NewKeystore(addr Address) *Keystore {
	return &Keystore{addr: addr}
}

func (k *Keystore) Address(context.Context) (Address, error) {
	if k.addr == "" {
		return "", ErrNoKey
	}
	return k.addr, nil
}

func (k *Keystore) SignTx(_ context.Context, tx *Tx) ([]byte, error) {
	if tx == nil || !tx.Policed {
		return nil, ErrNotPoliced
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key == nil {
		return nil, ErrNoKey
	}
	return nil, ErrNoKey // phase 1: key never loaded
}

// KMS is a remote-signing stub (cloud KMS / HSM). The private key never
// enters process memory; SignTx would call the KMS API.
type KMS struct {
	Addr   Address
	KeyURI string
}

func (k KMS) Address(context.Context) (Address, error) {
	if k.Addr == "" {
		return "", ErrNoKey
	}
	return k.Addr, nil
}

func (k KMS) SignTx(_ context.Context, tx *Tx) ([]byte, error) {
	if tx == nil || !tx.Policed {
		return nil, ErrNotPoliced
	}
	return nil, ErrNoKey
}
