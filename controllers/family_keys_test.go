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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// accounts is a vendor that answers each request by the key it carries, and
// records the keys in the order they arrived.
type accounts struct {
	mu     sync.Mutex
	status map[string]int // bearer key → status; absent = 200
	asked  []string
	bodies []string
}

func (a *accounts) serve(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		b, _ := io.ReadAll(r.Body)
		var in struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(b, &in)
		a.mu.Lock()
		a.asked = append(a.asked, key)
		a.bodies = append(a.bodies, string(b))
		st, ok := a.status[key]
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if ok && st != http.StatusOK {
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"error":{"message":"refused","code":` + strconv.Itoa(st) + `}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"gen-1","model":"` + in.Model + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (a *accounts) calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.asked)
}

func (a *accounts) reset() {
	a.mu.Lock()
	a.asked, a.bodies = nil, nil
	a.mu.Unlock()
}

// keyedRequest is a request the way dispatch builds one: a body it can replay.
func keyedRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	b := []byte(`{"model":"vendor/paid","messages":[{"role":"user","content":"2+2?"}]}`)
	r, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	return r
}

var orProvider = &object.Provider{Owner: "admin", Name: "openrouter", Type: "OpenRouter"}

func sendOnce(t *testing.T, a *accounts, url string, keys []string, free bool) int {
	t.Helper()
	resp, err := sendKeyed(keyedRequest(t, url), orProvider, keys, free, http.DefaultClient.Do)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

var threeKeys = []string{"k1", "k2", "k3"}

func TestOpenRouterNamesItsKeysInOrderAndSkipsTheUnset(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "")
	t.Setenv("OPENROUTER_API_KEY_3", "k3")
	if got := openrouterFam.keys(); !slices.Equal(got, []string{"k1", "k3"}) {
		t.Fatalf("keys = %v, want [k1 k3] — the priority key first, an unset name skipped", got)
	}
	t.Setenv("OPENROUTER_API_KEY_2", "k1")
	if got := openrouterFam.keys(); !slices.Equal(got, []string{"k1", "k3"}) {
		t.Fatalf("keys = %v, want [k1 k3] — one account named twice is tried once", got)
	}
	if got := zenFam.keys(); got != nil {
		t.Fatalf("zen keys = %v, want nil — a family naming no ring sends on its provider's key", got)
	}
}

func TestTheFirstKeyServesWhenItCan(t *testing.T) {
	forgetKeys()
	a := &accounts{}
	s := a.serve(t)
	if st := sendOnce(t, a, s.URL, threeKeys, false); st != http.StatusOK {
		t.Fatalf("status %d", st)
	}
	if got := a.calls(); !slices.Equal(got, []string{"k1"}) {
		t.Fatalf("asked %v, want [k1]", got)
	}
}

func TestAnAccountRefusalMovesTheSameRequestToTheNextKey(t *testing.T) {
	for _, st := range []int{http.StatusPaymentRequired, http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(http.StatusText(st), func(t *testing.T) {
			forgetKeys()
			a := &accounts{status: map[string]int{"k1": st}}
			s := a.serve(t)
			if got := sendOnce(t, a, s.URL, threeKeys, false); got != http.StatusOK {
				t.Fatalf("status %d, want 200 from k2", got)
			}
			if got := a.calls(); !slices.Equal(got, []string{"k1", "k2"}) {
				t.Fatalf("asked %v, want [k1 k2]", got)
			}
			if a.bodies[0] != a.bodies[1] {
				t.Fatalf("the second key was sent a different body:\n%s\n%s", a.bodies[0], a.bodies[1])
			}
		})
	}
}

func TestTheRequestsOwnErrorIsNotRetried(t *testing.T) {
	for _, st := range []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(st), func(t *testing.T) {
			forgetKeys()
			a := &accounts{status: map[string]int{"k1": st}}
			s := a.serve(t)
			if got := sendOnce(t, a, s.URL, threeKeys, false); got != st {
				t.Fatalf("status %d, want %d as the vendor said it", got, st)
			}
			if got := a.calls(); !slices.Equal(got, []string{"k1"}) {
				t.Fatalf("asked %v, want [k1] only", got)
			}
		})
	}
}

func TestAKeyThatCannotPayCoolsThenIsProbedAgain(t *testing.T) {
	forgetKeys()
	now := time.Now()
	keyNow = func() time.Time { return now }
	t.Cleanup(func() { keyNow = time.Now })

	a := &accounts{status: map[string]int{"k1": http.StatusPaymentRequired}}
	s := a.serve(t)
	sendOnce(t, a, s.URL, threeKeys, false)

	a.reset()
	sendOnce(t, a, s.URL, threeKeys, false)
	if got := a.calls(); !slices.Equal(got, []string{"k2"}) {
		t.Fatalf("priced request asked %v, want [k2] — k1 is cooling", got)
	}

	a.reset()
	sendOnce(t, a, s.URL, threeKeys, true)
	if got := a.calls(); len(got) == 0 || got[0] != "k1" {
		t.Fatalf("free request asked %v, want k1 first — an account that cannot pay still serves free routes", got)
	}

	a.reset()
	now = now.Add(keyCool + time.Second)
	sendOnce(t, a, s.URL, threeKeys, false)
	if got := a.calls(); len(got) == 0 || got[0] != "k1" {
		t.Fatalf("after the cooldown asked %v, want k1 first — it is probed again", got)
	}
}

func TestARejectedKeyCoolsForEveryRoute(t *testing.T) {
	forgetKeys()
	a := &accounts{status: map[string]int{"k1": http.StatusUnauthorized}}
	s := a.serve(t)
	sendOnce(t, a, s.URL, threeKeys, false)
	a.reset()
	sendOnce(t, a, s.URL, threeKeys, true)
	if got := a.calls(); !slices.Equal(got, []string{"k2"}) {
		t.Fatalf("free request asked %v, want [k2] — a rejected key is out for every route", got)
	}
}

func TestARateLimitedKeyDoesNotCool(t *testing.T) {
	forgetKeys()
	a := &accounts{status: map[string]int{"k1": http.StatusTooManyRequests}}
	s := a.serve(t)
	sendOnce(t, a, s.URL, threeKeys, false)
	a.reset()
	sendOnce(t, a, s.URL, threeKeys, false)
	if got := a.calls(); len(got) == 0 || got[0] != "k1" {
		t.Fatalf("asked %v, want k1 first — a 429 moves one request, it does not bench the account", got)
	}
}

func TestWhenEveryKeyRefusesTheLastRefusalIsTheAnswer(t *testing.T) {
	forgetKeys()
	a := &accounts{status: map[string]int{"k1": 402, "k2": 402, "k3": 402}}
	s := a.serve(t)
	if got := sendOnce(t, a, s.URL, threeKeys, false); got != http.StatusPaymentRequired {
		t.Fatalf("status %d, want the vendor's 402", got)
	}
	if got := a.calls(); !slices.Equal(got, threeKeys) {
		t.Fatalf("asked %v, want every key once", got)
	}
	a.reset()
	if got := sendOnce(t, a, s.URL, threeKeys, false); got != http.StatusPaymentRequired {
		t.Fatalf("status %d with every key cooling, want 402 restated", got)
	}
	if got := a.calls(); len(got) != 0 {
		t.Fatalf("asked %v with every key cooling, want no round trip", got)
	}
}

// Through the real pipe: the first account cannot pay and the second serves;
// and when none can, the caller gets a supply refusal and not a bill.
func TestThePipeServesOnTheNextAccountAndNeverBillsTheCaller(t *testing.T) {
	const paid = "vendor/paid-a"
	const free = "vendor/big:free"
	restore(t, engineFam)
	engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
	engineFam.providerFn = nil
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "k2")
	t.Setenv("OPENROUTER_API_KEY_3", "")

	body := []byte(`{"model":"` + paid + `","messages":[{"role":"user","content":"2+2?"}]}`)
	run := func(a *accounts) (*ApiController, []attempt) {
		s := a.serve(t)
		fam := spareFamily(t, s.URL, free, paid)
		c := visit(http.MethodPost, "/v1/chat/completions")
		c.Fiber().Request().SetBody(body)
		return c, c.pipeToFamily(fam, "chat/completions", "openai", paid, body, false, "acme", nil, false, nil, time.Now())
	}

	forgetKeys()
	cooled.forget()
	a := &accounts{status: map[string]int{"k1": http.StatusPaymentRequired}}
	c, out := run(a)
	if out != nil || !strings.Contains(sent(c), `"ok"`) {
		t.Fatalf("attempts=%v sent=%s — k2 should have served", out, sent(c))
	}
	if got := a.calls(); !slices.Equal(got, []string{"k1", "k2"}) {
		t.Fatalf("asked %v, want [k1 k2]", got)
	}

	forgetKeys()
	cooled.forget()
	a = &accounts{status: map[string]int{"k1": http.StatusPaymentRequired, "k2": http.StatusPaymentRequired}}
	_, out = run(a)
	if len(out) != 1 || out[0].status != http.StatusPaymentRequired || out[0].fault != faultProvider {
		t.Fatalf("attempts=%+v, want one provider-side 402", out)
	}
	var ae *apiError
	if err := exhausted(paid, out); !errors.As(err, &ae) || ae.status != http.StatusServiceUnavailable || ae.code == object.CodeInsufficientBalance {
		t.Fatalf("caller answer = %+v, want a 503 supply refusal, never insufficient_balance", ae)
	}
}
