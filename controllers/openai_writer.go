// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/go-openai"
)

// OpenAIWriter turns the upstream's event stream into OpenAI's, and writes the
// result to `out`.
//
// It is an io.Writer with an io.Writer inside it, and the inner one is a FIELD
// because the destination is a value the caller chooses, not a place to be
// reached for. /v1/responses composes its own translating writer here and reads
// the same chat stream in a second dialect; giving this type an http.ResponseWriter
// meant that caller had to reach into the request context and swap what it found.
type OpenAIWriter struct {
	out        io.Writer
	Cleaner    Cleaner
	Buffer     []byte
	MessageBuf []byte
	RequestID  string
	Stream     bool
	StreamSent bool
	Model      string
	// IncludeUsage mirrors the request's stream_options.include_usage. Per the
	// OpenAI spec the trailing empty-choices usage chunk is emitted ONLY when the
	// client asks for it; sending it unconditionally breaks clients that read
	// choices[0] on every chunk.
	IncludeUsage bool
}

// Flush pushes what has been written as far as the destination allows, and is
// what makes the stream a stream: an event that sits in a buffer until the reply
// ends has been typed, not streamed. Best-effort, like the http.Flusher assertion
// it replaces — a destination that cannot flush (io.Discard, on the non-streaming
// path) has nothing to push.
//
// Two shapes, because both are real here: a *bufio.Writer from the stream callback
// returns an error, and a wrapper that forwards to one may not.
func (w *OpenAIWriter) Flush() {
	switch f := w.out.(type) {
	case interface{ Flush() error }:
		_ = f.Flush()
	case interface{ Flush() }:
		f.Flush()
	}
}

// Write processes incoming data chunks and formats them for OpenAI compatibility
func (w *OpenAIWriter) Write(p []byte) (n int, err error) {
	// Parse the incoming SSE message format
	var content string

	if bytes.HasPrefix(p, []byte("event: message\ndata: ")) {
		prefix := []byte("event: message\ndata: ")
		suffix := []byte("\n\n")
		content = string(bytes.TrimSuffix(bytes.TrimPrefix(p, prefix), suffix))

		// Add content to message buffer
		w.MessageBuf = append(w.MessageBuf, []byte(content)...)
	} else if bytes.HasPrefix(p, []byte("event: reason\ndata: ")) {
		// We don't expose reason data in OpenAI format, but we'll store it
		prefix := []byte("event: reason\ndata: ")
		suffix := []byte("\n\n")
		content = string(bytes.TrimSuffix(bytes.TrimPrefix(p, prefix), suffix))
	} else {
		// If we can't parse, just store the raw bytes and attempt to clean
		content = w.Cleaner.CleanString(string(p))
		if content != "" {
			w.MessageBuf = append(w.MessageBuf, []byte(content)...)
		}
	}

	// Always store the original bytes
	w.Buffer = append(w.Buffer, p...)

	// For non-streaming, just collect the data
	if !w.Stream {
		return len(p), nil
	}

	// Skip empty content
	if content == "" {
		return len(p), nil
	}

	// OpenAI streams the assistant role on the FIRST delta chunk; clients
	// (LangChain/LibreChat agents) read it to classify the streamed message.
	// Omitting it breaks their parser with "Cannot read properties of undefined
	// (reading 'role')" and the reply never renders.
	delta := openai.ChatCompletionStreamChoiceDelta{Content: content}
	if !w.StreamSent {
		delta.Role = "assistant"
	}

	// Create SSE chunk using go-openai library structure
	chunk := openai.ChatCompletionStreamResponse{
		ID:      "chatcmpl-" + w.RequestID,
		Object:  "chat.completion.chunk",
		Created: util.GetCurrentUnixTime(),
		Model:   w.Model,
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: openai.FinishReasonNull,
			},
		},
	}

	jsonData, err := json.Marshal(chunk)
	if err != nil {
		return 0, err
	}

	// Out, not this writer: Write is the formatter, so writing to itself would recur.
	_, err = w.out.Write([]byte(fmt.Sprintf("data: %s\n\n", jsonData)))
	if err != nil {
		return 0, err
	}

	w.StreamSent = true
	w.Flush()

	return len(p), nil
}

// MessageString returns the complete buffered message
func (w *OpenAIWriter) MessageString() string {
	return string(w.MessageBuf)
}

// Reset discards what a failed attempt accumulated, so the next provider's
// answer is not served glued to the dead one's half-sentence.
//
// Write APPENDS to Buffer and MessageBuf, and one writer is shared across every
// failover attempt. A provider that emitted three tokens and then died leaves
// those three tokens in the buffer; without this the client is handed them
// followed by a complete answer from somebody else, which is indistinguishable
// from a model losing its mind and is not detectable downstream.
//
// StreamSent is NOT cleared. It records that bytes reached the CLIENT, which is
// a fact about the wire and cannot be undone — it is precisely the flag that
// forbids the retry this method prepares for.
func (w *OpenAIWriter) Reset() {
	w.Buffer = w.Buffer[:0]
	w.MessageBuf = w.MessageBuf[:0]
	w.Cleaner = *NewCleaner(w.Cleaner.bufferSize)
}

// fork makes a writer for ONE raced attempt, over that attempt's own share of
// the stream.
//
// Everything about the DIALECT is copied — the request id, the model, whether
// this is a stream, whether the client asked for a usage chunk — because every
// attempt is answering the same request and must speak the same way. Everything
// about the ANSWER starts empty, because that is the whole reason a race cannot
// share one writer: the losers' token counts are what the ledger needs, and one
// set of buffers holds exactly one of them.
//
// StreamSent starts false for the same reason it is never cleared by Reset. It
// records that bytes reached the CLIENT, and for a fork that has not yet won the
// race, none have.
func (w *OpenAIWriter) fork(out io.Writer) *OpenAIWriter {
	return &OpenAIWriter{
		out:          out,
		Cleaner:      *NewCleaner(w.Cleaner.bufferSize),
		Buffer:       []byte{},
		RequestID:    w.RequestID,
		Stream:       w.Stream,
		Model:        w.Model,
		IncludeUsage: w.IncludeUsage,
	}
}

// adopt takes the winning attempt's answer as this writer's own, so the handler
// that has held this writer all along reads what actually served: the message
// body for a non-streaming reply, and StreamSent for whether Close still owes
// the client a final chunk.
//
// `out` is deliberately NOT adopted. The winner wrote through the race's shared
// stream, which is spent the moment the race resolves; this writer's own
// destination is the response body, and the tail belongs there directly.
func (w *OpenAIWriter) adopt(won io.Writer) {
	o, ok := won.(*OpenAIWriter)
	if !ok || o == nil {
		return
	}
	w.Buffer = o.Buffer
	w.MessageBuf = o.MessageBuf
	w.Cleaner = o.Cleaner
	w.StreamSent = o.StreamSent
}

// Close finalizes the stream by sending completion message and DONE marker
func (w *OpenAIWriter) Close(promptTokens, completionTokens, totalTokens int) error {
	if !w.Stream {
		return nil
	}

	if w.StreamSent {
		// Send final message with finish_reason
		chunk := openai.ChatCompletionStreamResponse{
			ID:      "chatcmpl-" + w.RequestID,
			Object:  "chat.completion.chunk",
			Created: util.GetCurrentUnixTime(),
			Model:   w.Model,
			Choices: []openai.ChatCompletionStreamChoice{
				{
					Index:        0,
					Delta:        openai.ChatCompletionStreamChoiceDelta{}, // Empty delta
					FinishReason: openai.FinishReasonStop,
				},
			},
		}

		jsonData, err := json.Marshal(chunk)
		if err != nil {
			return err
		}

		_, err = w.out.Write([]byte(fmt.Sprintf("data: %s\n\n", jsonData)))
		if err != nil {
			return err
		}

		// Send usage as a proper OpenAI SSE chunk so SDK clients (v6+) can parse
		// it — but ONLY when the client opted in via stream_options.include_usage.
		// This chunk's choices array is always empty; emitting it unconditionally
		// crashes clients that read choices[0] on every chunk.
		if w.IncludeUsage {
			usageChunk := map[string]interface{}{
				"id":      "chatcmpl-" + w.RequestID,
				"object":  "chat.completion.chunk",
				"created": util.GetCurrentUnixTime(),
				"model":   w.Model,
				"choices": []interface{}{},
				"usage": openai.Usage{
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
					TotalTokens:      totalTokens,
				},
			}

			usageData, err := json.Marshal(usageChunk)
			if err != nil {
				return err
			}

			_, err = w.out.Write([]byte(fmt.Sprintf("data: %s\n\n", usageData)))
			if err != nil {
				return err
			}
		}

		// Final [DONE] marker for SSE
		_, err = w.out.Write([]byte("data: [DONE]\n\n"))
		if err != nil {
			return err
		}

		w.Flush()
	}

	return nil
}
