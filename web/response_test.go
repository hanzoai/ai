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
	"net/http"
	"net/http/httptest"
	"testing"
)

// newResponse wraps a recorder the way the router does.
func newResponse(rw http.ResponseWriter) *Response {
	r := &Response{}
	r.reset(rw)
	return r
}

// TestResponseWriteMarksStarted proves the ×84 ResponseWriter path: a body
// write marks the reply started and forwards the bytes.
func TestResponseWriteMarksStarted(t *testing.T) {
	rec := httptest.NewRecorder()
	r := newResponse(rec)
	if r.Started {
		t.Fatal("fresh response must not be started")
	}
	n, err := r.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil)", n, err)
	}
	if !r.Started {
		t.Fatal("Write must set Started")
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q, want hello", rec.Body.String())
	}
}

// TestResponseWriteHeaderOnce proves a second WriteHeader is ignored so the
// handler chain cannot emit two status lines — matching beego's Response.
func TestResponseWriteHeaderOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	r := newResponse(rec)
	r.WriteHeader(http.StatusTeapot)
	r.WriteHeader(http.StatusOK) // ignored
	if r.Status != http.StatusTeapot {
		t.Fatalf("Status = %d, want %d", r.Status, http.StatusTeapot)
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("recorder Code = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

// flushRecorder records whether Flush was called.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() { f.flushed = true }

// TestResponseFlushPassthrough proves the streaming path: Flush reaches the
// underlying Flusher (server-sent events depend on this).
func TestResponseFlushPassthrough(t *testing.T) {
	fr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	r := newResponse(fr)
	r.Flush()
	if !fr.flushed {
		t.Fatal("Flush must reach the underlying Flusher")
	}
}

// TestResponseHijackUnsupported proves Hijack errors cleanly when the writer
// is not an http.Hijacker (rather than panicking).
func TestResponseHijackUnsupported(t *testing.T) {
	r := newResponse(httptest.NewRecorder())
	if _, _, err := r.Hijack(); err == nil {
		t.Fatal("Hijack on a non-hijacker must return an error")
	}
}

// TestResponseReset proves reset rebinds and clears status/started so a
// pooled context is safe to reuse.
func TestResponseReset(t *testing.T) {
	r := newResponse(httptest.NewRecorder())
	r.WriteHeader(http.StatusTeapot)
	r.reset(httptest.NewRecorder())
	if r.Started || r.Status != 0 {
		t.Fatalf("after reset: Started=%v Status=%d, want false/0", r.Started, r.Status)
	}
}
