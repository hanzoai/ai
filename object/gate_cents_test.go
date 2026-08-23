// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"context"
	"testing"
)

// The gate asks whether a wallet covers a call. A call that costs anything has to
// need at least a cent, or the cheapest ones cost nothing to make and an empty
// wallet makes them all day.
func TestAnEmptyWalletCannotAffordAnything(t *testing.T) {
	saved := balanceReader
	t.Cleanup(func() { balanceReader = saved })
	balanceReader = func(_ context.Context, _, _, _ string) (int64, error) { return 0, nil }

	for _, price := range []float64{0.0001, 0.001, 0.004, 0.0049, 0.005, 0.01, 1} {
		err := ValidateTransactionForMessage(&Message{
			Owner: "acme", User: "alice", ModelProvider: "p", Price: price, Currency: "USD",
		})
		if err == nil {
			t.Errorf("a wallet holding nothing was allowed a call priced at $%v", price)
		}
	}

	// A call that costs nothing still needs no balance.
	if err := ValidateTransactionForMessage(&Message{
		Owner: "acme", User: "alice", ModelProvider: "p", Price: 0, Currency: "USD",
	}); err != nil {
		t.Errorf("a free call was refused: %v", err)
	}

	// And a wallet that covers the call allows it.
	balanceReader = func(_ context.Context, _, _, _ string) (int64, error) { return 100, nil }
	if err := ValidateTransactionForMessage(&Message{
		Owner: "acme", User: "alice", ModelProvider: "p", Price: 0.50, Currency: "USD",
	}); err != nil {
		t.Errorf("a covered call was refused: %v", err)
	}
}
