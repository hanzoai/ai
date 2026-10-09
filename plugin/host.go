// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Plugin is one model family run in this process.
type Plugin struct {
	// Name is how an address reaches it: plugin://<name>.
	Name string
	// New builds a fresh instance over the host's imports:
	//
	//	New: func(h *plugin.Host) plugin.Guest {
	//		return zen.New(plugin.Imports[*zen.Module]{H: h})
	//	}
	New func(*Host) Guest
	// Config is hz_init's argument: the plugin's own configuration (a catalog,
	// a family), never a key.
	Config []byte
	// Keys resolves a key the plugin names to its value; "" is a key it may not
	// use or one that holds nothing. The host writes the value into the upstream
	// request; the plugin never sees it.
	Keys func(name string) string
	// Reply bounds the wait for a call's head; zero waits as long as the
	// caller's context does.
	Reply time.Duration
	// Client makes the plugin's upstream requests; nil is a default.
	Client *http.Client
	// Now is the clock hz_now and hz_poll read; nil is the wall clock.
	Now func() time.Time
}

// window is how many bytes of answer the host holds for a caller who has not
// read them, and how many of an upstream's body for a plugin that has not. Past
// it the writer waits: hz_write answers Pending, an upstream's reader stops.
const window = 64 << 10

// Host runs one plugin: one instance, stepped by one goroutine. Requests reach
// it with Do; the plugin's upstream requests and answers reach the host through
// Imports.
type Host struct {
	p     Plugin
	start sync.Once
	work  chan func()
	kick  chan struct{}

	// Owned by the stepping goroutine.
	guest Guest
	calls map[int32]*call
	ups   map[int32]*upstream
	next  int32
}

// newHost makes the host of p. It starts on its first call.
func newHost(p Plugin) *Host {
	if p.Client == nil {
		p.Client = &http.Client{}
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	return &Host{p: p, work: make(chan func()), kick: make(chan struct{}, 1), calls: map[int32]*call{}, ups: map[int32]*upstream{}}
}

// wake asks the stepping goroutine to poll the plugin.
func (h *Host) wake() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// run steps the plugin: a request to start or stop, something it waits on that
// moved, or the time it asked to be woken at, then hz_poll.
func (h *Host) run() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case f := <-h.work:
			f()
		case <-h.kick:
		case <-timer.C:
		}
		for more := true; more; {
			select {
			case f := <-h.work:
				f()
			default:
				more = false
			}
		}
		at := h.poll()
		timer.Stop()
		if at >= 0 {
			timer.Reset(max(time.Until(time.UnixMilli(at)), 0))
		}
	}
}

// poll runs hz_poll and reports when the plugin asked to be woken, -1 for never.
func (h *Host) poll() int64 {
	if h.guest == nil {
		return -1
	}
	at := int64(-1)
	h.safe(func() { at = h.guest.HzPoll(h.p.Now().UnixMilli()) })
	return at
}

// safe runs f against the plugin. A trap ends every call in flight with it and
// leaves the host to build a fresh instance for the next request: a plugin that
// trapped has no state worth trusting.
func (h *Host) safe(f func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			h.trap(fmt.Errorf("plugin %s: %v", h.p.Name, r))
			ok = false
		}
	}()
	f()
	return true
}

func (h *Host) trap(err error) {
	for id, c := range h.calls {
		c.fail(err)
		delete(h.calls, id)
	}
	for id, u := range h.ups {
		u.close()
		delete(h.ups, id)
	}
	h.guest = nil
}

// ready builds and initializes the instance if there is none.
func (h *Host) ready() error {
	if h.guest != nil {
		return nil
	}
	g := h.p.New(h)
	var code int32
	ok := h.safe(func() {
		p, n := put(g, h.p.Config)
		code = g.HzInit(p, n)
		g.HzFree(p, n)
	})
	if !ok {
		return fmt.Errorf("plugin %s: init trapped", h.p.Name)
	}
	if code != 0 {
		return fmt.Errorf("plugin %s: init refused (%d)", h.p.Name, code)
	}
	h.guest = g
	return nil
}

// put copies b into memory the plugin allocated.
func put(g Guest, b []byte) (int32, int32) {
	n := int32(len(b))
	p := g.HzAlloc(n)
	copy(g.Memory()[p:p+n], b)
	return p, n
}

// call is one request in flight: what the plugin has answered, and what the
// caller has not yet read of it.
type call struct {
	h      *Host
	id     int32
	headed chan struct{}

	mu    sync.Mutex
	more  *sync.Cond
	reply Reply
	buf   []byte
	ended bool
	end   End
	err   error
	gone  bool
	ups   []int32 // reached only by the stepping goroutine
}

func (c *call) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil && !c.ended {
		c.err = err
	}
	c.closeHead()
	c.more.Broadcast()
}

// closeHead closes headed once, under mu.
func (c *call) closeHead() {
	select {
	case <-c.headed:
	default:
		close(c.headed)
	}
}

// Answer is a plugin's answer to one request: its head, its body as the plugin
// writes it, and what it states at the end.
type Answer struct {
	Reply Reply
	Body  io.ReadCloser
	// End is what the plugin stated after the body, valid once Body has
	// returned io.EOF.
	End func() End
}

// Do asks the plugin to answer ask, and returns once it has replied, failed, or
// the caller's context or the Reply bound ended the wait. Body reads the answer
// at the caller's pace: a caller that stops reading stops the plugin writing.
func (h *Host) Do(ctx context.Context, ask Ask, body []byte) (*Answer, error) {
	h.start.Do(func() { go h.run() })
	meta, err := json.Marshal(ask)
	if err != nil {
		return nil, err
	}
	type begun struct {
		c   *call
		err error
	}
	res := make(chan begun, 1)
	h.work <- func() {
		c, err := h.begin(meta, body)
		res <- begun{c, err}
	}
	b := <-res
	if b.err != nil {
		return nil, b.err
	}
	c := b.c
	wait := ctx.Done()
	var timeout <-chan time.Time
	if h.p.Reply > 0 {
		t := time.NewTimer(h.p.Reply)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-c.headed:
	case <-wait:
		h.cancel(c)
		return nil, context.Cause(ctx)
	case <-timeout:
		h.cancel(c)
		return nil, fmt.Errorf("plugin %s: no reply in %s", h.p.Name, h.p.Reply)
	}
	c.mu.Lock()
	reply, err := c.reply, c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	r := &reader{c: c}
	stop := context.AfterFunc(ctx, func() { _ = r.Close() })
	r.stop = stop
	return &Answer{Reply: reply, Body: r, End: func() End {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.end
	}}, nil
}

// begin starts a call on the stepping goroutine.
func (h *Host) begin(meta, body []byte) (*call, error) {
	if err := h.ready(); err != nil {
		return nil, err
	}
	var id int32
	ok := h.safe(func() {
		ap, an := put(h.guest, meta)
		bp, bn := put(h.guest, body)
		id = h.guest.HzBegin(ap, an, bp, bn)
		if h.guest != nil {
			h.guest.HzFree(bp, bn)
			h.guest.HzFree(ap, an)
		}
	})
	if !ok {
		return nil, fmt.Errorf("plugin %s: begin trapped", h.p.Name)
	}
	if id <= 0 {
		return nil, fmt.Errorf("plugin %s: refused the request (%d)", h.p.Name, id)
	}
	c := &call{h: h, id: id, headed: make(chan struct{})}
	c.more = sync.NewCond(&c.mu)
	h.calls[id] = c
	return c, nil
}

// cancel ends c for a caller who left: the plugin is told, and every upstream it
// opened for c is hung up.
func (h *Host) cancel(c *call) {
	c.mu.Lock()
	done := c.ended || c.gone
	c.gone = true
	c.more.Broadcast()
	c.mu.Unlock()
	if done {
		return
	}
	go func() {
		h.work <- func() {
			if h.calls[c.id] != c {
				return
			}
			delete(h.calls, c.id)
			h.drop(c)
			if h.guest != nil {
				h.safe(func() { h.guest.HzCancel(c.id) })
			}
		}
	}()
}

// drop hangs up every upstream c opened.
func (h *Host) drop(c *call) {
	for _, u := range c.ups {
		if up := h.ups[u]; up != nil {
			up.close()
			delete(h.ups, u)
		}
	}
	c.ups = nil
}

// reader is an answer's body: the bytes the plugin wrote, read at the caller's
// pace.
type reader struct {
	c    *call
	stop func() bool
	once sync.Once
}

func (r *reader) Read(p []byte) (int, error) {
	c := r.c
	c.mu.Lock()
	for len(c.buf) == 0 && !c.ended && c.err == nil && !c.gone {
		c.more.Wait()
	}
	if len(c.buf) > 0 {
		full := len(c.buf) >= window
		n := copy(p, c.buf)
		c.buf = c.buf[:copy(c.buf, c.buf[n:])]
		c.mu.Unlock()
		if full {
			c.h.wake()
		}
		return n, nil
	}
	defer c.mu.Unlock()
	switch {
	case c.err != nil:
		return 0, c.err
	case c.ended:
		return 0, io.EOF
	}
	return 0, errors.New("plugin: answer closed")
}

// Close stops the answer. A caller who closes before its end has left: the
// plugin is told, and stops.
func (r *reader) Close() error {
	r.once.Do(func() {
		if r.stop != nil {
			r.stop()
		}
		r.c.h.cancel(r.c)
	})
	return nil
}

// bytesAt is guest memory [p, p+n), or nil when the range is not in it.
func bytesAt(mem []byte, p, n int32) []byte {
	if p < 0 || n < 0 || int64(p)+int64(n) > int64(len(mem)) {
		return nil
	}
	return mem[p : p+n]
}

// Imports are the host's functions as a wasm2go'd plugin of type M calls them.
// One generic value serves every plugin, because every plugin imports the same
// hz_ functions:
//
//	zen.New(plugin.Imports[*zen.Module]{H: h})
type Imports[M Guest] struct{ H *Host }

func (i Imports[M]) Hz_open(m M, call, meta, metaLen, body, bodyLen int32) int32 {
	return i.H.open(m.Memory(), call, meta, metaLen, body, bodyLen)
}

func (i Imports[M]) Hz_head(m M, handle, buf, capacity int32) int32 {
	return i.H.head(m.Memory(), handle, buf, capacity)
}

func (i Imports[M]) Hz_read(m M, handle, buf, capacity int32) int32 {
	return i.H.read(m.Memory(), handle, buf, capacity)
}

func (i Imports[M]) Hz_close(_ M, handle int32) { i.H.close(handle) }

func (i Imports[M]) Hz_reply(m M, call, meta, n int32) int32 {
	return i.H.reply(m.Memory(), call, meta, n)
}

func (i Imports[M]) Hz_write(m M, call, buf, n int32) int32 {
	return i.H.write(m.Memory(), call, buf, n)
}

func (i Imports[M]) Hz_end(m M, call, meta, n int32) int32 {
	return i.H.finish(m.Memory(), call, meta, n)
}

func (i Imports[M]) Hz_now(_ M) int64 { return i.H.p.Now().UnixMilli() }

func (h *Host) reply(mem []byte, id, p, n int32) int32 {
	c := h.calls[id]
	if c == nil {
		return Gone
	}
	raw := bytesAt(mem, p, n)
	var r Reply
	if raw == nil || json.Unmarshal(raw, &r) != nil || r.Status < 100 || r.Status > 999 {
		return Invalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return Gone
	}
	c.reply = r
	c.closeHead()
	return 0
}

func (h *Host) write(mem []byte, id, p, n int32) int32 {
	c := h.calls[id]
	if c == nil {
		return Gone
	}
	raw := bytesAt(mem, p, n)
	if raw == nil {
		return Invalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return Gone
	}
	room := window - len(c.buf)
	if room <= 0 {
		return Pending
	}
	took := min(room, len(raw))
	c.buf = append(c.buf, raw[:took]...)
	c.more.Broadcast()
	return int32(took)
}

func (h *Host) finish(mem []byte, id, p, n int32) int32 {
	c := h.calls[id]
	if c == nil {
		return Gone
	}
	raw := bytesAt(mem, p, n)
	var e End
	if raw == nil || json.Unmarshal(raw, &e) != nil {
		return Invalid
	}
	delete(h.calls, id)
	h.drop(c)
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.headed:
	default:
		// An answer that ends before it began is a plugin's failure, not an
		// answer.
		c.err = fmt.Errorf("plugin %s: ended without a reply", h.p.Name)
		c.closeHead()
		c.more.Broadcast()
		return 0
	}
	c.ended, c.end = true, e
	c.more.Broadcast()
	if c.gone {
		return Gone
	}
	return 0
}
