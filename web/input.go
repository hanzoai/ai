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
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"sync"
)

// Input reads the request line, headers, route parameters, per-request data
// and the cached body for one request.
type Input struct {
	Context     *Context
	CruSession  Store
	pnames      []string
	pvalues     []string
	data        map[interface{}]interface{}
	dataLock    sync.RWMutex
	RequestBody []byte

	// BodyTooLarge reports that the request carried more body than CopyBody was
	// allowed to read. It is the difference between a body AT the bound and one
	// OVER it, which a truncated read cannot express on its own — see CopyBody.
	BodyTooLarge bool
}

// NewInput returns an empty Input.
func NewInput() *Input {
	return &Input{
		pnames:  make([]string, 0),
		pvalues: make([]string, 0),
		data:    make(map[interface{}]interface{}),
	}
}

// Reset rebinds the Input to a context and clears per-request state.
func (input *Input) Reset(ctx *Context) {
	input.Context = ctx
	input.CruSession = nil
	input.pnames = input.pnames[:0]
	input.pvalues = input.pvalues[:0]
	input.dataLock.Lock()
	input.data = nil
	input.dataLock.Unlock()
	input.RequestBody = []byte{}
	input.BodyTooLarge = false
}

// URL returns the request path without query string or fragment.
func (input *Input) URL() string {
	return input.Context.Request.URL.EscapedPath()
}

// Method returns the request method.
func (input *Input) Method() string {
	return input.Context.Request.Method
}

// Param returns the value of a route parameter, or "" when absent.
func (input *Input) Param(key string) string {
	for i, v := range input.pnames {
		if v == key && i < len(input.pvalues) {
			return input.pvalues[i]
		}
	}
	return ""
}

// SetParam sets a route parameter, replacing any existing value for the key.
func (input *Input) SetParam(key, val string) {
	for i, v := range input.pnames {
		if v == key && i < len(input.pvalues) {
			input.pvalues[i] = val
			return
		}
	}
	input.pvalues = append(input.pvalues, val)
	input.pnames = append(input.pnames, key)
}

// Query returns a route parameter of the key, or the parsed form/query value
// when no such parameter exists.
func (input *Input) Query(key string) string {
	if val := input.Param(key); val != "" {
		return val
	}
	if input.Context.Request.Form == nil {
		input.Context.Request.ParseForm()
	}
	return input.Context.Request.Form.Get(key)
}

// Header returns a request header value.
func (input *Input) Header(key string) string {
	return input.Context.Request.Header.Get(key)
}

// Scheme returns the request scheme, preferring the X-Forwarded-Proto header,
// then the URL scheme, then "https" when the connection is TLS, else "http".
func (input *Input) Scheme() string {
	if scheme := input.Header("X-Forwarded-Proto"); scheme != "" {
		return scheme
	}
	if input.Context.Request.URL.Scheme != "" {
		return input.Context.Request.URL.Scheme
	}
	if input.Context.Request.TLS == nil {
		return "http"
	}
	return "https"
}

// Params returns all route parameters as a name-to-value map.
func (input *Input) Params() map[string]string {
	m := make(map[string]string)
	for i, v := range input.pnames {
		if i < len(input.pvalues) {
			m[v] = input.pvalues[i]
		}
	}
	return m
}

// Session returns a session value for key, or nil when no session is bound.
// A missing store yields nil rather than a panic, so an unauthenticated
// request resolves to "no session value" and falls through to credential auth.
func (input *Input) Session(key interface{}) interface{} {
	if input.CruSession == nil {
		return nil
	}
	return input.CruSession.Get(key)
}

// CopyBody reads the request body once (up to maxMemory, transparently
// gunzipping a gzip-encoded body), caches it on RequestBody, and rewinds
// Request.Body from the cache so later readers see the same bytes. It sets
// BodyTooLarge when the body exceeded maxMemory.
//
// The bound is on the bytes the HANDLER SEES, which is the only reading of a
// body limit that holds for a compressed body: a gzip stream costs what it
// EXPANDS to, not what it costs to send. Bounding the compressed stream alone
// bounds nothing — DEFLATE's ratio ceiling is ~1030:1, so 255 KiB on the wire
// becomes 256 MiB in this process, and the read runs before any filter and so
// before authentication. The limit therefore sits on the DECOMPRESSED reader.
//
// One byte past the bound is read on purpose: a body that is exactly at the
// limit and one that runs past it are otherwise the same length here, and
// telling them apart is what lets the router answer 413 rather than truncate
// the body silently and let it fail later as an unintelligible parse error.
func (input *Input) CopyBody(maxMemory int64) []byte {
	if input.Context.Request.Body == nil {
		return []byte{}
	}
	// +1 so a body at exactly maxMemory is not mistaken for one over it.
	src := io.Reader(&io.LimitedReader{R: input.Context.Request.Body, N: maxMemory + 1})
	if input.Header("Content-Encoding") == "gzip" {
		reader, err := gzip.NewReader(src)
		if err != nil {
			return nil
		}
		src = &io.LimitedReader{R: reader, N: maxMemory + 1}
	}
	requestbody, _ := io.ReadAll(src)
	input.Context.Request.Body.Close()
	if int64(len(requestbody)) > maxMemory {
		input.BodyTooLarge = true
		requestbody = requestbody[:maxMemory]
	}
	bf := bytes.NewBuffer(requestbody)
	input.Context.Request.Body = http.MaxBytesReader(input.Context.ResponseWriter, io.NopCloser(bf), maxMemory)
	input.RequestBody = requestbody
	return requestbody
}

// Data returns the per-request data map, allocating it on first use.
func (input *Input) Data() map[interface{}]interface{} {
	input.dataLock.Lock()
	defer input.dataLock.Unlock()
	if input.data == nil {
		input.data = make(map[interface{}]interface{})
	}
	return input.data
}

// GetData returns a per-request value, or nil when the key is unset.
func (input *Input) GetData(key interface{}) interface{} {
	input.dataLock.RLock()
	defer input.dataLock.RUnlock()
	if v, ok := input.data[key]; ok {
		return v
	}
	return nil
}

// SetData stores a per-request value.
func (input *Input) SetData(key, val interface{}) {
	input.dataLock.Lock()
	defer input.dataLock.Unlock()
	if input.data == nil {
		input.data = make(map[interface{}]interface{})
	}
	input.data[key] = val
}
