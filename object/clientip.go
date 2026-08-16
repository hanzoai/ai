// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package object

import "net/http"

// ClientIPFunc answers which address a request came from.
//
// THIS MODULE CANNOT SEE IT. Mounted in the host, ai's routes are reached through an
// adapter, and the peer does not survive the crossing — r.RemoteAddr inside a handler
// here is not the address that opened the connection. Anything derived from it is
// therefore the SAME value for every caller, which is not a degraded answer but a
// single answer wearing every caller's name.
//
// It cost us a live defect: the public lane keys its per-visitor ceiling on the
// caller's address, and with one address for everyone the ceiling became one bucket
// for the whole internet — five calls a day, globally. It failed safe, and it was
// invisible in both repositories, because the seam it crosses exists in neither.
//
// So the HOST answers. hanzoai/cloud already owns this question and has hardened it
// (peer truth, then the forwarded chain walked from the right across trusted proxies),
// and one definition is the point: a second derivation here would be a second answer
// to "who is calling", which is how the two come to disagree about who a caller is.
//
// nil (the default, standalone ai) → the caller falls back to reading the request
// itself, which is correct when nothing sits in front of it.
type ClientIPFunc func(r *http.Request) string

var clientIP ClientIPFunc

// SetClientIP installs the host's answer. Called once at mount.
func SetClientIP(f ClientIPFunc) { clientIP = f }

// ClientIP returns the installed resolver, or nil when nothing installed one.
func ClientIP() ClientIPFunc { return clientIP }
