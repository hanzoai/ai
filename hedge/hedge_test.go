// Copyright © 2026 Hanzo AI. MIT License.

package hedge

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// A writer that records what actually reached the caller.
type sink struct {
	mu  sync.Mutex
	buf strings.Builder
	err error
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	return s.buf.Write(p)
}

func (s *sink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// says returns an attempt that waits, then writes text one chunk at a time.
func says(delay time.Duration, chunks ...string) Attempt {
	return func(ctx context.Context, w io.Writer) error {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		for _, c := range chunks {
			if _, err := w.Write([]byte(c)); err != nil {
				return err
			}
		}
		return nil
	}
}

// THE POINT: the caller gets exactly one answer, from whoever spoke first.
func TestTheFirstToSpeakOwnsTheStream(t *testing.T) {
	var dst sink
	won, err := Race(context.Background(), &dst,
		says(60*time.Millisecond, "slow"),
		says(1*time.Millisecond, "fast", " answer"),
		says(90*time.Millisecond, "slowest"),
	)
	if err != nil {
		t.Fatalf("race: %v", err)
	}
	if won != 1 {
		t.Errorf("winner = %d, want 1", won)
	}
	if got := dst.String(); got != "fast answer" {
		t.Errorf("caller received %q — a loser's bytes reached the stream", got)
	}
}

// A loser must be told, so it stops paying for a response nobody will read.
func TestALoserIsToldItLost(t *testing.T) {
	var dst sink
	// A channel, not a shared variable: Race returns the moment the WINNER is
	// done, with the loser still running. Reading a variable at that point reads
	// it before the loser has written — which is a bug in the test, and one that
	// looks exactly like the feature being broken.
	loserSaw := make(chan error, 1)

	_, err := Race(context.Background(), &dst,
		says(1*time.Millisecond, "winner"),
		func(ctx context.Context, w io.Writer) error {
			time.Sleep(30 * time.Millisecond) // let the winner claim
			_, werr := w.Write([]byte("too late"))
			loserSaw <- werr
			return werr
		},
	)
	if err != nil {
		t.Fatalf("race: %v", err)
	}
	if got := dst.String(); got != "winner" {
		t.Errorf("caller received %q", got)
	}

	select {
	case v := <-loserSaw:
		if !errors.Is(v, ErrLost) {
			t.Errorf("loser's write returned %v, want ErrLost", v)
		}
	case <-time.After(time.Second):
		t.Fatal("the loser never attempted a write")
	}
}

// A loser's context is cancelled the moment it loses — that is what stops the
// upstream call, and the upstream call is what costs money.
func TestALoserIsCancelled(t *testing.T) {
	var dst sink
	cancelled := make(chan struct{})

	_, err := Race(context.Background(), &dst,
		says(1*time.Millisecond, "winner"),
		func(ctx context.Context, w io.Writer) error {
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		},
	)
	if err != nil {
		t.Fatalf("race: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("the losing attempt was never cancelled — its upstream keeps running, and billing")
	}
}

// AN EMPTY WRITE MUST NOT CLAIM. A provider that flushes a zero-length chunk
// while still waiting on its upstream would otherwise win having said nothing,
// and the caller would then wait on the SLOWEST attempt with the others already
// cancelled — the exact opposite of hedging.
func TestAnEmptyWriteDoesNotClaimTheStream(t *testing.T) {
	var dst sink
	won, err := Race(context.Background(), &dst,
		func(ctx context.Context, w io.Writer) error {
			_, _ = w.Write(nil)         // a flush with nothing in it
			_, _ = w.Write([]byte(""))  // and again
			select {
			case <-time.After(80 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := w.Write([]byte("late"))
			return err
		},
		says(2*time.Millisecond, "real answer"),
	)
	if err != nil {
		t.Fatalf("race: %v", err)
	}
	if won != 1 {
		t.Errorf("winner = %d, want 1 — an empty write claimed the stream", won)
	}
	if got := dst.String(); got != "real answer" {
		t.Errorf("caller received %q", got)
	}
}

// Race returns when the WINNER finishes, not when the losers do. Waiting on an
// abandoned provider hands it exactly the latency this exists to avoid.
func TestItDoesNotWaitForTheLosers(t *testing.T) {
	var dst sink
	start := time.Now()
	_, err := Race(context.Background(), &dst,
		says(2*time.Millisecond, "quick"),
		func(ctx context.Context, w io.Writer) error {
			// Ignores cancellation entirely, the way a stuck upstream would.
			time.Sleep(2 * time.Second)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("race: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v — it waited for an attempt it had already abandoned", elapsed)
	}
}

// An attempt that fails FAST is not the winner. Its bytes never reached the
// caller, so the caller is still waiting on whoever does answer.
func TestAFastFailureIsNotAWin(t *testing.T) {
	var dst sink
	boom := errors.New("upstream 503")
	won, err := Race(context.Background(), &dst,
		func(ctx context.Context, w io.Writer) error { return boom },
		says(20*time.Millisecond, "the real answer"),
	)
	if err != nil {
		t.Fatalf("race returned %v, want the winner's nil", err)
	}
	if won != 1 {
		t.Errorf("winner = %d, want 1", won)
	}
	if got := dst.String(); got != "the real answer" {
		t.Errorf("caller received %q", got)
	}
}

// Every attempt failing is a real failure, reported with a reason rather than a
// nil error over an empty response.
func TestAllFailingReportsWhy(t *testing.T) {
	var dst sink
	boom := errors.New("upstream 503")
	won, err := Race(context.Background(), &dst,
		func(ctx context.Context, w io.Writer) error { return boom },
		func(ctx context.Context, w io.Writer) error { return boom },
	)
	if won != -1 {
		t.Errorf("winner = %d, want -1", won)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the upstream failure", err)
	}
	if dst.String() != "" {
		t.Errorf("caller received %q from attempts that all failed", dst.String())
	}
}

// One attempt is a plain call. A caller can hedge conditionally without branching
// on whether hedging is on.
func TestOneAttemptIsJustACall(t *testing.T) {
	var dst sink
	won, err := Race(context.Background(), &dst, says(0, "only"))
	if err != nil || won != 0 || dst.String() != "only" {
		t.Fatalf("won=%d err=%v out=%q", won, err, dst.String())
	}
	if _, err := Race(context.Background(), &dst); !errors.Is(err, ErrNoAttempts) {
		t.Errorf("empty race err = %v, want ErrNoAttempts", err)
	}
}

// Chunks from the winner must arrive whole and in order. Two attempts writing
// concurrently must never interleave into the caller's stream.
func TestTheWinnersChunksArriveIntact(t *testing.T) {
	var dst sink
	_, err := Race(context.Background(), &dst,
		says(1*time.Millisecond, "a", "b", "c", "d", "e"),
		says(0, ""), // claims nothing, then returns
	)
	if err != nil {
		t.Fatalf("race: %v", err)
	}
	if got := dst.String(); got != "abcde" {
		t.Errorf("caller received %q, want abcde", got)
	}
}
