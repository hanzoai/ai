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
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/ai/object"
)

// states is a `capture` object as the decision service writes one.
const states = `{"sha256":"aa","width":2,"questions":{"is_bug":{"type":"noul","labels":["false","true"],"pooled":"AAAAAA==","logits":[0,3],"probs":[0.04,0.96]}}}`

// An org that captures asks the service for its states, its caller is answered
// without them, byte for byte as without capture, and the host is handed them
// under the decision's id. An org that does not capture never asks, and a
// `capture` object the service sends anyway never reaches its caller either.
func TestDecisionStatesAreTheOrgsAndNeverTheCallers(t *testing.T) {
	fake, _ := setupDecisions(t)
	withStates := strings.TrimSuffix(decisionAnswer, "}") + `,"capture":` + states + `}`
	var mu sync.Mutex
	var asked []string
	fake.serve = func(_ string, _ []byte) (int, string) { return http.StatusOK, withStates }
	var got []object.Capture
	on := map[string]bool{decisionsOrg: true}
	object.SetCapture(func(_ context.Context, org string) bool { return on[org] },
		func(_ context.Context, c object.Capture) { mu.Lock(); got = append(got, c); mu.Unlock() })
	t.Cleanup(func() { object.SetCapture(nil, nil) })
	header := func() string {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.capture
	}

	status, body := driveDecisions(t, "Bearer "+decisionsKey, decisionBody)
	asked = append(asked, header())
	if status != http.StatusOK || body != decisionAnswer {
		t.Fatalf("status %d body %s, want 200 and the answer without its states", status, body)
	}
	if len(got) != 1 || got[0].Org != decisionsOrg || got[0].Decision != "dec_1" || string(got[0].States) != states || got[0].Model != "kai" {
		t.Fatalf("filed %+v, want acme's states under dec_1", got)
	}

	on[decisionsOrg] = false
	// a body the cache holds would answer without asking; this one differs
	status, body = driveDecisions(t, "Bearer "+decisionsKey, strings.Replace(decisionBody, "twice", "thrice", 1))
	asked = append(asked, header())
	if status != http.StatusOK || body != decisionAnswer {
		t.Fatalf("status %d body %s, want the answer without the states the service sent", status, body)
	}
	if len(got) != 1 {
		t.Fatalf("filed %d captures, want none for an org that does not capture", len(got)-1)
	}
	if asked[0] != "1" || asked[1] != "" {
		t.Fatalf("X-Capture sent %q then %q, want 1 then none", asked[0], asked[1])
	}
}

// uncapture keeps every other member's bytes and place, and leaves a body with
// no `capture` member untouched.
func TestUncaptureKeepsTheRest(t *testing.T) {
	in := `{"id":"dec_1","answers":{"a":{"capture":1}},"capture":{"x":[1,2.50]},"latency_ms":1.50}`
	out, st := uncapture([]byte(in))
	if string(out) != `{"id":"dec_1","answers":{"a":{"capture":1}},"latency_ms":1.50}` || string(st) != `{"x":[1,2.50]}` {
		t.Fatalf("uncapture = %s, %s", out, st)
	}
	plain := `{"id":"dec_1","answers":{"a":{"capture":1}}}`
	if out, st := uncapture([]byte(plain)); string(out) != plain || st != nil {
		t.Fatalf("a body with no capture member changed: %s, %s", out, st)
	}
}
