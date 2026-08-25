package market

import (
	"context"
	"errors"
	"testing"
)

func TestChainNotImplemented(t *testing.T) {
	c := NewChain("", "")
	if _, err := c.Pools(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Fatal(err)
	}
	if _, err := c.Perps(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Fatal(err)
	}
	if _, err := c.Subscribe(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Fatal(err)
	}
}
