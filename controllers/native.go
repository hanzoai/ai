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

// native.go — a model asked of the vendor that makes it, before a reseller of it.
//
// A resale family lists other vendors' models. Where the platform holds accounts
// with the vendor that MAKES one, that vendor's own API is asked first, on every
// key of its pool, and the reseller only once none of them answered. The SKU, its
// price to the caller and every gate in front of it are the family's, unchanged:
// this is one more place the same request may be sent, never a second catalog.
//
// Three rules hold it, and dispatch (zen_client.go) is where each is applied:
//
//	paid only   a maker's API bills every call, so it is asked only for a route the
//	            family charges for, by a request seated on the paid lane. A free
//	            route, the free pool and every request off the paid lane never reach it.
//	every key   an account that cannot serve hands the SAME request to the next:
//	            401, 403  the key is refused        → it sits out nativeDead
//	            no credit the account cannot pay    → it sits out nativeDead
//	            429       the account is limited    → it sits out until the vendor's
//	                                                  Retry-After, never past nativeBusy
//	            404, 408, 5xx, 529                  → the next key, nothing sits out
//	            anything else is the request's own answer, which every key would
//	            give: no other key is asked, and the family's own address is.
//	as itself   what is sent is the vendor's own id for the model asked. Nothing
//	            here chooses another model; when no key answers, the request goes
//	            on to the family that lists it, and that family's refusal stands.
//
// What each key may be asked for is what the vendor lists for it (GET /v1/models,
// read at start and every nativeEvery): a SKU is the maker's when its id, with its
// dots written as dashes, is an id that key lists, or one with a release date
// after it. A key whose list was never read is not asked, so a maker that cannot
// be reached costs a request nothing.
//
// An answer says where it came from the way a family service does (servedHeader's
// siblings): the model as the vendor names it, the vendor, the NAME of the key it
// ran on, each key that refused first, and what the call cost at the vendor's own
// price — a header on a whole answer, a trailer on a stream. Never a key.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/upstream"
)

const (
	// nativeEvery is how often each key's list of models is read from its vendor.
	nativeEvery = 5 * time.Minute
	// nativeDead is how long a key sits out once its vendor refused it as a
	// credential or for want of credit. Neither mends itself, so the wait is long;
	// a read of the key's list that succeeds ends it at once.
	nativeDead = 10 * time.Minute
	// nativeBusy is the longest a rate-limited key sits out, whatever its vendor says,
	// and nativePause how long it sits out when the vendor does not say.
	nativeBusy  = time.Minute
	nativePause = 5 * time.Second
	// nativeHead is how long the vendor may take to begin a STREAM. A stream's
	// head arrives before its answer, so a vendor silent this long is not serving,
	// and the request still has the reseller's whole wait ahead of it. A whole
	// answer's head arrives with the answer and keeps the pipe's own wait.
	nativeHead = 30 * time.Second
)

// nativeStreams sends the requests whose answer is a stream.
var nativeStreams = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: nativeHead,
}}

// native is a model vendor's own API, as a resale family asks it.
type native struct {
	name   string   // the vendor, as rows and spans name it ("anthropic")
	prefix string   // the family's SKUs it makes ("anthropic/")
	urlKey string   // config key for its address; unset, the maker is never asked
	names  []string // its keys by name, in the order a request tries them
	list   string   // one more name, holding further keys, one a line
	// chat says the vendor speaks OpenAI's Chat Completions, which a chat request
	// already is: it is sent the caller's body under the vendor's id for the model.
	// Without it the vendor speaks Anthropic's Messages, which a chat is translated
	// into.
	chat bool

	mu sync.RWMutex
	// lists is what each key may be asked for, by keyID: the vendor's own list,
	// each model under the id a SKU is spelled as.
	lists map[string]map[string]claude
	// said is each key's last standing as it was logged, by keyID, so a standing
	// is logged when it changes and not on every read.
	said map[string]string
}

// anthropicNative is Anthropic's own API, asked before OpenRouter for the Claude
// models OpenRouter lists.
var anthropicNative = &native{
	name:   "anthropic",
	prefix: "anthropic/",
	urlKey: "ANTHROPIC_URL",
	names:  object.AnthropicKeys,
	list:   object.AnthropicKeyList,
}

// openaiNative is OpenAI's own API, asked before OpenRouter for the OpenAI models
// OpenRouter lists: the same model at its maker's price, without the reseller's fee.
var openaiNative = &native{
	name:   "openai",
	prefix: "openai/",
	urlKey: "OPENAI_URL",
	names:  object.OpenAIKeys,
	list:   object.OpenAIKeyList,
	chat:   true,
}

// maker is the vendor among f's makers that makes sku (its id has the vendor's
// prefix and names a model of the vendor's), nil when none does.
func (f *modelFamily) maker(sku string) *native {
	for _, n := range f.makers {
		if _, ok := n.spelled(sku); ok {
			return n
		}
	}
	return nil
}

// base is the vendor's address, or "" when the deployment names none.
func (n *native) base() string {
	return strings.TrimRight(strings.TrimSpace(conf.GetConfigString(n.urlKey)), "/")
}

// accounts are the keys a request tries, in order.
func (n *native) accounts() []object.Account { return object.Accounts(n.names, n.list) }

// label names one key of the pool without the key: the vendor and the key's place.
func (n *native) label(place int) string { return n.name + "#" + strconv.Itoa(place) }

// place is the place in the pool of the key declared as name, or 0.
func (n *native) place(name string) int {
	for i, declared := range n.names {
		if declared == name {
			return i + 1
		}
	}
	if line, ok := strings.CutPrefix(name, n.list+"#"); ok && n.list != "" {
		if k, err := strconv.Atoi(line); err == nil && k > 0 {
			return len(n.names) + k
		}
	}
	return 0
}

// origin is where an answer on the key declared as name came from: the vendor's
// host and the key's place in the pool.
func (n *native) origin(name string) string {
	host := n.base()
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	if p := n.place(name); p > 0 {
		return host + "#" + strconv.Itoa(p)
	}
	return host
}

// reached is where an answer came from, and the vendor account it ran on where
// its family's maker served it: the maker's host and the place in its pool of the
// key that answered, in place of the family's own address.
//
// Whether a maker served it is the pipe's own knowledge (serving.maker), never a
// header's: a reseller's address answering with a maker's headers stays the
// reseller.
func reached(prov *object.Provider, mk *mark, sv serving) (origin, via string) {
	if n := sv.maker; n != nil {
		return n.origin(sv.account), n.label(n.place(sv.account))
	}
	return originOf(prov, mk), ""
}

// dated is a model id that ends in its release date.
var dated = regexp.MustCompile(`^(.+)-(\d{8})$`)

// spelled is the id a SKU names the vendor's model by: the SKU without the
// family's prefix, its dots written as dashes where the vendor spells versions so
// (Anthropic: claude-haiku-5-5); OpenAI keeps them (gpt-5.4-mini). A SKU with a
// variant after a colon is the reseller's own and names no model of the vendor's.
func (n *native) spelled(sku string) (string, bool) {
	id, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(sku)), n.prefix)
	if !ok || id == "" || strings.Contains(id, ":") {
		return "", false
	}
	if n.chat {
		return id, true
	}
	return strings.ReplaceAll(id, ".", "-"), true
}

// find is the model the key a lists for sku.
func (n *native) find(a object.Account, sku string) (claude, bool) {
	id, ok := n.spelled(sku)
	if !ok {
		return claude{}, false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	m, ok := n.lists[keyID(a.Key)][id]
	return m, ok
}

// serves reports whether any key of the pool lists sku.
func (n *native) serves(sku string) bool {
	if n.base() == "" {
		return false
	}
	for _, a := range n.accounts() {
		if _, ok := n.find(a, sku); ok {
			return true
		}
	}
	return false
}

// forget drops every list read and every standing logged. Tests start from it.
func (n *native) forget() {
	n.mu.Lock()
	n.lists, n.said = nil, nil
	n.mu.Unlock()
}

// watch reads every key's list now and every nativeEvery after, for the life of
// the process.
func (n *native) watch() {
	for {
		n.refresh()
		time.Sleep(nativeEvery)
	}
}

// refresh reads each key's list of models from the vendor. A key the vendor
// refuses sits out nativeDead; one it answers is back at once. A key that could
// not be read keeps the list it had.
func (n *native) refresh() {
	base := n.base()
	if base == "" {
		return
	}
	var wg sync.WaitGroup
	for _, a := range n.accounts() {
		wg.Add(1)
		go func(a object.Account) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), listWait)
			defer cancel()
			models, status, err := n.read(ctx, base, a)
			switch {
			case err != nil:
				n.say(a, "unread", "%s key %s: list unread: %v", n.name, a.Name, err)
			case status == http.StatusUnauthorized:
				n.rest(a, status, "the vendor refuses this key", scopeAll)
			case status == http.StatusForbidden:
				n.rest(a, status, "the vendor refuses this key", scopePriced)
			case status != http.StatusOK:
				n.say(a, "unread", "%s key %s: list unread: status %d", n.name, a.Name, status)
			default:
				n.mu.Lock()
				if n.lists == nil {
					n.lists = map[string]map[string]claude{}
				}
				n.lists[keyID(a.Key)] = models
				n.mu.Unlock()
				// A list that reads says the key is a key the vendor knows: it ends the
				// wait of one refused as a credential (401). It says nothing of what
				// the key may do or its account can pay, so a key refused permission
				// (403) or resting for want of credit rests its time out.
				revive(a.Key)
				if !cooling(a.Key, false) {
					n.say(a, "serving", "%s key %s: serving, %d models listed", n.name, a.Name, len(models))
				}
			}
		}(a)
	}
	wg.Wait()
}

// say logs a key's standing when it is not the one last logged for it.
func (n *native) say(a object.Account, standing, format string, v ...any) bool {
	id := keyID(a.Key)
	n.mu.Lock()
	if n.said == nil {
		n.said = map[string]string{}
	}
	same := n.said[id] == standing
	n.said[id] = standing
	n.mu.Unlock()
	if !same {
		log.Warn(format, v...)
	}
	return !same
}

// rest benches a key the vendor refused for itself for nativeDead, and says so
// once. scope is what ends the wait: a key refused as a credential (401, scopeAll)
// is back as soon as its list reads again; one refused permission (403) or out of
// credit (scopePriced) only when its time is up, since a list that reads says
// nothing of either. Each needs a person, so each is a line in the log and a
// count in cloud_supply_refused, when the key's standing changes and not on every
// request that would have used it.
func (n *native) rest(a object.Account, status int, why string, scope keyScope) {
	until := keyNow().Add(nativeDead)
	coolUntil(a.Key, scope, until)
	reason := reasonCredential
	if status == http.StatusPaymentRequired {
		reason = reasonFunding
	}
	if n.say(a, "resting "+strconv.Itoa(status), "%s key %s is not serving (%d: %s): it sits out until %s, and the next key, then the reseller, answer for it. Replace or fund it at KMS hanzo/prod:/ai.",
		n.name, a.Name, status, why, until.UTC().Format(time.RFC3339)) {
		supplyRefused.WithLabelValues(n.name, reason).Inc()
	}
	if status == http.StatusPaymentRequired {
		emptied(n.name, a.Name, "", status)
	}
}

// revive ends the wait of a key refused as a credential.
func revive(key string) {
	keyCoolMu.Lock()
	delete(keyCooled, keyCooling{keyID(key), scopeAll})
	keyCoolMu.Unlock()
}

// claude is one model as Anthropic's own API lists it: the id it is asked for by,
// the most one answer may hold, and what its listing says a request may ask of
// it. A model whose listing states no capabilities is asked for nothing it could
// refuse: no thinking, no effort, no sampling parameter.
type claude struct {
	ID string
	// Out is the most one answer may hold (max_tokens), 0 where unstated.
	Out int
	// Adaptive and Budget are the two ways it is asked to think: adaptive
	// thinking, and thinking within a stated budget of tokens.
	Adaptive, Budget bool
	// Efforts are the effort levels it takes.
	Efforts map[string]bool
	// Vision is whether it reads images, and Schema whether an answer can be held
	// to a JSON schema.
	Vision, Schema bool
}

// efforts are the effort levels a model may list, least first.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// supported reads one capability from a model's listing: the `supported` at the
// end of path.
func supported(caps map[string]json.RawMessage, path ...string) bool {
	for i, key := range path {
		raw, ok := caps[key]
		if !ok {
			return false
		}
		var next map[string]json.RawMessage
		if json.Unmarshal(raw, &next) != nil {
			return false
		}
		if i == len(path)-1 {
			var yes bool
			return json.Unmarshal(next["supported"], &yes) == nil && yes
		}
		caps = next
	}
	return false
}

// read is one key's list of models: every page of GET /v1/models, each model
// under its own id and, for an id that ends in its release date, under the id
// without it — the newest release where the vendor lists several.
func (n *native) read(ctx context.Context, base string, a object.Account) (map[string]claude, int, error) {
	out := map[string]claude{}
	// exact holds the ids the vendor lists without a date, and released the date of
	// the release filed under each id that has none of its own.
	exact, released := map[string]bool{}, map[string]string{}
	after := ""
	for page := 0; page < 20; page++ {
		target := base + "/v1/models"
		if !n.chat {
			target += "?limit=1000"
			if after != "" {
				target += "&after_id=" + url.QueryEscape(after)
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, 0, err
		}
		n.sign(req, a, "", nil)
		resp, err := zenDiscoveryClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return nil, 0, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, resp.StatusCode, nil
		}
		var list struct {
			Data []struct {
				ID           string                     `json:"id"`
				Out          int                        `json:"max_tokens"`
				Capabilities map[string]json.RawMessage `json:"capabilities"`
			} `json:"data"`
			More bool   `json:"has_more"`
			Last string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, 0, err
		}
		for _, w := range list.Data {
			id := strings.ToLower(strings.TrimSpace(w.ID))
			if id == "" {
				continue
			}
			m := claude{
				ID:       w.ID,
				Out:      w.Out,
				Adaptive: supported(w.Capabilities, "thinking", "types", "adaptive"),
				Budget:   supported(w.Capabilities, "thinking", "types", "enabled"),
				Vision:   supported(w.Capabilities, "image_input"),
				Schema:   supported(w.Capabilities, "structured_outputs"),
				Efforts:  map[string]bool{},
			}
			for _, e := range efforts {
				if supported(w.Capabilities, "effort", e) {
					m.Efforts[e] = true
				}
			}
			out[id] = m
			parts := dated.FindStringSubmatch(id)
			if parts == nil {
				exact[id] = true
				continue
			}
			// An id that ends in its release date also answers for the id without
			// it, unless the vendor lists that id itself: the newest release.
			if name, date := parts[1], parts[2]; !exact[name] && date > released[name] {
				out[name], released[name] = m, date
			}
		}
		if !list.More || list.Last == "" {
			break
		}
		after = list.Last
	}
	return out, http.StatusOK, nil
}

// sign puts the vendor's headers on a request: the API version (the caller's
// where one was stated), each beta the caller turned on, and the key.
func (n *native) sign(r *http.Request, a object.Account, version string, betas []string) {
	if n.chat {
		upstream.Authorize(r, &object.Provider{Type: "OpenAI", ClientSecret: a.Key})
		return
	}
	if version == "" {
		version = anthropicVersion
	}
	r.Header.Set("anthropic-version", version)
	for _, b := range betas {
		r.Header.Add("anthropic-beta", b)
	}
	upstream.Authorize(r, &object.Provider{Type: "Anthropic", ClientSecret: a.Key})
}

// nativeCall is one request as dispatch hands it to a maker.
type nativeCall struct {
	path string // "chat/completions" or "messages": the dialect the body speaks
	sku  string // the model asked for, as the family's vendor spells it
	body []byte // the body the family would be sent
	// stream says the answer is asked for as a stream; a chat is always asked so.
	stream bool
	// version and betas are the caller's anthropic-version and anthropic-beta, for
	// a body that already speaks the vendor's dialect.
	version string
	betas   []string
	// price is the SKU as the family lists it: what the answer cost is stated from
	// its rates, less the family's markup.
	price zenModel
	start time.Time
}

// errUntranslated says a request holds something the vendor's own API has no word
// for, or does not serve on a shared account. It is not asked there, and the
// family's own address is, exactly as before.
var errUntranslated = errors.New("the request is not one the vendor's own API is asked")

// send asks the vendor for c on each key in turn and returns the first answer, or
// nil and each key that refused. An answer carries where it came from in its
// headers (and, for a stream, its trailer).
func (n *native) send(ctx context.Context, c nativeCall) (*http.Response, []miss) {
	base := n.base()
	if base == "" {
		return nil, nil
	}
	var missed []miss
	for _, a := range n.accounts() {
		if ctx.Err() != nil || time.Since(c.start) > zenPipeHeader {
			break
		}
		m, ok := n.find(a, c.sku)
		if !ok || cooling(a.Key, false) {
			continue
		}
		body, hide, err := n.shape(c, m)
		if err != nil {
			// Not the maker's to answer on any key.
			return nil, missed
		}
		path := "/v1/messages"
		if n.chat {
			path = "/v1/chat/completions"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
		if err != nil {
			return nil, missed
		}
		req.Header.Set("Content-Type", "application/json")
		n.sign(req, a, c.version, n.betas(c))
		client := zenPipeClient
		if c.stream {
			client = nativeStreams
		}
		resp, err := client.Do(req)
		if err != nil {
			// The vendor was not reached, which no other key changes.
			log.Warn("%s could not be reached for %s on key %s: %v", n.name, c.sku, a.Name, err)
			missed = append(missed, miss{upstream: m.ID, provider: n.name, reason: fmt.Sprintf("key %d: unreachable", a.Place)})
			break
		}
		if c.stream {
			resp = opening(resp)
			// A stream was asked for and is what the pipe reads. An answer in any other
			// shape is not relayed as one: the family's own address answers instead.
			if resp.StatusCode == http.StatusOK && !eventStream(resp) {
				resp.Body.Close()
				log.Warn("%s answered %s with no stream on key %s: the reseller is asked", n.name, c.sku, a.Name)
				return nil, append(missed, miss{upstream: m.ID, provider: n.name, reason: fmt.Sprintf("key %d: not a stream", a.Place)})
			}
		}
		if resp.StatusCode == http.StatusOK {
			return n.answer(resp, c, m, a, hide, missed), nil
		}
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if c.stream && string(said) == emptyAnswer {
			// The model was asked and ended its answer having said nothing (opening).
			// That is the request's own answer: every key would buy the same
			// silence, so none is asked, and the reseller answers for it.
			return nil, append(missed, miss{upstream: m.ID, provider: n.name, reason: fmt.Sprintf("key %d: empty answer", a.Place)})
		}
		status, kind, why := claudeRefusal(resp.StatusCode, said)
		broke := unfunded(status, kind, why)
		if n.chat {
			status, kind, broke = chatRefusal(resp.StatusCode, said)
		}
		missed = append(missed, miss{upstream: m.ID, provider: n.name, reason: fmt.Sprintf("key %d: %d %s", a.Place, status, kind)})
		switch {
		case status == http.StatusUnauthorized:
			n.rest(a, status, kind, scopeAll)
		case status == http.StatusForbidden:
			n.rest(a, status, kind, scopePriced)
		case broke:
			n.rest(a, http.StatusPaymentRequired, "the account is out of credit", scopePriced)
		case status == http.StatusTooManyRequests:
			wait := retryAfter(resp.Header)
			if wait <= 0 {
				wait = nativePause
			}
			coolUntil(a.Key, scopePriced, keyNow().Add(min(wait, nativeBusy)))
			log.Warn("%s key %s is rate limited for %s (429): the next key is asked", n.name, a.Name, c.sku)
		case status == http.StatusNotFound || status == http.StatusRequestTimeout || status >= 500:
			log.Warn("%s key %s did not serve %s (%d %s): the next key is asked", n.name, a.Name, c.sku, status, kind)
		default:
			// The request's own answer: every key would give it, so none is asked,
			// and the family that lists the model answers for it.
			return nil, missed
		}
	}
	return nil, missed
}

// betas are the betas a body in the vendor's own dialect is sent with: each the
// caller turned on that the shared account serves. The rest stay behind, as every
// one does on the family's own path.
func (n *native) betas(c nativeCall) []string {
	var out []string
	for _, line := range c.betas {
		for b := range strings.SplitSeq(line, ",") {
			if b = strings.TrimSpace(b); b != "" && sharedBetas[b] {
				out = append(out, b)
			}
		}
	}
	return out
}

// shape is the body the vendor is sent for c, addressed to m: a chat translated
// to the vendor's dialect, or a body that already speaks it, asked to cache its
// prompt like any other the platform sends a Claude model. hide says the caller
// asked for the model's reasoning not to be written out.
func (n *native) shape(c nativeCall, m claude) (body []byte, hide bool, err error) {
	if n.chat {
		return chatBody(c, m)
	}
	if c.path == "chat/completions" {
		return chatAsMessages(c.body, m)
	}
	if c.path != "messages" {
		return nil, false, errUntranslated
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(c.body, &in) != nil || in == nil {
		return nil, false, errUntranslated
	}
	in["model"], _ = json.Marshal(m.ID)
	if !bytes.Contains(c.body, []byte(`"cache_control"`)) {
		in["cache_control"] = ephemeral
	}
	if body, err = json.Marshal(in); err != nil {
		return nil, false, errUntranslated
	}
	// The shared account serves what is priced in tokens and nothing it would have
	// to keep (anthropic_api.go shared): the rest is the reseller's to answer.
	if shared(body, n.betas(c)) != nil {
		return nil, false, errUntranslated
	}
	return body, false, nil
}

// claudeStatus is the status each of the vendor's error types is answered with,
// for a refusal that arrived inside a stream and so carries none of its own.
var claudeStatus = map[string]int{
	"invalid_request_error": http.StatusBadRequest,
	"authentication_error":  http.StatusUnauthorized,
	"billing_error":         http.StatusPaymentRequired,
	"permission_error":      http.StatusForbidden,
	"not_found_error":       http.StatusNotFound,
	"request_too_large":     http.StatusRequestEntityTooLarge,
	"rate_limit_error":      http.StatusTooManyRequests,
	"api_error":             http.StatusInternalServerError,
	"timeout_error":         http.StatusGatewayTimeout,
	"overloaded_error":      529,
}

// claudeRefusal reads the vendor's refusal: its status, its error type and its message.
// A refusal that opened a stream was given 502 for want of a status of its own
// (opening), and is read by its type.
func claudeRefusal(status int, body []byte) (int, string, string) {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if st, ok := claudeStatus[e.Error.Type]; ok && status == http.StatusBadGateway {
		status = st
	}
	kind := e.Error.Type
	if kind == "" {
		kind = "error"
	}
	return status, kind, e.Error.Message
}

// unfunded reports the refusal of an account that cannot pay: a 402, the vendor's
// billing_error, or the 400 it answers an account out of credit or at its spending
// limit with, read by the vendor's own sentence FROM ITS START. A message that merely contains the words
// is a validation error quoting the caller's request back, and reading it as the
// account's would let one request bench every key of the pool.
func unfunded(status int, kind, message string) bool {
	if status == http.StatusPaymentRequired || kind == "billing_error" {
		return true
	}
	if status != http.StatusBadRequest {
		return false
	}
	said := strings.ToLower(strings.TrimSpace(message))
	// Out of credit, and at the spending limit its owner set for it.
	return strings.HasPrefix(said, "your credit balance is too low") ||
		strings.HasPrefix(said, "you have reached your specified api usage limits")
}

// retryAfter is the wait a refusal states in seconds, or 0.
func retryAfter(h http.Header) time.Duration {
	if s, err := strconv.ParseFloat(strings.TrimSpace(h.Get("Retry-After")), 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	return 0
}

// chain writes arms a request could not use as a family states them
// (failoverHeader), so arms reads them back.
func chain(missed []miss) string {
	parts := make([]string, 0, len(missed))
	for _, m := range missed {
		parts = append(parts, fmt.Sprintf("%s (%s): %s", m.upstream, m.provider, m.reason))
	}
	return strings.Join(parts, "; ")
}

// answer is the vendor's 200 as the pipe reads a family's: the body in the
// dialect the caller's request spoke, and where it came from in its headers. What
// it cost is stated once its usage is known — at once for a whole answer, in the
// trailer for a stream. It carries none of the vendor's own headers and not the
// request it answers, which holds the key.
func (n *native) answer(resp *http.Response, c nativeCall, m claude, a object.Account, hide bool, missed []miss) *http.Response {
	h := http.Header{}
	h.Set(armHeader, m.ID)
	h.Set(providerHeader, n.name)
	h.Set(keyHeader, a.Name)
	if len(missed) > 0 {
		h.Set(failoverHeader, chain(missed))
	}
	out := &http.Response{
		Status:     resp.Status,
		StatusCode: http.StatusOK,
		Proto:      resp.Proto,
		ProtoMajor: resp.ProtoMajor,
		ProtoMinor: resp.ProtoMinor,
		Header:     h,
		Trailer:    http.Header{},
	}
	// stated files what the answer cost. A stream cut before its count is priced on
	// what it reported and the text it wrote, at four characters a token.
	stated := func(used AnthropicUsage, final bool, wrote int) {
		if !final && wrote > 0 {
			used.OutputTokens = max(used.OutputTokens, wrote/4+1)
		}
		if usd, ok := c.cost(used); ok {
			out.Trailer.Set(cogsHeader, usd)
		}
	}
	switch {
	case n.chat:
		// The vendor's own stream, as the family's would arrive: the pipe puts our
		// envelope on it. What it cost is read from its usage chunk as it passes.
		h.Set("Content-Type", "text/event-stream")
		out.Body = &chatTap{ReadCloser: resp.Body, end: func(t tokens) {
			if usd, ok := c.costOf(t); ok {
				out.Trailer.Set(cogsHeader, usd)
			}
		}}
	case c.path == "chat/completions":
		h.Set("Content-Type", "text/event-stream")
		out.Body = newClaudeChat(resp.Body, c.sku, hide, stated)
	case c.stream:
		h.Set("Content-Type", "text/event-stream")
		out.Body = &tap{ReadCloser: resp.Body, end: stated}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		h.Set("Content-Type", "application/json")
		if used, ok := answerUsage(body); ok {
			if usd, ok := c.cost(used); ok {
				h.Set(cogsHeader, usd)
			}
		}
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.ContentLength = int64(len(body))
	}
	return out
}

// cost is what an answer cost at the vendor's own price, in USD: its tokens at
// the rates the family lists the SKU at, less the family's markup, an hour-long
// cache write counted as the vendor prices it (AnthropicUsage.billed). false for
// a SKU the family states no price for.
func (c nativeCall) cost(used AnthropicUsage) (string, bool) {
	t := used.billed(c.sku)
	return c.costOf(tokens{fresh: t.in, cached: t.read, written: t.write, completion: t.out})
}

// costOf is what tokens cost at the vendor's own price, in USD: the rates the
// family lists the SKU at, less the family's markup. false for a SKU the family
// states no price for.
func (c nativeCall) costOf(t tokens) (string, bool) {
	if !c.price.priced() {
		return "", false
	}
	usd := c.price.retailUSD(t)
	if c.price.Margin.Sign() > 0 {
		usd = usd.Quo(c.price.Margin, 18)
	}
	return usd.String(), true
}

// tap relays a stream in the vendor's own dialect as it came and reads its usage
// as it passes, so what the answer cost is stated when it ends: at its last byte,
// or when its reader hangs up on it. The vendor bills what it wrote either way.
type tap struct {
	io.ReadCloser
	end func(used AnthropicUsage, final bool, wrote int)

	// mu holds what a Read learned against a Close from another goroutine.
	mu    sync.Mutex
	line  []byte
	used  AnthropicUsage
	final bool
	wrote int
	ended bool
}

func (t *tap) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range p[:n] {
		if b != '\n' {
			// A line past sseLineMax is a document inside a tool result: relayed,
			// never parsed.
			if len(t.line) <= sseLineMax {
				t.line = append(t.line, b)
			}
			continue
		}
		if len(t.line) <= sseLineMax {
			var ev messagesEvent
			if ev.read(strings.TrimSuffix(string(t.line), "\r"), &t.used, &t.final) {
				t.wrote += len(ev.Delta.Text) + len(ev.Delta.Thinking) + len(ev.Delta.PartialJSON)
			}
		}
		t.line = t.line[:0]
	}
	if err != nil {
		t.finish()
	}
	return n, err
}

// Close hangs up on the vendor and, for a stream that had not ended, says what it
// used as far as it got.
func (t *tap) Close() error {
	err := t.ReadCloser.Close()
	t.mu.Lock()
	t.finish()
	t.mu.Unlock()
	return err
}

// finish says once what the stream used. mu is held.
func (t *tap) finish() {
	if !t.ended {
		t.ended = true
		t.end(t.used, t.final, t.wrote)
	}
}
