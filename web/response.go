// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package web

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// Response wraps http.ResponseWriter and records whether the reply has begun
// (Started) and the status written (Status). It passes Flush, Hijack,
// CloseNotify and Pusher through to the underlying writer so streaming and
// server-sent-event handlers keep working.
type Response struct {
	http.ResponseWriter
	Started bool
	Status  int
}

func (r *Response) reset(rw http.ResponseWriter) {
	r.ResponseWriter = rw
	r.Status = 0
	r.Started = false
}

// Write sends body bytes and marks the reply as started.
func (r *Response) Write(p []byte) (int, error) {
	r.Started = true
	return r.ResponseWriter.Write(p)
}

// WriteHeader sends the status line once; a second call is ignored so the
// handler chain cannot emit two WriteHeader calls.
func (r *Response) WriteHeader(code int) {
	if r.Status > 0 {
		return
	}
	r.Status = code
	r.Started = true
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer when it is an http.Flusher, so
// streaming responses can push buffered bytes.
func (r *Response) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer when it is an http.Hijacker.
func (r *Response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("web: response writer does not support hijacking")
	}
	return hj.Hijack()
}

// Pusher forwards to the underlying writer when it is an http.Pusher.
func (r *Response) Pusher() http.Pusher {
	if p, ok := r.ResponseWriter.(http.Pusher); ok {
		return p
	}
	return nil
}
