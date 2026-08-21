// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"io"

	"github.com/hanzoai/ai/hedge"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// fan is how one request may be offered to several providers AT ONCE — the
// arrangement fast mode needs, held as one value so a request either carries the
// whole thing or hedges not at all.
//
// It is the CALLER's, because every part of it is: the destination is the body
// this handler is writing, and the writers are its own dialect. The cascade
// hands one translating writer to each provider in turn; a race cannot, because
// the losers' token counts are the thing the ledger needs and a shared writer
// holds exactly one of them.
type fan struct {
	// n is how many providers to ask at once. Below 2 there is no race and the
	// request takes the cascade, so a caller can set this from a number without
	// branching on whether hedging is on.
	n int
	// to is where the winner's bytes go: the response body itself, undecorated.
	// The forks translate; this does not.
	to io.Writer
	// fork makes one attempt's own writer over the raced stream. Fresh buffers
	// per attempt is the whole point — see adopt.
	fork func(out io.Writer) io.Writer
	// adopt takes the winning attempt's writer as the caller's own, so the
	// answer, the tail chunk and the settled token count all come from the
	// provider that actually served.
	adopt func(won io.Writer)
	// bill records one beaten provider and what it spent.
	//
	// IT IS CALLED LATE, from the losing attempt's own goroutine, typically after
	// the winner's answer has already reached the client — and that is not a
	// wrinkle to be tidied away, it is the shape of the thing. A loser stops when
	// it next writes and sees hedge.ErrLost; how long that takes is the loser's
	// business, and holding the response until it finishes would hand the caller
	// exactly the latency this whole path exists to avoid.
	//
	// So it must close over VALUES. By the time it runs the handler has returned
	// and, on fasthttp, the request has been recycled — reading the controller's
	// context or its IP here reads whatever the next request put there.
	bill func(attempt)
}

// said is what an attempt managed to produce. Both dialect writers accumulate
// their own message, and a cancelled attempt's is exactly what we were billed
// for.
type said interface{ MessageString() string }

// shot is one provider's whole outcome, sent by its own goroutine the moment it
// finishes.
type shot struct {
	i   int
	c   candidate
	res *model.ModelResult
	row *object.Provider
	out io.Writer
	err error
}

// race offers the request to the first n candidates at once and keeps the first
// answer, falling back to the cascade when there is nobody to race against.
//
// WHAT THIS COSTS, stated where it is spent: n upstream completions for one
// answer. The losers are cancelled the moment the winner's first byte lands, but
// cancelled is not free and is not even prompt — QueryText takes no context, so
// the only stop signal a provider gets is hedge.ErrLost on its NEXT write. A
// provider that checks its write error stops there; one that does not runs to
// completion and bills us for all of it. Either way the tokens are real, which
// is why every loser that reached a vendor is billed, through fan.bill, whenever
// it eventually finishes.
//
// THE LOSERS ARE NOT IN THE RETURNED ATTEMPTS, deliberately. The caller records
// what it is handed as refusals that cost nothing, which is exactly the wrong
// row for a provider that was cancelled mid-generation — and most of them are
// not knowable yet anyway. One ledger path each: refusals through the caller,
// raced losers through bill.
func (a ask) race() (*model.ModelResult, served, []attempt, error) {
	queue := candidates(a.org, a.route, a.prior)
	if len(queue) > a.fan.n {
		queue = queue[:a.fan.n]
	}
	// One provider is not a race. Take the cascade, which gives this request the
	// retry and the demotion a lone Race would drop on the floor.
	if len(queue) < 2 {
		return a.cascade()
	}
	if err := a.context().Err(); err != nil {
		return nil, served{}, a.prior, err
	}

	done := make(chan shot, len(queue))
	runs := make([]hedge.Attempt, len(queue))
	for i, c := range queue {
		i, c := i, c
		runs[i] = func(_ context.Context, w io.Writer) error {
			// Its OWN translating writer over its OWN share of the stream. The
			// buffers this fills are what the ledger reads if it loses.
			out := a.fan.fork(w)
			res, row, err := callProvider(a.org, a.rowFor(c), c, a.question, out, a.history, a.knowledge, a.lang)
			// Reported before returning, so the winner's outcome is always on the
			// channel by the time Race hands its index back.
			done <- shot{i: i, c: c, res: res, row: row, out: out, err: err}
			return err
		}
	}

	won, err := hedge.Race(a.context(), a.fan.to, runs...)

	// Everything drained on the way to the winner is a loser that had already
	// finished; it is billed with the rest rather than separately.
	var win shot
	var over []shot
	if won >= 0 {
		for {
			s := <-done
			if s.i == won {
				win = s
				break
			}
			over = append(over, s)
		}
	}

	if won < 0 {
		// Nobody produced a byte, and Race only says that once EVERY attempt has
		// returned — so all of them are on the channel and none of this waits.
		// The client is told who was asked, exactly as the cascade tells it.
		raced := a.settle(over, done, len(queue)-len(over))
		if len(raced) == 0 {
			return nil, served{}, a.prior, err
		}
		return nil, served{}, a.prior, exhausted(a.model, raced)
	}

	// The stragglers are still running. Waiting for them is the one thing this
	// path must not do, so a goroutine does it and the answer goes out now.
	go a.settle(over, done, len(queue)-len(over)-1)

	if win.err != nil {
		// The winner claimed the stream and then failed. Its bytes are already on
		// the wire, so this cannot be moved to anybody else — the same commitment
		// the cascade honours once sent() is true.
		return nil, served{name: win.c.provider}, a.prior, win.err
	}

	// The winner's writer becomes the caller's, which is what makes the answer,
	// the tail chunk and the settled token count all come from the provider that
	// actually served.
	a.fan.adopt(win.out)
	log.Info("hedge: model=%s served by %s, %d asked", a.model, win.c.provider, len(queue))
	return win.res, served{win.c.provider, originOf(win.row, nil), win.row}, a.prior, nil
}

// settle bills the beaten providers: the ones already finished, then however many
// are still unwinding. It reports what it billed, which is what names them in the
// error when the race produced no answer at all.
//
// It runs off the request's own goroutine whenever anybody is still running, so
// it touches nothing but its arguments and the values fan.bill closed over.
func (a ask) settle(over []shot, done <-chan shot, rest int) []attempt {
	for i := 0; i < rest; i++ {
		over = append(over, <-done)
	}
	out := make([]attempt, 0, len(over))
	for _, s := range over {
		at := attempt{
			provider: s.c.provider,
			upstream: s.c.upstream,
			origin:   originOf(s.row, nil),
			status:   upstreamHTTPStatus(s.err),
			fault:    faultOf(s.err),
			err:      s.err,
			row:      s.row,
		}
		if at.err == nil {
			// It answered and lost the race. Not a refusal at all — but it is
			// still an attempt this request paid for, and the ledger's question
			// is what was spent rather than who was at fault.
			at.err = hedge.ErrLost
		}
		// Billed only if it reached a vendor. A row that could not be resolved
		// spent no credential and cost nothing, which is the cascade's case and
		// keeps its meaning here.
		if s.row != nil {
			partial := ""
			if t, ok := s.out.(said); ok {
				partial = t.MessageString()
			}
			at.prompt, at.completion = spent(s.res, a.model, a.prompt, partial)
		}
		if d := cooled.rest(a.org, s.c.provider, s.err); d > 0 {
			log.Warn("hedge: demoting provider=%s for %s after status=%d", s.c.provider, d, at.status)
		}
		if a.fan.bill != nil {
			a.fan.bill(at)
		}
		out = append(out, at)
	}
	return out
}
