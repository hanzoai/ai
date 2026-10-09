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

// Package plugin answers a family's address in this process.
//
// A family service may run in the process that relays to it: its engine linked
// as a Go package (the zen and enso engines are their Rust core, translated by
// wasm2go, behind an http.Handler that runs the module and makes its upstream
// calls) and registered here under a name. The address plugin://<name> is then
// that handler, called directly: no socket, no second process. Every other
// address is the network's. The address is the only thing that differs, so the
// relay, the tells, billing, failover rows and attribution are the HTTP path's.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Scheme is the address scheme a plugin answers at: plugin://zen is the handler
// registered as "zen".
const Scheme = "plugin"

var handlers sync.Map // name -> http.Handler

// Register makes h answer plugin://<name>, in place of any it replaces. A family
// is registered once per process: one instance holds its key pool, lane health
// and sessions.
func Register(name string, h http.Handler) {
	handlers.Store(strings.ToLower(strings.TrimSpace(name)), h)
}

// Lookup is the handler registered as name, or nil.
func Lookup(name string) http.Handler {
	if h, ok := handlers.Load(strings.ToLower(strings.TrimSpace(name))); ok {
		return h.(http.Handler)
	}
	return nil
}

// Transport answers plugin:// requests in process, as an http.RoundTripper:
// the request is served by the registered handler, and its answer read back as
// the HTTP answer it would have been, its body as it is written and its trailers
// once the body ends. Register it on an http.Transport:
//
//	t.RegisterProtocol(plugin.Scheme, plugin.Transport{Head: t.ResponseHeaderTimeout})
type Transport struct {
	// Head bounds how long the handler may take to begin its answer; zero is no
	// bound but the request's own context.
	Head time.Duration
}

// errHead is a handler that did not begin its answer within Transport.Head.
var errHead = errors.New("plugin: timeout awaiting response headers")

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	h := Lookup(r.URL.Host)
	if h == nil {
		return nil, fmt.Errorf("plugin %q is not registered", r.URL.Host)
	}
	// The handler's request is the caller's, under a context that also ends when
	// the caller closes the body early: a caller who leaves ends the call, and the
	// handler hangs up on its upstream.
	ctx, cancel := context.WithCancel(r.Context())
	in := r.Clone(ctx)
	in.RequestURI = r.URL.RequestURI()
	if in.Body == nil {
		in.Body = http.NoBody
	}
	pr, pw := io.Pipe()
	w := &writer{header: http.Header{}, pipe: pw, head: make(chan struct{})}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				w.fail(fmt.Errorf("plugin %s: %v", r.URL.Host, p))
				return
			}
			w.finish()
		}()
		h.ServeHTTP(w, in)
	}()

	var bound <-chan time.Time
	if t.Head > 0 {
		timer := time.NewTimer(t.Head)
		defer timer.Stop()
		bound = timer.C
	}
	select {
	case <-w.head:
	case <-ctx.Done():
		cancel()
		_ = pr.CloseWithError(ctx.Err())
		return nil, ctx.Err()
	case <-bound:
		cancel()
		_ = pr.CloseWithError(errHead)
		return nil, errHead
	}
	if w.err != nil && w.status == 0 {
		cancel()
		_ = pr.Close()
		return nil, w.err
	}
	resp := &http.Response{
		Status:        fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		StatusCode:    w.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        w.sent,
		ContentLength: -1,
		Request:       r,
	}
	if declared := w.sent.Values("Trailer"); len(declared) > 0 {
		resp.Trailer = http.Header{}
		for _, v := range declared {
			for _, k := range strings.Split(v, ",") {
				if k = strings.TrimSpace(k); k != "" {
					resp.Trailer[http.CanonicalHeaderKey(k)] = nil
				}
			}
		}
		resp.Header.Del("Trailer")
	}
	resp.Body = &body{PipeReader: pr, w: w, resp: resp, cancel: cancel}
	return resp, nil
}

// writer is the handler's http.ResponseWriter: its head is handed over once, at
// the first WriteHeader, Write or Flush, and its body through a pipe the caller
// reads, so a caller that stops reading stops the handler's writes.
type writer struct {
	header http.Header
	pipe   *io.PipeWriter

	once   sync.Once
	head   chan struct{} // closed when the head is handed over
	status int
	sent   http.Header // the header as handed over
	err    error       // why the handler ended before it began an answer
	done   http.Header // the header as the handler left it, for the trailers
}

func (w *writer) Header() http.Header { return w.header }

func (w *writer) WriteHeader(status int) {
	w.once.Do(func() {
		w.status = status
		w.sent = w.header.Clone()
		close(w.head)
	})
}

func (w *writer) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pipe.Write(p)
}

// Flush hands the head over. The pipe holds no bytes, so what was written has
// already reached the caller.
func (w *writer) Flush() { w.WriteHeader(http.StatusOK) }

// finish ends the body once the handler returns; one that wrote nothing answers
// 200 with no body, as net/http does.
func (w *writer) finish() {
	w.done = w.header.Clone()
	w.WriteHeader(http.StatusOK)
	_ = w.pipe.Close()
}

// fail ends a handler that panicked: before its head, the call fails; after, the
// body breaks.
func (w *writer) fail(err error) {
	w.once.Do(func() {
		w.err = err
		close(w.head)
	})
	_ = w.pipe.CloseWithError(err)
}

// body is the answer's body. At its end it states the handler's trailers: each
// key the head declared (Trailer), as the handler set it after its body, and any
// set under http.TrailerPrefix.
type body struct {
	*io.PipeReader
	w      *writer
	resp   *http.Response
	cancel context.CancelFunc
	once   sync.Once
}

func (b *body) Read(p []byte) (int, error) {
	n, err := b.PipeReader.Read(p)
	if err == io.EOF {
		b.once.Do(b.trailers)
	}
	return n, err
}

func (b *body) trailers() {
	for k, vs := range b.w.done {
		switch {
		case strings.HasPrefix(k, http.TrailerPrefix):
			if b.resp.Trailer == nil {
				b.resp.Trailer = http.Header{}
			}
			b.resp.Trailer[http.CanonicalHeaderKey(strings.TrimPrefix(k, http.TrailerPrefix))] = vs
		default:
			if _, declared := b.resp.Trailer[k]; declared {
				b.resp.Trailer[k] = vs
			}
		}
	}
}

// Close ends the call: a caller who closes the body before its end has left.
func (b *body) Close() error {
	b.cancel()
	return b.PipeReader.Close()
}
