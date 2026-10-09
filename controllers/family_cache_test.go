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

	// 200k fresh at 0.42 + 800k cached at 0.09 = $0.084 + $0.072 = $0.156, exactly.
	// Without the cache term the same answer billed $0.42.
	if got := zm.retailNano(oai); got != 156_000_000 {
		t.Errorf("retailNano = %d, want 156000000 ($0.156)", got)
	}
	p, _ := zm.price()
	if p.CacheReadPerMillion != 0.09 {
		t.Errorf("CacheReadPerMillion = %v, want the discovered 0.09", p.CacheReadPerMillion)
	}
	// The debit path reads the same price: a 1M prompt, 800k of it cached, in nano-USD.
	if got := tokenNanoAt(p.InputPerMillion, p.OutputPerMillion, p.CacheReadPerMillion, p.CacheWritePerMillion, oai.prompt(), 0, oai.cached, 0); got != 156_000_000 {
		t.Errorf("debit = %d nano-USD, want 156000000", got)
	}

	// No stated cache price: cached tokens bill at the input rate.
	nocache := zenModel{Base: zenTier{MaxCtx: 1000000, In: decimal.MustParse("0.42"), Out: decimal.MustParse("1.5")}}
	nocache.Tiers = []zenTier{nocache.Base}
	if got := nocache.retailNano(tokens{fresh: 200000, cached: 800000, completion: 0}); got != 420_000_000 {
		t.Errorf("unpriced cache billed %d nano, want 420000000 ($0.42) at the input rate", got)
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

// Every usage shape is read into one prompt: every input token, the cache reads and
// writes parts of it. OpenAI counts its cached tokens inside prompt_tokens, OpenRouter
// its cache writes there too, and Anthropic counts both beside input_tokens.
func TestEveryUsageShapeCountsThePromptWhole(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       tokens
	}{
		{"openai", `{"usage":{"prompt_tokens":1000,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":800}}}`,
			tokens{fresh: 200, cached: 800, completion: 7, reported: true}},
		{"openrouter", `{"usage":{"prompt_tokens":10000,"completion_tokens":5,"cost":0.01,"prompt_tokens_details":{"cached_tokens":6000,"cache_write_tokens":1000}}}`,
			tokens{fresh: 3000, cached: 6000, written: 1000, completion: 5, reported: true}},
		{"anthropic", `{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800,"cache_creation_input_tokens":300,"output_tokens":1}}}`,
			tokens{fresh: 200, cached: 800, written: 300, completion: 1, reported: true}},
	} {
		var got tokens
		sniffZenUsage([]byte(tc.body), &got)
		if got != tc.want {
			t.Errorf("%s read as %+v, want %+v", tc.name, got, tc.want)
		}
	}
	// A record counts the same prompt, whole, on every path.
	rec := servedRecord(ServedUsage{PromptTokens: 1300, CachedTokens: 800, WrittenTokens: 300, CompletionTokens: 1})
	if rec.PromptTokens != 1300 || rec.CacheReadTokens != 800 || rec.CacheWriteTokens != 300 {
		t.Errorf("served record prompt=%d read=%d write=%d, want 1300/800/300", rec.PromptTokens, rec.CacheReadTokens, rec.CacheWriteTokens)
	}
}

// A cache write bills at the model's write rate: a resold model's is the vendor's
// input_cache_write plus the vendor's fee; a Hanzo model that states none bills a
// written token at its input rate. The table path bills the same prompt alike: the
// input rate on the part read fresh, each cache part at its own rate.
func TestACacheWriteBillsAtTheWriteRate(t *testing.T) {
	fee(t, "5.5")
	var w openrouterWireModel
	if err := json.Unmarshal([]byte(`{"id":"anthropic/claude-x","context_length":200000,"pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003","input_cache_write":"0.00000375"}}`), &w); err != nil {
		t.Fatal(err)
	}
	markup, err := openrouterMarkup()
	if err != nil {
		t.Fatal(err)
	}
	m := w.model(markup)
	// 1,000 fresh at $3.165/M, 2,000 read at $0.3165/M, 1,000 written at $3.95625/M.
	if got := m.retailNano(tokens{fresh: 1000, cached: 2000, written: 1000}); got != 3_165_000+633_000+3_956_250 {
		t.Errorf("resold retail = %d nano, want %d", got, 3_165_000+633_000+3_956_250)
	}
	ours := zenModel{Base: zenTier{MaxCtx: 1000000, In: decimal.MustParse("0.42"), Out: decimal.MustParse("1.5"), CacheRead: decimal.MustParse("0.09")}}
	ours.Tiers = []zenTier{ours.Base}
	if got := ours.retailNano(tokens{fresh: 1000, written: 1000}); got != 840_000 {
		t.Errorf("a write with no stated rate billed %d nano, want 840000 at the input rate", got)
	}
	// $2/M in, $0.20/M read, $2.50/M written: 200 fresh, 800 read, 300 written.
	if got := tokenNanoAt(2, 0, 0.2, 2.5, 1300, 0, 800, 300); got != 200*2000+800*200+300*2500 {
		t.Errorf("table path billed %d nano, want %d", got, 200*2000+800*200+300*2500)
	}
}
