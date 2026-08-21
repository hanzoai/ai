package controllers

import (
	"bufio"
	"bytes"
	"net/http"
	"testing"
)

// EVERY STREAMING MODEL ADAPTER ASSERTS http.Flusher BEFORE IT EMITS A FRAME
// (model/claude.go, model/local.go, model/gemini.go and the rest all open with
// `writer.(http.Flusher)`). AnthropicWriter is the writer /v1/messages hands
// them, so if it does not satisfy that interface the endpoint cannot answer at
// all — which is what it did: "writer does not implement http.Flusher", 500,
// for every request.
//
// The trap is that it EMBEDS *bufio.Writer, which promotes a Flush that returns
// an error. That is not http.Flusher, but it reads like it at a glance and it
// compiles, so nothing caught it.
func TestAnthropicWriter_SatisfiesHTTPFlusher(t *testing.T) {
	var buf bytes.Buffer
	w := &AnthropicWriter{Writer: bufio.NewWriter(&buf)}

	f, ok := interface{}(w).(http.Flusher)
	if !ok {
		t.Fatal("AnthropicWriter does not implement http.Flusher — /v1/messages will 500 on every request")
	}

	if _, err := w.Writer.WriteString("frame"); err != nil {
		t.Fatal(err)
	}
	f.Flush()
	if buf.String() != "frame" {
		t.Fatalf("Flush did not reach the underlying writer: %q", buf.String())
	}
}

// A writer with no sink must not panic when a provider flushes it.
func TestAnthropicWriter_FlushWithNoSink(t *testing.T) {
	w := &AnthropicWriter{}
	w.Flush() // must be a no-op, not a nil dereference
}
