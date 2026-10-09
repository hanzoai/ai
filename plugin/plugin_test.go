// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package plugin_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/ai/plugin"
)

// client is an http.Client whose transport answers plugin:// in process.
func client(head time.Duration) *http.Client {
	tr := &http.Transport{}
	tr.RegisterProtocol(plugin.Scheme, plugin.Transport{Head: head})
	return &http.Client{Transport: tr}
}

// A whole answer crosses as the handler wrote it: the request as the caller sent
// it, the status, the headers, the body, and the trailers it declared, set once its
// body was written.
func TestAWholeAnswerCrossesWithItsTrailers(t *testing.T) {
	type seen struct{ method, uri, org, body string }
	got := make(chan seen, 1)
	plugin.Register("whole", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.RequestURI, r.Header.Get("X-Org-Id"), string(b)}
		w.Header().Set("Trailer", "X-Hanzo-Cogs")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Hanzo-Arm", "vendor/model:free")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
		w.Header().Set("X-Hanzo-Cogs", "0.000123")
		w.Header().Set(http.TrailerPrefix+"X-Hanzo-Failover", "vendor/a (openrouter): upstream status 429")
	}))
	req, _ := http.NewRequest(http.MethodPost, "plugin://whole/v1/chat/completions?x=1", strings.NewReader(`{"model":"zen6"}`))
	req.Header.Set("X-Org-Id", "acme")
	resp, err := client(0).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	s := <-got
	if s.method != http.MethodPost || s.uri != "/v1/chat/completions?x=1" || s.org != "acme" || s.body != `{"model":"zen6"}` {
		t.Errorf("the handler saw %+v", s)
	}
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("X-Hanzo-Arm") != "vendor/model:free" || resp.Header.Get("Trailer") != "" {
		t.Errorf("answer %d %v", resp.StatusCode, resp.Header)
	}
	if _, declared := resp.Trailer["X-Hanzo-Cogs"]; !declared {
		t.Errorf("trailers %v, want X-Hanzo-Cogs declared before the body is read", resp.Trailer)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != `{"ok":true}` {
		t.Errorf("body %q", b)
	}
	if resp.Trailer.Get("X-Hanzo-Cogs") != "0.000123" || resp.Trailer.Get("X-Hanzo-Failover") != "vendor/a (openrouter): upstream status 429" {
		t.Errorf("trailers %v", resp.Trailer)
	}
}

// A stream reaches the caller as the handler writes it, not once it has ended.
func TestAStreamArrivesAsItIsWritten(t *testing.T) {
	next := make(chan struct{})
	plugin.Register("stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-next:
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer close(next)
	resp, err := client(0).Get("plugin://stream/v1/chat/completions")
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
			t.Fatalf("event %d did not arrive while the handler held the next", i)
		}
		next <- struct{}{}
	}
}

// A caller who stops reading stops the handler's writes: nothing is buffered
// beyond what the caller has read.
func TestACallerWhoStopsReadingStopsTheHandler(t *testing.T) {
	var wrote atomic.Int64
	plugin.Register("slow", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for range 1024 {
			n, err := w.Write(chunk)
			wrote.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	resp, err := client(0).Get("plugin://slow/v1/big")
	if err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, one); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if held := wrote.Load(); held > 128<<10 {
		t.Fatalf("the handler wrote %d bytes to a caller that read one", held)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if n+1 != 1024*64<<10 {
		t.Fatalf("read %d, want the rest", n)
	}
}

// A caller who leaves ends the handler's context, so the handler hangs up on its
// upstream at once.
func TestACallerWhoLeavesEndsTheCall(t *testing.T) {
	gone := make(chan struct{})
	plugin.Register("leave", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(gone)
	}))
	resp, err := client(0).Get("plugin://leave/v1/stream")
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
		t.Fatal("the handler's context did not end when its caller left")
	}
}

// A caller whose context ends before the answer begins ends the call.
func TestACallerWhoGivesUpBeforeTheAnswerEndsTheCall(t *testing.T) {
	gone := make(chan struct{})
	plugin.Register("giveup", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(gone)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "plugin://giveup/v1/models", nil)
	if _, err := client(0).Do(req); err == nil {
		t.Fatal("answered past the caller's deadline")
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context did not end")
	}
}

// A handler that has not begun its answer within the head bound is ended.
func TestAnAnswerThatDoesNotBeginInTimeIsEnded(t *testing.T) {
	gone := make(chan struct{})
	plugin.Register("late", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(gone)
	}))
	start := time.Now()
	if _, err := client(150 * time.Millisecond).Get("plugin://late/v1/models"); err == nil {
		t.Fatal("an answer that never began was returned")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("waited %s for a 150ms bound", d)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the late handler was not ended")
	}
}

// A handler that panics before its answer fails the call; after, the body breaks.
func TestAHandlerThatPanicsFailsItsCall(t *testing.T) {
	plugin.Register("panics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/later" {
			_, _ = w.Write([]byte("partial"))
		}
		panic("boom")
	}))
	if _, err := client(0).Get("plugin://panics/v1/early"); err == nil {
		t.Fatal("a panic before the answer answered")
	}
	resp, err := client(0).Get("plugin://panics/v1/later")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("a panic mid-answer read as a whole body")
	}
}

// A handler that writes nothing answers 200 with an empty body, as net/http does.
func TestAHandlerThatWritesNothingAnswersEmpty(t *testing.T) {
	plugin.Register("empty", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	resp, err := client(0).Get("plugin://empty/health")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || len(b) != 0 {
		t.Errorf("answered %d %q", resp.StatusCode, b)
	}
}

// A request for a plugin nobody registered fails at once.
func TestAnUnregisteredPluginIsAnError(t *testing.T) {
	if _, err := client(0).Get("plugin://nobody/v1/models"); err == nil {
		t.Fatal("an unregistered plugin answered")
	}
}
