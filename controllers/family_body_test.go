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
	"net/http"
	"strings"
	"testing"
	"time"
)

// The fields a reseller reads to spend the account on something the SKU never
// quoted: other models, other endpoints, extra completions, paid tools.
var steering = []string{"plugins", "web_search_options", "models", "transforms", "route", "n", "service_tier", "usage", "modalities", "audio", "prediction", "max_completion_tokens"}

const steeringChat = `{"model":"vendor/paid-a","messages":[{"role":"user","content":"hi"}],` +
	`"plugins":[{"id":"web","max_results":100}],"web_search_options":{"search_context_size":"high"},` +
	`"models":["openai/gpt-6-pro","anthropic/claude-opus-5.5"],"provider":{"sort":"price","order":["x"]},` +
	`"transforms":["middle-out"],"route":"fallback","n":8,"service_tier":"priority","usage":{"include":true},` +
	`"modalities":["text","audio"],"audio":{"voice":"alloy"},"prediction":{"type":"content","content":"x"},` +
	`"max_tokens":100000000,"max_completion_tokens":100000000,"temperature":0.2,` +
	`"tools":[{"type":"function","function":{"name":"f","parameters":{}}}],"reasoning_effort":"high"}`

func decoded(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %s", b)
	}
	return m
}

// A family is sent the fields that shape the answer and the reserved token ceiling,
// in every dialect.
func TestAFamilyIsSentOnlyWhatWasPriced(t *testing.T) {
	ceiling := clampMaxTokens(100000000)
	got := decoded(t, familyBody([]byte(steeringChat), "chat/completions", ceiling))
	for _, k := range append(steering, "provider") {
		if _, ok := got[k]; ok {
			t.Errorf("chat: %q is sent", k)
		}
	}
	for _, k := range []string{"model", "messages", "temperature", "tools", "reasoning_effort"} {
		if _, ok := got[k]; !ok {
			t.Errorf("chat: %q is dropped", k)
		}
	}
	if string(got["max_tokens"]) != jsonInt(ceiling) {
		t.Errorf("chat: max_tokens = %s, want the reserved %d", got["max_tokens"], ceiling)
	}

	msgs := decoded(t, familyBody([]byte(`{"model":"enso","max_tokens":100000000,"system":"s","messages":[],"thinking":{"type":"enabled","budget_tokens":2048},"plugins":[{"id":"web"}],"models":["x"]}`), "messages", ceiling))
	if _, ok := msgs["plugins"]; ok {
		t.Error("messages: plugins is sent")
	}
	if _, ok := msgs["models"]; ok {
		t.Error("messages: models is sent")
	}
	if _, ok := msgs["system"]; !ok {
		t.Error("messages: system is dropped")
	}
	if string(msgs["max_tokens"]) != jsonInt(ceiling) {
		t.Errorf("messages: max_tokens = %s, want %d", msgs["max_tokens"], ceiling)
	}

	emb := decoded(t, familyBody([]byte(`{"model":"zen-embedding","input":"x","dimensions":8,"provider":{"order":["y"]}}`), "embeddings", 0))
	if _, ok := emb["provider"]; ok {
		t.Error("embeddings: provider is sent")
	}
	if _, ok := emb["max_tokens"]; ok {
		t.Error("embeddings: a token ceiling was invented")
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

// Through the pipe to the reseller: the steering fields never leave, the vendor's
// own provider field carries only the terms ai states, and the token ceiling is the
// reserved one. A web-searching model id is refused before any vendor is called.
func TestTheResellerIsSentOnlyWhatWasPriced(t *testing.T) {
	const paid = "vendor/paid-a"
	restore(t, engineFam)
	engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
	engineFam.providerFn = nil
	t.Setenv("OPENROUTER_API_KEY", "k1")
	t.Setenv("OPENROUTER_API_KEY_2", "")
	t.Setenv("OPENROUTER_API_KEY_3", "")
	forgetKeys()
	cooled.forget()

	a := &accounts{status: map[string]int{}}
	s := a.serve(t)
	fam := spareFamily(t, s.URL, "vendor/big:free", paid, paid+":online")
	body := []byte(steeringChat)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if out := c.pipeToFamily(fam, "chat/completions", "openai", paid, body, false, clampMaxTokens(100000000), "acme", nil, false, nil, time.Now()); out != nil {
		t.Fatalf("attempts %+v", out)
	}
	a.mu.Lock()
	sentBodies := append([]string(nil), a.bodies...)
	a.mu.Unlock()
	if len(sentBodies) != 1 {
		t.Fatalf("vendor saw %d requests", len(sentBodies))
	}
	got := decoded(t, []byte(sentBodies[0]))
	for _, k := range steering {
		if _, ok := got[k]; ok {
			t.Errorf("%q reached the reseller: %s", k, sentBodies[0])
		}
	}
	var prov map[string]json.RawMessage
	_ = json.Unmarshal(got["provider"], &prov)
	if _, ok := prov["data_collection"]; !ok || len(prov) != 1 {
		t.Errorf("provider = %s, want only the terms ai states", got["provider"])
	}
	if string(got["max_tokens"]) != jsonInt(clampMaxTokens(100000000)) {
		t.Errorf("max_tokens = %s", got["max_tokens"])
	}

	c = visit(http.MethodPost, "/v1/chat/completions")
	online := []byte(strings.Replace(steeringChat, `"vendor/paid-a"`, `"vendor/paid-a:online"`, 1))
	c.Fiber().Request().SetBody(online)
	if out := c.pipeToFamily(fam, "chat/completions", "openai", paid+":online", online, false, 64, "acme", nil, false, nil, time.Now()); out != nil {
		t.Fatalf("attempts %+v", out)
	}
	if st := c.Fiber().Response().StatusCode(); st != http.StatusBadRequest {
		t.Errorf("a web-searching id answered %d, want 400", st)
	}
	if n := len(a.calls()); n != 1 {
		t.Errorf("a web-searching id reached the vendor (%d calls)", n)
	}
}

// A short id that resolves to a reseller's model (gpt-4o → openai/gpt-4o) is sent
// as that model with none of the steering fields the caller wrote.
func TestAnAliasIsSentOnlyWhatWasPriced(t *testing.T) {
	vendor := withAliasVendor(t)
	body := []byte(strings.Replace(steeringChat, `"vendor/paid-a"`, `"gpt-4o"`, 1))
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if refused := c.pipeToFamily(openrouterFam, "chat/completions", "openai", "gpt-4o", body, false, clampMaxTokens(100000000), "acme", nil, false, nil, time.Now()); refused != nil {
		t.Fatalf("refused: %+v", refused)
	}
	vendor.mu.Lock()
	bodies := append([]string(nil), vendor.bodies...)
	vendor.mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("vendor saw %d requests", len(bodies))
	}
	got := decoded(t, []byte(bodies[0]))
	if string(got["model"]) != `"openai/gpt-4o"` {
		t.Errorf("model = %s, want the vendor's own id", got["model"])
	}
	for _, k := range steering {
		if _, ok := got[k]; ok {
			t.Errorf("%q reached the reseller for gpt-4o: %s", k, bodies[0])
		}
	}
	var prov map[string]json.RawMessage
	_ = json.Unmarshal(got["provider"], &prov)
	if _, ok := prov["order"]; ok {
		t.Errorf("the caller's provider.order reached the reseller: %s", got["provider"])
	}
}
