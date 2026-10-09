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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// upstreamTimeout bounds an upstream exchange, body included, when the plugin
// states none: a full-context prompt is large by design and a long answer takes
// its time.
const upstreamTimeout = 10 * time.Minute

// upstream is one request the host makes for a plugin: its head once it
// arrives, and the body's bytes the plugin has not yet read.
type upstream struct {
	h      *Host
	cancel context.CancelFunc

	mu     sync.Mutex
	room   *sync.Cond
	head   []byte // the Head as JSON, once it arrived
	buf    []byte
	eof    bool
	cut    bool
	closed bool
}

func (u *upstream) close() {
	u.cancel()
	u.mu.Lock()
	u.closed = true
	u.room.Broadcast()
	u.mu.Unlock()
}

// open starts the request a plugin asked for and returns its handle at once; the
// plugin reads its head and body as they arrive.
func (h *Host) open(mem []byte, id, mp, mn, bp, bn int32) int32 {
	c := h.calls[id]
	if c == nil {
		return Gone
	}
	raw, body := bytesAt(mem, mp, mn), bytesAt(mem, bp, bn)
	var o Open
	if raw == nil || body == nil || json.Unmarshal(raw, &o) != nil {
		return Invalid
	}
	target, err := url.Parse(o.URL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return Invalid
	}
	method := strings.ToUpper(strings.TrimSpace(o.Method))
	if method == "" {
		method = http.MethodGet
	}
	key := ""
	if o.Key != "" {
		if h.p.Keys != nil {
			key = h.p.Keys(o.Key)
		}
		if key == "" {
			return Denied
		}
	}
	timeout := upstreamTimeout
	if o.Timeout > 0 {
		timeout = time.Duration(o.Timeout) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(bytes.Clone(body)))
	if err != nil {
		cancel()
		return Invalid
	}
	for _, kv := range o.Headers {
		req.Header.Add(kv[0], kv[1])
	}
	if key != "" {
		switch auth := strings.TrimSpace(o.Auth); {
		case auth == "" || strings.EqualFold(auth, "bearer"):
			req.Header.Set("Authorization", "Bearer "+key)
		case strings.EqualFold(auth, "x-api-key"):
			req.Header.Set("X-Api-Key", key)
		case strings.HasPrefix(strings.ToLower(auth), "header:") && len(auth) > len("header:"):
			req.Header.Set(auth[len("header:"):], key)
		default:
			cancel()
			return Invalid
		}
	}
	u := &upstream{h: h, cancel: cancel}
	u.room = sync.NewCond(&u.mu)
	h.next++
	handle := h.next
	h.ups[handle] = u
	c.ups = append(c.ups, handle)
	var head *time.Timer
	if o.Head > 0 {
		head = time.AfterFunc(time.Duration(o.Head)*time.Millisecond, cancel)
	}
	go u.fetch(req, head)
	return handle
}

// fetch makes the request and holds its body for the plugin, a window at a time:
// an upstream the plugin is not reading is not read either.
func (u *upstream) fetch(req *http.Request, head *time.Timer) {
	defer u.cancel()
	resp, err := u.h.p.Client.Do(req)
	if head != nil {
		head.Stop()
	}
	if err != nil {
		u.arrive(Head{Error: err.Error()})
		return
	}
	defer resp.Body.Close()
	hd := Head{Status: resp.StatusCode}
	for k, vs := range resp.Header {
		for _, v := range vs {
			hd.Headers = append(hd.Headers, [2]string{k, v})
		}
	}
	u.arrive(hd)
	chunk := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			u.mu.Lock()
			for len(u.buf) >= window && !u.closed {
				u.room.Wait()
			}
			if u.closed {
				u.mu.Unlock()
				return
			}
			u.buf = append(u.buf, chunk[:n]...)
			u.mu.Unlock()
			u.h.wake()
		}
		if err != nil {
			u.mu.Lock()
			if errors.Is(err, io.EOF) {
				u.eof = true
			} else {
				u.cut = true
			}
			u.mu.Unlock()
			u.h.wake()
			return
		}
	}
}

func (u *upstream) arrive(hd Head) {
	b, _ := json.Marshal(hd)
	u.mu.Lock()
	u.head = b
	if hd.Status == 0 {
		u.cut = true
	}
	u.mu.Unlock()
	u.h.wake()
}

func (h *Host) head(mem []byte, handle, p, n int32) int32 {
	u := h.ups[handle]
	if u == nil {
		return Invalid
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.head == nil {
		return Pending
	}
	if int32(len(u.head)) > n {
		return int32(len(u.head))
	}
	out := bytesAt(mem, p, int32(len(u.head)))
	if out == nil {
		return Invalid
	}
	copy(out, u.head)
	return int32(len(u.head))
}

func (h *Host) read(mem []byte, handle, p, n int32) int32 {
	u := h.ups[handle]
	if u == nil {
		return Invalid
	}
	out := bytesAt(mem, p, n)
	if out == nil {
		return Invalid
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.buf) > 0 {
		took := copy(out, u.buf)
		u.buf = u.buf[:copy(u.buf, u.buf[took:])]
		u.room.Broadcast()
		return int32(took)
	}
	switch {
	case u.cut:
		return Cut
	case u.eof:
		return 0
	}
	return Pending
}

func (h *Host) close(handle int32) {
	if u := h.ups[handle]; u != nil {
		u.close()
		delete(h.ups, handle)
	}
}
