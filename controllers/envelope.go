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

// envelope.go — the envelope a relayed completion leaves in.
//
// Two paths relay an answer produced elsewhere (pipeToFamily, proxyToolRequest) and
// an answer arrives wearing whoever produced it: an id in someone else's shape,
// fields they invented, and the name of whichever sub-provider they picked this
// second. A customer bought inference from Hanzo. Who we bought it from is our side
// of the trade, it changes by the hour, and naming it in their response hands them
// nothing to act on and a dependency they never agreed to.
//
// So the envelope that leaves is ours: our id, the SKU they asked for, and the name
// of who SOLD them the inference. Every field our schema does not publish is
// dropped — an allowlist, so a sub-provider nobody here has heard of cannot leak by
// not being on a list of names to hide.
//
// The chain is not lost, only unpublished: the mark keeps what it took (mark.upstream)
// and the usage row records it as the origin of the call.

import (
	"bytes"
	"encoding/json"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// What our chat schema publishes: the fields go-openai's ChatCompletionResponse and
// ChatCompletionStreamResponse serialise, the fields of their choices, and of a
// choice's message or delta — plus `provider`, which is ours. `error` is published
// too; a refusal is an answer. A name absent from these sets does not go out.
var (
	chatFields = fields("id", "object", "created", "model", "provider", "choices",
		"usage", "error", "system_fingerprint", "prompt_annotations", "prompt_filter_results")
	choiceFields = fields("index", "message", "delta", "finish_reason", "logprobs",
		"content_filter_results")
	messageFields = fields("role", "content", "reasoning_content", "refusal", "name",
		"function_call", "tool_calls", "tool_call_id")
	// usageFields is what the CUSTOMER consumed. An aggregator hangs our side of
	// the trade on the same object — what we paid for the call (`cost`,
	// `cost_details.upstream_inference_cost`), whose account it ran on
	// (`is_byok`), and who we bought it from (`provider_name`) — and our buy
	// price is not a number a customer gets to read off their own receipt.
	usageFields = fields("prompt_tokens", "completion_tokens", "total_tokens",
		"prompt_tokens_details", "completion_tokens_details")
	// errorFields is what a refusal may say. A refusal IS an answer, so `error` is
	// published — but it was published whole, and an aggregator hangs the same
	// things on a refusal it hangs on a success: who it bought from
	// (error.metadata.provider_name) and the upstream's own raw body.
	errorFields = fields("message", "type", "param", "code")

	// The Anthropic dialect — /v1/messages, which is what Claude Code and every
	// agent built on that SDK speaks. Identity lives on the MESSAGE object: the
	// whole body when buffered, nested under `message` on a message_start event.
	bodyFields = fields("id", "type", "role", "model", "content",
		"stop_reason", "stop_sequence", "usage", "error")
	eventFields = fields("type", "index", "message", "content_block", "delta",
		"usage", "error")
	blockFields = fields("type", "text", "thinking", "signature", "citations",
		"id", "name", "input", "partial_json", "stop_reason", "stop_sequence")
	tokenFields = fields("input_tokens", "output_tokens",
		"cache_creation_input_tokens", "cache_read_input_tokens")

	// The embeddings dialect — an object list, no id of its own.
	listFields   = fields("object", "data", "model", "usage")
	vectorFields = fields("object", "index", "embedding")
)

// shape is the envelope a relay speaks. A mark is minted for exactly one, at the
// endpoint it belongs to, because the dialect is decided there and nowhere else.
type shape int

const (
	chatShape    shape = iota // OpenAI chat completion, buffered and chunked
	messageShape              // Anthropic /v1/messages and its SSE events
	listShape                 // OpenAI embeddings
)

func fields(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// A mark is what makes a relayed answer ours: the id we minted for this completion,
// the SKU the customer asked for, and who sold it to them. It carries back what it
// took — the upstream the envelope disclosed — so the chain still reaches the usage
// row once it stops reaching the customer. One mark per request, written and read on
// the single goroutine serving it.
type mark struct {
	id       string
	model    string
	seller   string
	upstream string
	// cost is what this call cost US to buy, in nano-USD, when the envelope
	// stated it. A pointer because exactness is PRESENCE, not positivity: a call
	// that cost exactly nothing — a free tier, a customer's own connected
	// account — knows its price as precisely as any other, and 0-as-unset would
	// send the ledger back to inventing one for a call that had a real one.
	cost *int64
	// speaks is the envelope shape this relay carries. Every relayed dialect has
	// one; a dialect with no mark is a dialect with no envelope, which is how the
	// Anthropic path and the embeddings path went on publishing `provider`,
	// `gen-` ids and `native_finish_reason` after the chat path stopped.
	speaks shape
}

// seller names who SOLD the inference. Our keys sell as Hanzo, whatever they route
// through underneath. A customer's own connected account sells as the vendor they
// connected: they hold that relationship and pay that vendor directly, so hiding it
// would be the same lie pointed the other way. It reads the SAME predicate metering
// bills on, so the answer and the invoice cannot disagree about whose account ran
// the call.
func seller(provider *object.Provider, user *iam.User) string {
	byo, _ := providerBYO(provider, user)
	switch {
	case !byo:
		return "hanzo"
	case provider.Type != "":
		return provider.Type
	case provider.Name != "":
		return provider.Name
	}
	return "hanzo"
}

// originOf answers who actually served a call: the upstream the envelope disclosed,
// when it disclosed one, else the host we dialled. It is the question the ledger's
// origin column already asks, and after the envelope it is the only place the specific
// answer survives — the customer stops reading it, metering keeps it.
//
// A nil mark is a path that relays no envelope (the provider-SDK failover loop builds
// its own answer), and a nil row is an attempt where nothing answered at all. Both
// read as the host, and a nil row has no host, which Origin already says as "".
func originOf(provider *object.Provider, m *mark) string {
	if m != nil && m.upstream != "" {
		return m.upstream
	}
	return provider.Origin()
}

// stamp rewrites one chat-completion envelope — a whole body, or one SSE data
// payload — as ours, and remembers what it removed. A payload that is not a JSON
// object (an SSE `[DONE]`, anything we did not produce) is returned unchanged, and a
// nil mark stamps nothing, which is how a dialect we relay verbatim opts out.
func (m *mark) stamp(payload []byte) []byte {
	if m == nil {
		return payload
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(payload, &env) != nil || env == nil {
		return payload
	}
	// Whatever the shape, an aggregator names its sub-provider the same way, and
	// we keep it the same way: read here, gone by the time this returns.
	if name, ok := name(env["provider"]); ok && m.upstream == "" {
		m.upstream = name
	}
	switch m.speaks {
	case messageShape:
		return or(m.stampMessage(env), payload)
	case listShape:
		return or(m.stampList(env), payload)
	}
	return or(m.stampChat(env), payload)
}

// or falls back to the payload as it arrived when an envelope could not be
// re-encoded. Relaying an upstream's own bytes beats inventing a shape for them.
func or(out []byte, payload []byte) []byte {
	if out == nil {
		return payload
	}
	return out
}

// stampMessage normalises the Anthropic dialect. The identity fields live on the
// MESSAGE object — the whole body when buffered, nested under `message` on a
// message_start event — and every other event carries deltas only.
func (m *mark) stampMessage(env map[string]json.RawMessage) []byte {
	if inner, ok := env["message"]; ok { // message_start
		var msg map[string]json.RawMessage
		if json.Unmarshal(inner, &msg) == nil && msg != nil {
			if body := m.body(msg); body != nil {
				keep(env, eventFields)
				env["message"] = body
				m.trim(env)
				return wire(env)
			}
		}
		return nil
	}
	if _, whole := env["content"]; whole { // the buffered answer
		return m.body(env)
	}
	keep(env, eventFields) // content_block_*, message_delta, ping, stops
	if block, ok := env["content_block"]; ok {
		env["content_block"] = prune(block, blockFields)
	}
	if delta, ok := env["delta"]; ok {
		env["delta"] = prune(delta, blockFields)
	}
	m.trim(env)
	return wire(env)
}

// body stamps one Anthropic message object: our id, the SKU asked for, and
// nothing the dialect does not publish. nil when it cannot be re-encoded.
func (m *mark) body(msg map[string]json.RawMessage) []byte {
	if name, ok := name(msg["provider"]); ok && m.upstream == "" {
		m.upstream = name
	}
	keep(msg, bodyFields)
	msg["id"] = text(m.id)
	msg["model"] = text(m.model)
	if content, ok := msg["content"]; ok {
		msg["content"] = stampBlocks(content)
	}
	m.trim(msg)
	return wire(msg)
}

// stampBlocks drops what the dialect does not publish from each content block —
// where an aggregator hangs its own name for reasoning text.
func stampBlocks(raw json.RawMessage) json.RawMessage {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return raw
	}
	for _, block := range blocks {
		keep(block, blockFields)
	}
	if out := wire(blocks); out != nil {
		return out
	}
	return raw
}

// stampList normalises the embeddings dialect: an object list with no id of its
// own, whose model used to be whatever the upstream called it.
func (m *mark) stampList(env map[string]json.RawMessage) []byte {
	keep(env, listFields)
	env["model"] = text(m.model)
	if data, ok := env["data"]; ok {
		env["data"] = prune(data, vectorFields)
		var vectors []map[string]json.RawMessage
		if json.Unmarshal(data, &vectors) == nil {
			for _, v := range vectors {
				keep(v, vectorFields)
			}
			if out := wire(vectors); out != nil {
				env["data"] = out
			}
		}
	}
	m.trim(env)
	return wire(env)
}

// trim takes the price and the refusal detail out of any envelope that carries
// them. Every dialect states both the same way, so every dialect loses them the
// same way — and the ledger gains the price at the same moment.
func (m *mark) trim(env map[string]json.RawMessage) {
	if usage, ok := env["usage"]; ok {
		m.take(usage)
		env["usage"] = prune(usage, m.tokens())
	}
	if refusal, ok := env["error"]; ok {
		env["error"] = stampError(refusal)
	}
}

// tokens is the token-count vocabulary of this dialect. The two shapes count the
// same things under different names, which is a fact about the dialects and not
// about what a customer may read.
func (m *mark) tokens() map[string]bool {
	if m.speaks == messageShape {
		return tokenFields
	}
	return usageFields
}

// wire re-encodes an envelope, or nil when it cannot be encoded.
func wire(v any) []byte {
	out, err := encode(v)
	if err != nil {
		return nil
	}
	return out
}

// stampChat normalises the OpenAI chat dialect, buffered and chunked.
func (m *mark) stampChat(env map[string]json.RawMessage) []byte {
	keep(env, chatFields)
	env["id"] = text(m.id)
	env["model"] = text(m.model)
	env["provider"] = text(m.seller)
	if choices, ok := env["choices"]; ok {
		env["choices"] = stampChoices(choices)
	}
	// Read before pruning, exactly as the upstream name is: this is the last place
	// the price we paid exists, and after this line it is out of the answer.
	m.trim(env)
	return wire(env)
}

// stampError keeps a refusal diagnostic and stops it carrying two things it must not.
//
// `metadata` is where an aggregator names the sub-provider and quotes the upstream's
// raw body, so it goes the way every other unpublished field goes. The message stays:
// a refusal a customer cannot read is not a kindness to anybody.
//
// A NUMERIC `code` goes too. In our schema code is a string an SDK switches on —
// "insufficient_quota", "context_length_exceeded". An upstream that writes 402 there
// is restating an HTTP status, and that status says the CUSTOMER owes money when what
// happened is that OUR account with a vendor is empty. It sends them to fix a bill
// that is not theirs, which is the same reason an exhausted request answers 503 and
// never the upstream's own status. A 200 carrying a refusal must not smuggle back
// what the status line already refused to say.
func stampError(raw json.RawMessage) json.RawMessage {
	var refusal map[string]json.RawMessage
	if json.Unmarshal(raw, &refusal) != nil || refusal == nil {
		return raw
	}
	keep(refusal, errorFields)
	if _, isText := name(refusal["code"]); !isText {
		delete(refusal, "code")
	}
	out, err := encode(refusal)
	if err != nil {
		return raw
	}
	return out
}

// take keeps the price the envelope stated for this call, so the ledger gains it as
// the customer loses it. It is the COGS the business could not see: the answer
// carried it all the way to the customer and nothing on our side ever read it.
//
// First statement wins. One completion has one price, and a stream restates it on
// the chunk that carries usage.
func (m *mark) take(usage json.RawMessage) {
	if m.cost != nil {
		return
	}
	var u struct {
		Cost    *float64 `json:"cost"`
		Details struct {
			Upstream *float64 `json:"upstream_inference_cost"`
		} `json:"cost_details"`
	}
	if json.Unmarshal(usage, &u) != nil {
		return
	}
	// `cost` is what WE were billed, which is our COGS. The upstream's own COGS is
	// the fallback: an aggregator that states only what it paid still tells us more
	// than nothing.
	usd := u.Cost
	if usd == nil {
		usd = u.Details.Upstream
	}
	if usd == nil {
		return
	}
	nano := usdToNano(*usd)
	m.cost = &nano
}

// cogs is what this call cost us to buy, or nil when nothing stated a price — in
// which case the ledger prices it the way it always has.
func (m *mark) cogs() *int64 {
	if m == nil {
		return nil
	}
	return m.cost
}

// stampChoices drops what our schema does not publish from each choice and from its
// message or delta — where an aggregator hangs its own finish reason and its own
// name for reasoning text. Choices it cannot read are left alone: relaying an
// upstream's malformed array beats inventing a shape for it.
func stampChoices(raw json.RawMessage) json.RawMessage {
	var choices []map[string]json.RawMessage
	if json.Unmarshal(raw, &choices) != nil {
		return raw
	}
	for _, choice := range choices {
		keep(choice, choiceFields)
		for _, part := range []string{"message", "delta"} {
			if body, ok := choice[part]; ok {
				choice[part] = prune(body, messageFields)
			}
		}
	}
	out, err := encode(choices)
	if err != nil {
		return raw
	}
	return out
}

// prune is keep over an encoded object: the object with every unpublished field
// dropped, or the input unchanged when it is not an object.
func prune(raw json.RawMessage, published map[string]bool) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return raw
	}
	keep(obj, published)
	out, err := encode(obj)
	if err != nil {
		return raw
	}
	return out
}

// keep drops every field an object carries that our schema does not publish.
func keep(obj map[string]json.RawMessage, published map[string]bool) {
	for name := range obj {
		if !published[name] {
			delete(obj, name)
		}
	}
}

// name reads a JSON string value. Absent, null, or any other type reads as no name.
func name(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || s == "" {
		return "", false
	}
	return s, true
}

// text is a string as a JSON value. A string always encodes.
func text(s string) json.RawMessage {
	out, _ := json.Marshal(s)
	return out
}

// encode marshals without HTML escaping, so relayed content survives the round trip
// as it was written — `<`, `>` and `&` are ordinary characters in a completion, and
// the encoder's default would rewrite every one of them.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
