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

	"github.com/hanzoai/decimal"
	"github.com/klauspost/compress/zstd"
)

// A family answer's cached prompt tokens bill at the SKU's discovered cache_read
// price, in both dialects' usage shapes; an SKU that states no cache price bills
// them at the input rate, never at a guessed discount.
func TestCachedFamilyTokensBillAtTheCacheRate(t *testing.T) {
	var wire struct {
		Data []zenWireModel `json:"data"`
	}
	if err := json.Unmarshal([]byte(`{"data":[{"id":"enso-flash","context_window":1000000,`+
		`"pricing":{"input":"0.42","output":"1.5","cache_read":"0.09"},`+
		`"pricing_tiers":[{"max_context":1000000,"input":"0.42","output":"1.5","cache_read":"0.09"}]}]}`), &wire); err != nil {
		t.Fatal(err)
	}
	zm := wire.Data[0].model()

	// OpenAI: prompt_tokens includes the cached ones.
	var oai tokens
	sniffZenUsage([]byte(`{"usage":{"prompt_tokens":1000000,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":800000}}}`), &oai)
	if oai != (tokens{fresh: 200000, cached: 800000}) {
		t.Fatalf("openai usage read as %+v", oai)
	}
	// Anthropic: cache_read_input_tokens is in addition to input_tokens.
	var ant tokens
	sniffZenUsage([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":200000,"cache_read_input_tokens":800000,"output_tokens":1}}}`), &ant)
	if ant.fresh != 200000 || ant.cached != 800000 {
		t.Fatalf("anthropic usage read as %+v", ant)
	}

	// 200k fresh at 0.42 + 800k cached at 0.09 = $0.084 + $0.072 = $0.156 → 16¢.
	// Without the cache term the same answer billed $0.42 → 42¢.
	if got := zm.costCents(oai.fresh, oai.cached, oai.completion); got != 16 {
		t.Errorf("costCents = %d¢, want 16¢", got)
	}
	p, _ := zm.price()
	if p.CacheReadPerMillion != 0.09 {
		t.Errorf("CacheReadPerMillion = %v, want the discovered 0.09", p.CacheReadPerMillion)
	}
	// The debit path reads the same price: 200k fresh + 800k cached, in nano-USD.
	if got := tokenNanoAt(p.InputPerMillion, p.OutputPerMillion, p.CacheReadPerMillion, p.CacheWritePerMillion, oai.fresh, 0, oai.cached, 0); got != 156_000_000 {
		t.Errorf("debit = %d nano-USD, want 156000000", got)
	}

	// No stated cache price: cached tokens bill at the input rate.
	nocache := zenModel{Base: zenTier{MaxCtx: 1000000, In: decimal.MustParse("0.42"), Out: decimal.MustParse("1.5")}}
	nocache.Tiers = []zenTier{nocache.Base}
	if got := nocache.costCents(200000, 800000, 0); got != 42 {
		t.Errorf("unpriced cache billed %d¢, want 42¢ at the input rate", got)
	}
	if p, _ := nocache.price(); p.CacheReadPerMillion != 0.42 {
		t.Errorf("unpriced cache reads %v, want the input rate 0.42", p.CacheReadPerMillion)
	}
}

// A zstd /v1/responses request that is not routed (a concrete model) is read once:
// the server has already expanded the body, so it is not expanded again.
func TestAZstdResponsesRequestIsReadOnce(t *testing.T) {
	token := mintUsageJWT(t, "acme", "ann")
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := visit(http.MethodPost, "/v1/responses")
	c.Fiber().Request().Header.Set("Authorization", "Bearer "+token)
	c.Fiber().Request().Header.Set("Content-Encoding", "zstd")
	c.Fiber().Request().SetBody(enc.EncodeAll([]byte(`{"model":"no-such-model-anywhere","input":"hi"}`), nil))
	c.Responses()
	body := string(c.Fiber().Response().Body())
	if strings.Contains(body, "decompress") || strings.Contains(body, "Failed to parse Responses") {
		t.Fatalf("a zstd Responses body was refused as unreadable: %d %s", c.Fiber().Response().StatusCode(), body)
	}
}
