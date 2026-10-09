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

package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	iam "github.com/hanzoai/ai/internal/iam"
)

// No ZAP door serves a token IAM delegated for model calls (scope ai:inference,
// iam LLM.md "Delegation"). The routes such a token may reach are decided by the
// host's boundary, and that boundary stands in front of HTTP alone. A ZAP door has
// nothing in front of it and reaches far past model calls — the gateway falls back
// to the whole router, the forward bridge serves it, the ops node deploys — so
// every door refuses one outright, before any verifier is asked, whatever verifier
// the host installed (object.SetConfinedVerifier). The identity seams the doors
// share (zapResolveAuth, zapResolveUser, zapPrincipal) refuse one as well, so a
// door added later without the check still cannot turn one into a principal.

// zapConfinedRefusal is what a door answers a confined token.
const zapConfinedRefusal = "a token delegated for model calls is not served over ZAP"

var errZapConfined = errors.New(zapConfinedRefusal)

// confinedIn reports whether any field of values is a confined token. It reads
// every field rather than one header's one shape because ai takes a credential
// from several places — Authorization under any scheme, x-api-key, the IAM cookie
// — and a door that looked at one of them would pass the rest.
func confinedIn(values ...string) bool {
	split := func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",;=&", r) }
	for _, v := range values {
		for _, f := range strings.FieldsFunc(v, split) {
			if iam.Confined(f) {
				return true
			}
		}
	}
	return false
}

// confinedHeaders is confinedIn over a gateway request's JSON-encoded headers.
func confinedHeaders(headersJSON []byte) bool {
	var headers map[string]string
	if json.Unmarshal(headersJSON, &headers) != nil {
		return false
	}
	for _, v := range headers {
		if confinedIn(v) {
			return true
		}
	}
	return false
}

// confinedQuery is confinedIn over a raw query string, read decoded as the router
// would read it, and raw as well, should it not decode.
func confinedQuery(raw string) bool {
	if confinedIn(raw) {
		return true
	}
	q, _ := url.ParseQuery(raw)
	return confinedValues(q)
}

// confinedValues is confinedIn over a header set or a decoded query.
func confinedValues(m map[string][]string) bool {
	for _, vs := range m {
		if confinedIn(vs...) {
			return true
		}
	}
	return false
}

// unconfined guards the forward bridge, whose envelope carries the client's whole
// header set and query into the router.
func unconfined(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if confinedValues(r.Header) || (r.URL != nil && confinedQuery(r.URL.RawQuery)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": zapConfinedRefusal})
			return
		}
		h.ServeHTTP(w, r)
	})
}
