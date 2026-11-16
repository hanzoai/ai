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

package controllers

import (
	"io"

	"github.com/hanzoai/ai/model"
)

// Answering reads the chat answer of a model a host serves in-process, the way
// every relay here reads one: inline reasoning off first, then the tool calls the
// model wrote as text lifted (lift.go), under the same gating. One per request.
type Answering struct {
	strip *model.ReasoningStripper
	lift  *lifter
	eaten bool
}

// ReadAnswer is the reader of the answer to the chat request body, served by the
// upstream model. It is nil when nothing is to be stripped or lifted: the answer
// then goes through as it came.
func ReadAnswer(body []byte, upstream string, strict bool) *Answering {
	eaten := model.OpenerEaten(upstream)
	a := &Answering{strip: model.NewReasoningStripper(upstream, eaten), lift: liftFor(body, upstream, strict), eaten: eaten}
	if a.strip == nil && a.lift == nil {
		return nil
	}
	return a
}

// Whole reads one finished chat completion.
func (a *Answering) Whole(body []byte) []byte {
	if a == nil {
		return body
	}
	return readAnswerBody(body, a.eaten, a.strip != nil, a.lift)
}

// Stream returns a writer for the chat SSE stream that writes the answer to w. Close
// ends the stream, lets go what is still held, and reports what w refused.
func (a *Answering) Stream(w io.Writer) io.WriteCloser {
	if a == nil {
		return nopCloser{w}
	}
	pr, pw := io.Pipe()
	s := &answerWriter{pw: pw, done: make(chan error, 1)}
	go func() {
		_, err := io.Copy(w, readAnswer(pr, a.strip, a.lift))
		pr.CloseWithError(err) // a refused write fails the writer, as a closed pipe does
		s.done <- err
	}()
	return s
}

type answerWriter struct {
	pw   *io.PipeWriter
	done chan error
}

func (s *answerWriter) Write(p []byte) (int, error) { return s.pw.Write(p) }

func (s *answerWriter) Close() error {
	_ = s.pw.Close()
	return <-s.done
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
