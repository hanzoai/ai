// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// endless is a family that streams tokens until its client hangs up, and reports
// the moment it was hung up on — the moment its model would stop.
func endless(t *testing.T) (url string, gone <-chan struct{}) {
	t.Helper()
	ended := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				close(ended)
				return
			case <-time.After(5 * time.Millisecond):
			}
			fmt.Fprintf(w, "data: {\"id\":\"gen-1\",\"choices\":[{\"delta\":{\"content\":\"t%d \"}}]}\n\n", i)
			f.Flush()
		}
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv.URL, ended
}

// hungUp is a client connection that took a few bytes and then went away.
type hungUp struct{ left int }

func (h *hungUp) Write(p []byte) (int, error) {
	if h.left <= 0 {
		return 0, errors.New("client gone")
	}
	h.left--
	return len(p), nil
}

// hungUpOn fails unless gone closes inside d: the family was hung up on.
func hungUpOn(t *testing.T, gone <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-gone:
	case <-time.After(d):
		t.Fatalf("the family was still generating %s after its client hung up", d)
	}
}

// A streamed answer whose client hangs up hangs up on the family, so the model stops
// rather than generating to its ceiling for nobody.
func TestARelayWhoseClientLeftHangsUpOnTheFamily(t *testing.T) {
	url, gone := endless(t)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer resp.Body.Close() // the caller's close, as pipeToFamily's stream does
		relayZenStream(bufio.NewWriterSize(&hungUp{left: 3}, 16), resp.Body, nil, nil)
	}()
	hungUpOn(t, gone, 2*time.Second)
}

// A whole answer being assembled hangs up on the family once its client is gone,
// noticed at the first beat that cannot be written.
func TestAnAssemblyWhoseClientLeftHangsUpOnTheFamily(t *testing.T) {
	url, gone := endless(t)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer resp.Body.Close()
		assembleZenStream(bufio.NewWriterSize(&hungUp{left: 1}, 1), resp.Body, nil, 20*time.Millisecond, nil, nil)
	}()
	hungUpOn(t, gone, 2*time.Second)
}

// A relayed chat stream whose client hangs up hangs up on the vendor, and bills the
// text that was relayed.
func TestARelayedChatWhoseClientLeftHangsUpOnTheVendor(t *testing.T) {
	url, gone := endless(t)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	text := make(chan string, 1)
	go func() {
		defer resp.Body.Close()
		w := bufio.NewWriterSize(&hungUp{left: 3}, 16)
		_, said := streamCaptureUsage(resp.Body, w, w.Flush, false, nil, nil)
		text <- said
	}()
	hungUpOn(t, gone, 2*time.Second)
	if said := <-text; said == "" {
		t.Fatal("the text relayed before the hangup is what bills, and none was kept")
	}
}

// An answer cut before the family states its usage is billed for the text that was
// relayed, not for nothing.
func TestAnAnswerCutBeforeItsUsageBillsTheTextRelayed(t *testing.T) {
	url, _ := endless(t)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	billed := make(chan tokens, 1)
	go func() {
		defer resp.Body.Close()
		tk, _, _, _ := relayZenStream(bufio.NewWriterSize(&hungUp{left: 6}, 16), resp.Body, nil, nil)
		billed <- tk
	}()
	select {
	case tk := <-billed:
		if tk.completion == 0 {
			t.Fatalf("a cut answer billed no completion: %+v", tk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the relay outlived its client and was never billed")
	}
}
