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

package routers

import (
	"sort"
	"strings"
	"testing"
)

// THE BIJECTION: every route this router serves has a sentence, and every
// sentence is about a route this router serves.
//
// Both halves fail the same way from the caller's side — an address with nothing
// said about it, or a promise about an address nobody can call — so both are one
// test. It is the whole contract [Prose] owes hanzoai/cloud, which relays this
// surface through a single `/v1/*` door and publishes what comes back as the
// fleet's document: a missing sentence there is an operation no generated SDK, MCP
// tool or CLI command can describe, and an extra one is a phantom.
func TestEveryRouteHasASentenceAndEverySentenceHasARoute(t *testing.T) {
	said := Prose()

	var mute []string
	live := map[string]bool{}
	for pattern, methods := range App.Patterns() {
		for _, m := range methods {
			key := strings.ToUpper(m) + " " + openAPIPath(pattern)
			live[key] = true
			if strings.TrimSpace(said[key].Summary) == "" {
				mute = append(mute, key)
			}
		}
	}
	if len(mute) > 0 {
		sort.Strings(mute)
		t.Errorf("%d served route(s) say nothing about themselves:\n  %s\n\n"+
			"The sentence is the Go doc comment on the handler the registration names; "+
			"`go generate ./routers` lifts it. Nothing downstream can supply it.",
			len(mute), strings.Join(mute, "\n  "))
	}

	var phantom []string
	for key := range said {
		if !live[key] {
			phantom = append(phantom, key)
		}
	}
	if len(phantom) > 0 {
		sort.Strings(phantom)
		t.Errorf("%d sentence(s) describe a route this router does not serve:\n  %s\n\n"+
			"Prose for an address nobody can call is published as an operation and read as a "+
			"promise. Key it to the route as the router registers it, or delete it with the route.",
			len(phantom), strings.Join(phantom, "\n  "))
	}
}

// The generated table is the ROUTER's, not a list beside it: every wired row must
// be a live registration. The reverse direction is covered above.
func TestWiredRowsAreRegisteredRoutes(t *testing.T) {
	live := map[string]bool{}
	for pattern, methods := range App.Patterns() {
		for _, m := range methods {
			live[strings.ToUpper(m)+" "+pattern] = true
		}
	}
	for _, w := range wired {
		if !live[w.Method+" "+w.Path] {
			t.Errorf("wired_gen.go carries %s %s -> %s, which App does not register — "+
				"re-run `go generate ./routers`", w.Method, w.Path, w.Handler)
		}
	}
}
