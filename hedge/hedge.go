// Copyright © 2026 Hanzo AI. MIT License.

// Hedging: ask several providers at once and keep the first answer.
//
// Tail latency is not the average. A provider that usually answers in 400ms
// occasionally takes eight seconds, and a single request has no way to know which
// kind it drew until it has already waited. Asking three and keeping the first is
// how that tail gets cut — the slow draw stops mattering because it is racing two
// others.
//
// It costs what it sounds like: N upstream calls for one answer. That is the
// whole trade, it is the customer's to make per request, and it meters itself —
// every upstream call writes its own usage row, so a three-way hedge draws down
// three units of allowance with no change to the billing path.
//
// FIRST BYTE WINS, and that is the only definition of "fastest" a stream can act
// on. A provider that will finish sooner but has not started is not yet the
// winner, because the caller is waiting on bytes rather than on completion — and
// by the time completion is knowable the whole response has arrived and there was
// nothing to race.
//
// NOTHING HERE TOUCHES A PROVIDER. Every provider already writes into an
// io.Writer; this hands each attempt a different one and only lets the winner's
// reach the caller. The seam was already the right shape.
package hedge

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ErrLost is what a losing attempt's writer returns once another attempt has
// claimed the stream. A provider that checks its write error stops early instead
// of finishing a response nobody will read.
var ErrLost = errors.New("hedge: another attempt claimed the stream")

// arena is the shared decision: who owns the caller's writer.
//
// A mutex rather than an atomic, because claiming is not the whole operation —
// the winner's first bytes must reach dst BEFORE any later write from the same
// attempt, and two attempts writing concurrently must not interleave into the
// caller's stream. The lock is held across the write for exactly that reason.
type arena struct {
	mu     sync.Mutex
	dst    io.Writer
	winner int  // 0 = unclaimed; otherwise the winning attempt's id
	failed bool // dst returned an error; every later write is refused
}

// writer is one attempt's view of the caller's stream.
type writer struct {
	a  *arena
	id int // 1-based, so the zero value cannot accidentally hold the claim
}

// Write claims the stream on first use and then passes through. A losing attempt
// gets ErrLost and no bytes reach the caller.
//
// An EMPTY write does not claim. A provider that writes a zero-length chunk while
// waiting on its upstream would otherwise win the race having produced nothing,
// and the caller would wait on the slowest attempt with the others already
// cancelled — the opposite of the point.
func (w *writer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.a.mu.Lock()
	defer w.a.mu.Unlock()

	if w.a.winner == 0 {
		w.a.winner = w.id
	}
	if w.a.winner != w.id {
		// Report the bytes as written. A losing provider is being cancelled and
		// does not need a short-write error on the way out; ErrLost already says
		// what happened, and a short write would send some clients into a retry
		// loop against an upstream we are abandoning.
		return len(p), ErrLost
	}
	if w.a.failed {
		return len(p), ErrLost
	}
	n, err := w.a.dst.Write(p)
	if err != nil {
		// The CALLER went away. Every attempt should stop, so the arena records
		// it once rather than each attempt discovering it separately.
		w.a.failed = true
	}
	return n, err
}

// Attempt is one provider's shot at the request. It writes its answer to w and
// returns when done; ctx is cancelled the moment another attempt wins.
type Attempt func(ctx context.Context, w io.Writer) error

// Race runs every attempt against dst and returns the index of the one whose
// bytes reached the caller, with that attempt's own error.
//
// It returns as soon as the WINNER finishes, not when the losers do. A loser is
// cancelled and left to unwind on its own: waiting for a provider we are
// abandoning would hand it exactly the latency this exists to avoid.
//
// With no attempts it returns (-1, ErrNoAttempts). With one it is a plain call
// with one allocation of overhead — so a caller can hedge conditionally without
// branching on whether hedging is on.
func Race(ctx context.Context, dst io.Writer, attempts ...Attempt) (int, error) {
	switch len(attempts) {
	case 0:
		return -1, ErrNoAttempts
	case 1:
		return 0, attempts[0](ctx, dst)
	}

	a := &arena{dst: dst}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		id  int
		err error
	}
	done := make(chan outcome, len(attempts))

	for i, fn := range attempts {
		go func(id int, fn Attempt) {
			err := fn(ctx, &writer{a: a, id: id + 1})
			done <- outcome{id: id, err: err}
		}(i, fn)
	}

	// Wait for the attempt that CLAIMED the stream. Another may finish first —
	// by failing fast, typically — and that is not the answer: its bytes never
	// reached the caller, so the caller is still waiting on whoever won.
	var firstErr error
	for range attempts {
		o := <-done
		a.mu.Lock()
		winner := a.winner
		a.mu.Unlock()

		if winner == o.id+1 {
			return o.id, o.err
		}
		if firstErr == nil && o.err != nil {
			firstErr = o.err
		}
		if winner != 0 {
			// Someone else owns the stream; this attempt is a loser that has
			// already unwound. Keep waiting for the winner.
			continue
		}
	}

	// Every attempt returned and none produced a byte. The caller got nothing, so
	// the honest answer is the first real failure rather than a nil error over an
	// empty response.
	if firstErr == nil {
		firstErr = ErrNoAnswer
	}
	return -1, firstErr
}

var (
	// ErrNoAttempts — Race was called with nothing to run.
	ErrNoAttempts = errors.New("hedge: no attempts")
	// ErrNoAnswer — every attempt returned without writing a byte, and none of
	// them reported why.
	ErrNoAnswer = errors.New("hedge: no attempt produced an answer")
)
