// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package plugin_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/ai/plugin"
	"github.com/hanzoai/ai/internal/stub"
)

// relay registers the stub plugin as name, relaying to upstream, and returns a
// client whose transport answers plugin:// in process.
func relay(t *testing.T, name, upstream string, keys func(string) string) *http.Client {
	t.Helper()
	if keys == nil {
		keys = func(n string) string {
			if n == "STUB_KEY" {
				return "sk-held-by-the-host"
			}
			return ""
		}
	}
	plugin.Register(plugin.Plugin{
		Name:   name,
		Config: []byte(upstream),
		New:    func(h *plugin.Host) plugin.Guest { return stub.New(plugin.Imports[*stub.Module]{H: h}) },
		Keys:   keys,
	})
	tr := &http.Transport{}
	tr.RegisterProtocol(plugin.Scheme, plugin.Transport{})
	return &http.Client{Transport: tr}
}

// A whole answer crosses as the family service would send it: the request as the
// caller wrote it, under the key the host holds rather than the caller's; the
// upstream's status, headers and body; the tells as X-Hanzo-* headers; and, once
// the body is read, what it cost and the arm that failed, as trailers declared
// up front.
func TestAWholeAnswerCrossesWithItsTellsAndCost(t *testing.T) {
	type seen struct{ method, path, auth, body string }
	got := make(chan seen, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), string(b)}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	c := relay(t, "whole", up.URL, nil)

	req, _ := http.NewRequest(http.MethodPost, "plugin://whole/v1/chat/completions", strings.NewReader(`{"model":"zen6"}`))
	req.Header.Set("Authorization", "Bearer the-family-service-key")
	req.Header.Set("X-Org-Id", "acme")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	s := <-got
	if s.method != http.MethodPost || s.path != "/v1/chat/completions" || s.body != `{"model":"zen6"}` {
		t.Errorf("upstream saw %+v", s)
	}
	if s.auth != "Bearer sk-held-by-the-host" {
		t.Errorf("upstream was sent Authorization %q, want the key the host resolved", s.auth)
	}
	if resp.StatusCode != 201 || resp.Header.Get("X-Upstream") != "yes" || resp.Header.Get("X-Hanzo-Arm") != "stub-arm" || resp.Header.Get("X-Hanzo-Provider") != "stub" {
		t.Errorf("answer %d %v", resp.StatusCode, resp.Header)
	}
	if _, declared := resp.Trailer["X-Hanzo-Cogs"]; !declared {
		t.Errorf("trailers declared %v, want X-Hanzo-Cogs before the body is read", resp.Trailer)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != `{"ok":true}` {
		t.Errorf("body %q", b)
	}
	if resp.Trailer.Get("X-Hanzo-Cogs") != "0.000123" || resp.Trailer.Get("X-Hanzo-Failover") != "vendor/a (free): upstream status 429" {
		t.Errorf("trailers %v", resp.Trailer)
	}
}

// A stream reaches the caller as the upstream writes it, not once it has ended.
func TestAStreamArrivesAsItIsWritten(t *testing.T) {
	next := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			<-next
		}
	}))
	defer up.Close()
	defer close(next)
	c := relay(t, "stream", up.URL, nil)

	resp, err := c.Post("plugin://stream/v1/chat/completions", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	for i := range 3 {
		line := make(chan string, 1)
		go func() {
			l, _ := br.ReadString('\n')
			_, _ = br.ReadString('\n')
			line <- l
		}()
		select {
		case l := <-line:
			if l != fmt.Sprintf("data: %d\n", i) {
				t.Fatalf("event %d = %q", i, l)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("event %d did not arrive while the upstream held the next", i)
		}
		next <- struct{}{}
	}
}

// A caller that stops reading stops the plugin writing, and the plugin stops
// reading its upstream: what the host holds stays within a window either side
// rather than the whole answer.
func TestACallerWhoStopsReadingStopsTheUpstream(t *testing.T) {
	const total = 64 << 20
	var sent atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for sent.Load() < total {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			sent.Add(int64(len(chunk)))
		}
	}))
	defer up.Close()
	c := relay(t, "slow", up.URL, nil)

	resp, err := c.Get("plugin://slow/v1/big")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	one := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, one); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	stalled := sent.Load()
	if stalled >= total {
		t.Fatalf("the upstream sent all %d bytes to a caller that read one", stalled)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil || n+1 != total {
		t.Fatalf("read %d (%v), want the rest of %d", n, err, total)
	}
	t.Logf("held while the caller paused: %d of %d bytes sent upstream", stalled, total)
}

// A caller who leaves mid-answer ends the call: the plugin is told, and it hangs
// up on its upstream.
func TestACallerWhoLeavesHangsUpTheUpstream(t *testing.T) {
	gone := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(gone)
	}))
	defer up.Close()
	c := relay(t, "leave", up.URL, nil)

	resp, err := c.Get("plugin://leave/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream was never hung up on")
	}
}

// A key the host will not give the plugin is refused at hz_open, and the plugin
// answers for itself; the upstream is never asked.
func TestAKeyTheHostWillNotResolveIsDenied(t *testing.T) {
	var asked atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked.Add(1) }))
	defer up.Close()
	c := relay(t, "nokey", up.URL, func(string) string { return "" })

	resp, err := c.Get("plugin://nokey/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(b), "no upstream") || asked.Load() != 0 {
		t.Errorf("answered %d %s, upstream asked %d times", resp.StatusCode, b, asked.Load())
	}
}

// A call the plugin does not reply to within the bound is ended, and its
// upstream hung up on.
func TestACallThatDoesNotReplyInTimeIsEnded(t *testing.T) {
	gone := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(gone)
	}))
	defer up.Close()
	plugin.Register(plugin.Plugin{
		Name: "late", Config: []byte(up.URL), Reply: 200 * time.Millisecond,
		New:  func(h *plugin.Host) plugin.Guest { return stub.New(plugin.Imports[*stub.Module]{H: h}) },
		Keys: func(string) string { return "k" },
	})
	tr := &http.Transport{}
	tr.RegisterProtocol(plugin.Scheme, plugin.Transport{})
	start := time.Now()
	if _, err := (&http.Client{Transport: tr}).Get("plugin://late/v1/models"); err == nil {
		t.Fatal("a call with no reply answered")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("waited %s for a 200ms bound", d)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream of an ended call was never hung up on")
	}
}

// A plugin that traps ends the calls in flight, and the next request is served
// by a fresh instance.
func TestATrapEndsItsCallsAndTheNextIsServed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	c := relay(t, "trap", up.URL, nil)

	// The stub's arena holds 1 MiB: a body past it traps hz_alloc.
	if _, err := c.Post("plugin://trap/v1/x", "application/json", bytes.NewReader(make([]byte, 2<<20))); err == nil {
		t.Fatal("a request the plugin trapped on was answered")
	}
	resp, err := c.Get("plugin://trap/v1/x")
	if err != nil {
		t.Fatalf("the request after a trap: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "ok" {
		t.Errorf("after a trap answered %q", b)
	}
}

// One instance answers many callers at once.
func TestManyCallsAtOnce(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer up.Close()
	c := relay(t, "many", up.URL, nil)

	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("/v1/n%d", i)
			resp, err := c.Get("plugin://many" + path)
			if err != nil {
				errs <- err
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(b) != path {
				errs <- fmt.Errorf("%s answered %q", path, b)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// A request for a plugin nobody registered fails at once.
func TestAnUnregisteredPluginIsAnError(t *testing.T) {
	tr := &http.Transport{}
	tr.RegisterProtocol(plugin.Scheme, plugin.Transport{})
	if _, err := (&http.Client{Transport: tr}).Get("plugin://nobody/v1/models"); err == nil {
		t.Fatal("an unregistered plugin answered")
	}
}

// A caller's context ending before the reply ends the call.
func TestACallerWhoGivesUpBeforeTheReplyEndsTheCall(t *testing.T) {
	gone := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(gone)
	}))
	defer up.Close()
	c := relay(t, "giveup", up.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "plugin://giveup/v1/models", nil)
	if _, err := c.Do(req); err == nil {
		t.Fatal("answered past the caller's deadline")
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream was never hung up on")
	}
}
