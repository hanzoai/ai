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

// EVERY PUBLISHED OPERATION SAYS SOMETHING ABOUT ITSELF.
//
// [Document] is what hanzoai/cloud relays through a single `/v1/*` door and
// publishes as the fleet's contract, so an operation with no sentence there is one
// no generated SDK, MCP tool or CLI command can describe, and nothing downstream
// can supply it. The other half of the old bijection — prose about a route nobody
// serves — is gone as a class rather than tested: the document is built FROM the
// route table, so there is no longer anywhere for a phantom to be written.
func TestEveryPublishedOperationHasASentence(t *testing.T) {
	paths, _ := Document(built())["paths"].(map[string]any)

	var mute []string
	for path, item := range paths {
		for method, o := range item.(map[string]any) {
			op, _ := o.(map[string]any)
			if summary, _ := op["summary"].(string); strings.TrimSpace(summary) == "" {
				mute = append(mute, strings.ToUpper(method)+" "+path)
			}
		}
	}
	if len(mute) > 0 {
		sort.Strings(mute)
		t.Errorf("%d published operation(s) say nothing about themselves:\n  %s\n\n"+
			"The sentence is the Go doc comment on the handler the registration names; "+
			"`go generate ./routers` lifts it. Nothing downstream can supply it.",
			len(mute), strings.Join(mute, "\n  "))
	}
}

// THE DESCRIBED SURFACE IS THE SERVED ONE. A resource row whose generated address
// is not registered describes an operation nobody can call — the same phantom the
// deleted swagger.json was, arrived at from the other direction.
func TestEveryDescribedAddressIsServed(t *testing.T) {
	live := map[string]bool{}
	for pattern, methods := range Patterns(built()) {
		for _, m := range methods {
			for _, verb := range expand(m) {
				live[verb+" "+openAPIPath(pattern)] = true
			}
		}
	}
	for path, item := range items() {
		for method := range item.(map[string]any) {
			if at := strings.ToUpper(method) + " " + path; !live[at] {
				t.Errorf("the resource table describes %s, which App does not register", at)
			}
		}
	}
}

// The generated table is the ROUTER's, not a list beside it: every wired row must
// be a live registration. The reverse direction is covered above.
func TestWiredRowsAreRegisteredRoutes(t *testing.T) {
	patterns := Patterns(built())
	live := map[string]bool{}
	for pattern, methods := range patterns {
		for _, m := range methods {
			live[strings.ToUpper(m)+" "+pattern] = true
		}
	}
	for _, w := range wired {
		// A "*" row is a mapping that claims EVERY verb, and the live table honestly
		// lists them one by one — the router registers a handler per method, so there
		// is no "*" entry to match against. The path being served is what "*" asserts.
		if w.Method == "*" {
			if len(patterns[w.Path]) == 0 {
				t.Errorf("wired_gen.go carries * %s -> %s, and no verb is registered at that path — "+
					"re-run `go generate ./routers`", w.Path, w.Handler)
			}
			continue
		}
		if !live[w.Method+" "+w.Path] {
			t.Errorf("wired_gen.go carries %s %s -> %s, which is not registered — "+
				"re-run `go generate ./routers`", w.Method, w.Path, w.Handler)
		}
	}
}
