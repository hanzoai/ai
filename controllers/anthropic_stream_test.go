// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func streamer(buf *bytes.Buffer, stream bool) *AnthropicWriter {
	return &AnthropicWriter{
		Writer:    bufio.NewWriter(buf),
		Cleaner:   *NewCleaner(0),
		RequestID: "req-1",
		Model:     "a-model",
		Stream:    stream,
	}
}

// events returns the SSE event names, in order, that reached the client.
func events(buf *bytes.Buffer) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "event: ") {
			out = append(out, strings.TrimPrefix(line, "event: "))
		}
	}
	return out
}

// A streamed answer is the Anthropic sequence, opened once and closed once.
func TestAStreamedAnswerIsTheAnthropicSequence(t *testing.T) {
	var buf bytes.Buffer
	w := streamer(&buf, true)

	if _, err := w.Write([]byte("Hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := w.Write([]byte(" world")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(3, 2, 5); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := events(&buf)
	want := []string{"message_start", "content_block_start", "content_block_delta",
		"content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v\nwant  = %v", got, want)
	}
	// The opening events are emitted once, however many chunks follow. Counted on
	// the event line, because the name also appears inside its own JSON payload.
	if n := strings.Count(buf.String(), "event: message_start"); n != 1 {
		t.Errorf("message_start was announced %d times, want 1", n)
	}
	if w.MessageString() != "Hello world" {
		t.Errorf("MessageString = %q, want %q", w.MessageString(), "Hello world")
	}
}

// Nothing was sent, so nothing is closed. A stop event for a stream that never
// opened is a message the client cannot place.
func TestClosingAStreamThatNeverOpenedSendsNothing(t *testing.T) {
	var buf bytes.Buffer
	w := streamer(&buf, true)
	if err := w.Close(0, 0, 0); err != nil {
		t.Fatalf("close: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q to a stream that never opened", buf.String())
	}
}

// A non-streaming answer accumulates and sends no events at all.
func TestANonStreamingAnswerSendsNoEvents(t *testing.T) {
	var buf bytes.Buffer
	w := streamer(&buf, false)
	if _, err := w.Write([]byte("Hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(1, 1, 2); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := events(&buf); len(got) != 0 {
		t.Errorf("a non-streaming answer emitted %v", got)
	}
	if w.MessageString() != "Hello" {
		t.Errorf("MessageString = %q, want Hello", w.MessageString())
	}
}

// RESET PREPARES A RETRY AND MUST NOT ERASE WHAT THE CLIENT ALREADY HAS.
//
// One writer is shared across every failover attempt, so Reset drops the dead
// vendor's half-sentence before the next one answers. What it must NOT drop is
// the record that bytes reached the client: that fact cannot be undone, and it is
// exactly what forbids the retry Reset is preparing for. Clearing it would let
// the failover loop offer a request whose opening is already on the wire, and the
// client would receive the start of one vendor's answer followed by the whole of
// another's — indistinguishable from a model losing its mind.
func TestResetForgetsTheAnswerAndNeverTheWire(t *testing.T) {
	var buf bytes.Buffer
	w := streamer(&buf, true)
	if _, err := w.Write([]byte("half a sentence")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !w.StreamSent {
		t.Fatal("bytes reached the client and StreamSent is false")
	}

	w.Reset()

	if w.MessageString() != "" {
		t.Errorf("Reset kept the dead attempt's answer: %q", w.MessageString())
	}
	if len(w.Buffer) != 0 {
		t.Errorf("Reset kept %d buffered bytes", len(w.Buffer))
	}
	if !w.StreamSent {
		t.Error("Reset cleared StreamSent — a request whose opening is on the wire would be offered to another vendor")
	}
	if !w.headerSent {
		t.Error("Reset cleared headerSent — the opening events would be sent a second time")
	}
}

// StreamSent flips where the bytes actually go out, not at the end of Write.
func TestStreamSentIsFalseUntilBytesLeave(t *testing.T) {
	var buf bytes.Buffer
	w := streamer(&buf, true)
	if w.StreamSent {
		t.Fatal("StreamSent is true before anything was written")
	}
	// Content the cleaner drops writes nothing, so nothing has been sent.
	if _, err := w.Write([]byte("")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if w.StreamSent {
		t.Errorf("StreamSent went true without any bytes reaching the client: %q", buf.String())
	}
}
