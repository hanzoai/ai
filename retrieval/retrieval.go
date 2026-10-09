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

// Package retrieval states when a model call asks this module to search the org's
// document index into its prompt (controllers/chat_retrieval.go), and which store
// it names.
//
// A LEAF, on purpose: the host in front reads the same answer to refuse retrieval
// to a credential that may not read the org's documents (hanzo-inc/cloud refuses a
// delegated token one), and a host that kept its own copy of these spellings would
// silently disagree the day one changed here.
package retrieval

import (
	"encoding/json"
	"strings"
)

// flags is the ask as the BODY carries it. A browser preflights custom headers
// and the edge's CORS allow-list names only the standard ones, so a public page
// cannot send X-Retrieval — the body is how it asks.
type flags struct {
	Retrieval bool   `json:"retrieval"`
	Store     string `json:"retrieval_store"`
}

func fromBody(body []byte) flags {
	var f flags
	_ = json.Unmarshal(body, &f)
	return f
}

// Asked reports whether a request asks for retrieval: X-Retrieval when it is
// present decides ("1" or "true"), then X-Retrieval-Store, then the body's
// `retrieval` or `retrieval_store`. header reads one request header by name.
func Asked(header func(string) string, body []byte) bool {
	if v := header("X-Retrieval"); v != "" {
		return v == "1" || strings.EqualFold(v, "true")
	}
	if header("X-Retrieval-Store") != "" {
		return true
	}
	f := fromBody(body)
	return f.Retrieval || f.Store != ""
}

// Store names the store a request searches: the header if present, else the
// body, else empty — which the search resolves to the org's default.
func Store(header func(string) string, body []byte) string {
	if v := header("X-Retrieval-Store"); v != "" {
		return v
	}
	return fromBody(body).Store
}
