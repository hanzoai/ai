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

// voice_vendor.go — speech a vendor's voice model writes for a caller.
//
// The resale family lists OpenRouter's speech models (ElevenLabs, MiniMax, Fish,
// Deepgram, Gemini TTS, …) beside its text and video lists. POST /v1/audio/speech
// for one is reserved at the most its text can cost, sent to OpenRouter's
// OpenAI-compatible POST /v1/audio/speech on one of our accounts, and answered
// with the audio. It bills what OpenRouter says the generation cost (GET
// /v1/generation), times the markup; when that is not stated in time, the
// listed rate for the characters spoken, or the reservation for a model priced
// by the second.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
	"github.com/hanzoai/decimal"
)

// voiceSpec is what a speech model takes and what it costs, as its vendor lists it.
type voiceSpec struct {
	Voices []string `json:"voices,omitempty"`
	// perChar is USD a character spoken (the vendor's prompt rate); perOut is USD
	// a second or an audio token of what it writes (its completion rate).
	perChar, perOut decimal.Decimal
}

// voiceFor is the speech SKU a model names on the resale family, and false for any
// other model.
func voiceFor(model string) (zenModel, bool) {
	if !openrouterFam.enabled() {
		return zenModel{}, false
	}
	openrouterFam.warm()
	m, ok := openrouterFam.lookup(model)
	return m, ok && m.Voice != nil
}

// speaksOnly reports an entry that answers in speech and nothing else.
func (w openrouterWireModel) speaksOnly() bool {
	out := w.Architecture.OutputModalities
	return len(out) == 1 && strings.EqualFold(strings.TrimSpace(out[0]), "speech")
}

// speech is a speech model as a discovered SKU: it takes text and answers audio,
// is billed at what each call cost (Variable), and its card is the vendor's rates
// times the markup. A model the vendor serves free is a free SKU; every other one
// takes both paid floors.
func (w openrouterWireModel) speech(margin decimal.Decimal) zenModel {
	m := zenModel{
		ID: w.ID, Created: w.Created, OwnedBy: openrouterOwner(w.ID), Name: w.title(),
		Description: strings.TrimSpace(w.Description), Expires: strings.TrimSpace(w.Expiration),
		Inputs: []string{"text"}, Outputs: []string{"speech"},
		Margin: margin, Variable: true, Card: w.Pricing.card(margin),
		Voice: &voiceSpec{Voices: w.SupportedVoices, perChar: w.Pricing.Prompt, perOut: w.Pricing.Completion},
		Tiers: []zenTier{{}},
	}
	if !w.free() {
		m.MinTier, m.Funding = "paid", "prepaid"
	} else {
		m.Variable, m.Card = false, nil
	}
	return m
}

// voiceSecondsPerChar bounds how long speech can run for its text: no voice
// speaks slower than eight characters a second, so a model priced by the second
// or by the audio token is reserved at that.
var voiceSecondsPerChar = decimal.MustParse("0.125")

// audioTokensPerSecond bounds the audio tokens a second of speech is written in.
const audioTokensPerSecond = 50

// voiceCeiling is the most speaking chars characters can cost at the vendor's
// price, in USD: every character at its rate, and the longest the speech can run
// at its rate for what it writes (read as seconds or as audio tokens, whichever is
// dearer).
func (v *voiceSpec) voiceCeiling(chars int) decimal.Decimal {
	n := decimal.New(int64(chars), 0)
	total := v.perChar.Mul(n)
	if v.perOut.Sign() > 0 {
		seconds := n.Mul(voiceSecondsPerChar)
		asTokens := seconds.Mul(decimal.New(audioTokensPerSecond, 0))
		total = total.Add(v.perOut.Mul(maxDecimal(seconds, asTokens)))
	}
	return total
}

// voiceQuoteNano is the most a speech request can cost the caller, in
// nano-dollars, and false for a body or a model it cannot bound.
func voiceQuoteNano(model string, body []byte) (int64, bool) {
	m, ok := voiceFor(model)
	if !ok {
		return 0, false
	}
	var r audioSpeechRequest
	if json.Unmarshal(body, &r) != nil {
		return 0, false
	}
	return nanoUp(m.Voice.voiceCeiling(utf8.RuneCountInString(r.Input)).Mul(m.Margin)), true
}

// generationCost asks OpenRouter what a generation cost (GET
// /v1/generation?id=), in nano-dollars, for a few seconds after it was made; nil
// when it has not said.
func generationCost(ctx context.Context, id, account string) *int64 {
	for i := 0; i < 6; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Duration(300*(i+1)) * time.Millisecond):
		}
		resp, _, err := orSend(ctx, http.MethodGet, "/v1/generation?id="+id, nil, account)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		var g struct {
			Data struct {
				Cost json.Number `json:"total_cost"`
			} `json:"data"`
		}
		if json.Unmarshal(b, &g) != nil || g.Data.Cost == "" {
			continue
		}
		if d, err := decimal.Parse(g.Data.Cost.String()); err == nil && d.Sign() > 0 {
			n := nanoUp(d)
			return &n
		}
	}
	log.Warn("speech: generation %s stated no cost", id)
	return nil
}

// orSend is one request to OpenRouter on our accounts: the account named, or each
// in turn until one is not refused for want of credit or as a credential. It
// answers the response, whose body the caller closes, and the account's name.
func orSend(ctx context.Context, method, path string, body []byte, account string) (*http.Response, string, error) {
	p := openrouterFam.provider()
	if p == nil {
		return nil, "", fmt.Errorf("the vendor is not configured")
	}
	type key struct{ name, value string }
	var keys []key
	if account != "" {
		keys = []key{{account, object.ResolveKey(account)}}
	} else {
		values := openrouterFam.keys()
		for _, v := range order(values, false) {
			if !cooling(v, false) {
				keys = append(keys, key{object.AccountOf(openrouterFam.keyNames, openrouterFam.keyList, v), v})
			}
		}
		if len(values) == 0 && strings.TrimSpace(p.ClientSecret) != "" {
			keys = []key{{p.Name, p.ClientSecret}}
		}
	}
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("no account with the vendor can pay")
	}
	base := strings.TrimRight(strings.TrimSpace(p.ProviderUrl), "/")
	var last *http.Response
	for _, k := range keys {
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return nil, "", err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		pk := *p
		pk.ClientSecret = k.value
		upstream.Authorize(req, &pk)
		resp, err := zenPipeClient.Do(req)
		if err != nil {
			return nil, "", err
		}
		switch resp.StatusCode {
		case http.StatusPaymentRequired:
			cool(k.value, scopePriced)
			emptied(openrouterFam.name, k.name, "", resp.StatusCode)
		case http.StatusUnauthorized:
			cool(k.value, scopeAll)
		default:
			if last != nil {
				last.Body.Close()
			}
			return resp, k.name, nil
		}
		if last != nil {
			last.Body.Close()
		}
		last = resp
		if account != "" {
			break
		}
	}
	return last, "", nil
}
