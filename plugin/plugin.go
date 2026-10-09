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

// Package plugin runs a model family in this process: a Rust core compiled to
// wasm32-wasip1 and from there to a Go package by wasm2go, called directly with
// typed envelopes rather than over a network.
//
// A plugin is pure. It reaches nothing but this host: every upstream request it
// wants made, the host makes (hz_open), with the key it names resolved here so
// no key ever enters the plugin's memory, and every byte of its answer it hands
// to the host (hz_reply, hz_write, hz_end), which hands them to the caller at
// the caller's pace. One instance holds the plugin's state for every request:
// its own executor steps them all, and returns to the host whenever none can
// move (hz_poll). The host calls it again when one can.
//
// # The ABI
//
// Every name carries the hz_ prefix, so nothing a plugin links (wasi-libc's
// malloc, free, read and write) can collide with it.
//
// The plugin exports:
//
//	hz_alloc(len) -> ptr                  bytes for the host to write into
//	hz_free(ptr, len)
//	hz_init(cfg, len) -> 0 | code         its configuration, once, first
//	hz_begin(ask, len, body, len) -> call | code
//	hz_poll(now_ms) -> wake_ms | -1       step every call that can move
//	hz_cancel(call)                       the caller left
//
// The host provides, in import module "hanzo":
//
//	hz_open(call, meta, len, body, len) -> handle | code
//	hz_head(handle, buf, cap) -> n | Pending
//	hz_read(handle, buf, cap) -> n | 0 at the end | Pending | Cut
//	hz_close(handle)
//	hz_reply(call, meta, len) -> 0 | Gone | Invalid
//	hz_write(call, buf, len) -> n | Pending | Gone
//	hz_end(call, meta, len) -> 0 | Gone | Invalid
//	hz_now() -> ms
//
// Pointers are into the plugin's memory and are read or written only during the
// call that passes them: a buffer hz_begin is given is the host's again once
// hz_begin returns. Every meta is JSON (Ask, Open, Head, Reply, End); every body
// is bytes. hz_head writes nothing and returns the length it needs when the head
// is longer than cap. hz_write takes what fits and returns how much: Pending is a
// full buffer, the caller's pace, and the plugin writes the rest after the host
// wakes it.
//
// hz_poll is called after hz_begin, after anything a plugin waits on moves (an
// upstream's head or bytes, a caller who read, a caller who left) and at the
// wake time it returned. It returns once nothing can move.
package plugin

import "encoding/json"

// Codes a call into the host returns besides a length or a handle.
const (
	// Pending: not yet. The host calls hz_poll when it is.
	Pending int32 = -1
	// Cut: a body that broke before its end.
	Cut int32 = -2
	// Gone: the caller left; stop the call.
	Gone int32 = -3
	// Invalid: an argument the host cannot read.
	Invalid int32 = -4
	// Denied: a key this plugin may not use, or one that holds nothing.
	Denied int32 = -5
)

// Ask is one request a plugin answers, beside its body. The typed fields are
// what the families and Kai read off a request; Headers carries the rest
// (Accept, anthropic-version, …). No credential is ever in it.
type Ask struct {
	Method string `json:"method"`
	// Path is the route with its query: /v1/chat/completions, /v1/models.
	Path string `json:"path"`
	// Org is who the call is for (X-Org-Id).
	Org string `json:"org,omitempty"`
	// RequestID is the id the caller was answered under (X-Request-Id).
	RequestID string `json:"request_id,omitempty"`
	// Capabilities are the org's published capabilities a decision attaches
	// (X-Org-Capabilities), as JSON.
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	// Capture asks Kai for each question's pooled state (X-Capture).
	Capture bool `json:"capture,omitempty"`
	// Spend is what the caller's plan holds for the family's paid rungs, in USD
	// (X-Hanzo-Spend).
	Spend string `json:"spend,omitempty"`
	// Fronted says ai fronts the call and settles it (X-Hanzo-Fronted-By).
	Fronted bool        `json:"fronted,omitempty"`
	Headers [][2]string `json:"headers,omitempty"`
}

// Tells is what a family says about an answer beyond its body: the Hanzo SKU
// that answered, the upstream arm and the provider that ran it, whether that arm
// bills nothing, each arm that failed first (`<upstream> (<provider>): <why>`),
// and on a committed plan answer the most it can cost, in USD.
type Tells struct {
	Served   string   `json:"served,omitempty"`
	Arm      string   `json:"arm,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Free     bool     `json:"free,omitempty"`
	Failover []string `json:"failover,omitempty"`
	Bound    string   `json:"bound,omitempty"`
}

// Reply is an answer's head (hz_reply). Trailers names what its End will state,
// declared now as an HTTP answer declares them.
type Reply struct {
	Status  int         `json:"status"`
	Headers [][2]string `json:"headers,omitempty"`
	Tells
	Trailers []string `json:"trailers,omitempty"`
}

// End is what an answer states after its body (hz_end): what the request cost
// Hanzo upstream (Cogs, every arm it ran), what a committed plan answer's paid
// rung cost (Cost), both in USD, and the tells as they stood at the end.
type End struct {
	Tells
	Cogs string `json:"cogs,omitempty"`
	Cost string `json:"cost,omitempty"`
}

// Open is an upstream request a plugin asks the host to make (hz_open). Key is
// the NAME of the key the request goes under; the host resolves it and writes it
// into the request as Auth says: "bearer" (the default), "x-api-key", or
// "header:<Name>".
type Open struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers [][2]string `json:"headers,omitempty"`
	Key     string      `json:"key,omitempty"`
	Auth    string      `json:"auth,omitempty"`
	// Timeout bounds the whole exchange, body included; Head the wait for its
	// head. Milliseconds; zero is the host's default.
	Timeout int64 `json:"timeout_ms,omitempty"`
	Head    int64 `json:"head_ms,omitempty"`
}

// Head is an upstream answer's head as hz_head writes it. A request that never
// reached an answer is a head with status 0 and the error.
type Head struct {
	Status  int         `json:"status"`
	Headers [][2]string `json:"headers,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// Guest is a plugin's exports as wasm2go generates them: its memory and its hz_
// functions. A generated *Module is one.
type Guest interface {
	Memory() []byte
	HzAlloc(n int32) int32
	HzFree(ptr, n int32)
	HzInit(cfg, n int32) int32
	HzBegin(ask, askLen, body, bodyLen int32) int32
	HzPoll(now int64) int64
	HzCancel(call int32)
}
