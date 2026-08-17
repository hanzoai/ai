// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openai "github.com/hanzoai/go-openai"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/web"
)

// apiErr builds the typed error a provider HTTP failure arrives as, so these
// tests exercise the same path production does rather than a string that merely
// looks like one.
func apiErr(status int, msg string) error {
	return &openai.APIError{HTTPStatusCode: status, Message: msg}
}

// The taxonomy IS the design. Everything else — whether to cascade, whether to
// demote, what the client is told — reads off this one function, so it gets the
// exhaustive table.
func TestFaultOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want fault
		why  string
	}{
		// ── the outage: a vendor whose account is empty ──────────────────
		{"402 typed", apiErr(402, "Insufficient credits."), faultProvider,
			"the measured outage: enso-flash, 402, whole request died"},
		{"402 untyped text", errors.New(`status code 402, Payment Required, "Insufficient credits."`), faultProvider,
			"the same refusal from a client that stringifies its errors"},
		{"insufficient_quota", errors.New("You exceeded your current quota, code: insufficient_quota"), faultProvider,
			"OpenAI's spelling of the same thing"},
		{"insufficient balance", errors.New("Insufficient Balance"), faultProvider, "DeepSeek's spelling"},
		{"billing hard limit", errors.New("Billing hard limit has been reached"), faultProvider, "OpenAI cap"},

		// ── busy / broken vendors ────────────────────────────────────────
		{"429 typed", apiErr(429, "Platform overloaded"), faultProvider, "ask again shortly"},
		{"500 typed", apiErr(500, "internal"), faultProvider, "vendor broke"},
		{"502 typed", apiErr(502, "bad gateway"), faultProvider, "vendor broke"},
		{"503 typed", apiErr(503, "unavailable"), faultProvider, "vendor broke"},
		{"504 typed", apiErr(504, "gateway timeout"), faultProvider, "vendor broke"},
		{"408 typed", apiErr(408, "request timeout"), faultProvider, "vendor gave up on itself"},
		{"599 typed", apiErr(599, "who knows"), faultProvider, "any 5xx is the vendor's end"},
		{"connection refused", errors.New("dial tcp 10.0.0.1:443: connection refused"), faultProvider, "never arrived"},
		{"connection reset", errors.New("read tcp: connection reset by peer"), faultProvider, "never completed"},
		{"no such host", errors.New("lookup api.example.com: no such host"), faultProvider, "never resolved"},
		{"deadline exceeded", errors.New("context deadline exceeded"), faultProvider, "never answered"},
		{"unexpected EOF", errors.New("unexpected EOF"), faultProvider, "closed mid-response"},
		{"provider disabled", unavailable("do-ai"), faultProvider,
			"an admin switched it off; the alternates must still be honoured. The VALUE the loop " +
				"raises, not a string resembling it — it carries a 503 so nothing has to read our own prose"},

		// ── a substring of an unrelated token must not classify ──────────
		// Every one of these was measured against the old substring matcher.
		{"a date in a model id is not a payment code", errors.New("model claude-3-sonnet-20240229 not found"), faultRequest,
			`"402" lies inside "2024_022_9", so a typo'd model id read as an empty account: three ` +
				"vendors times three retries is nine upstream calls, plus a five-minute demotion of a healthy vendor"},
		{"a token count is not a status", errors.New("you requested 8503 tokens, which exceeds the limit"), faultRequest,
			`"503" lies inside "8503" — a request-size fact read as a broken vendor`},
		{"a longer number that STARTS with a status is not that status", errors.New("request 402913 was rejected"), faultRequest,
			`"402" begins a token here, so only checking the LEFT edge still reads an id as an empty account. ` +
				"A status code is exact at both ends"},
		{"geofence is not an eof", errors.New("request blocked by geofence policy"), faultRequest,
			`"eof" lies inside "geofence", which would read any policy refusal as a dropped connection`},
		{"a tier refusal is not our own switched-off row", errors.New("this model is unavailable for your account tier"), faultRequest,
			`"unavailable" was in the transport list only because our OWN disabled-row error used the word. ` +
				"If a vendor is observed using this phrasing for a catalogue gate it belongs in absentText — " +
				"a deliberate data change, not an accident of overlap"},

		// ── the vendor does not stock the item ───────────────────────────
		{"403 agreement gate typed", apiErr(403, "this model is not available for your account"), faultProvider,
			"DO's agreement gate — the do-ai to anthropic fallback exists for exactly this"},
		{"403 agreement gate untyped", errors.New("error, status code: 403, message: this model is not available for your account"), faultProvider,
			"same refusal, no status to read"},
		{"no access to model", errors.New("your account does not have access to model xyz"), faultProvider, "same fact, other wording"},

		// ── OUR mistake: must NOT cascade ────────────────────────────────
		{"401 typed", apiErr(401, "invalid api key"), faultRequest,
			"our credential is dead; cascading hides it and we pay a second vendor for the first one's broken key"},
		{"401 untyped", errors.New("HTTP 401 Unauthorized"), faultRequest, "same, no status"},
		{"403 plain", apiErr(403, "you do not have permission to use this key"), faultRequest,
			"not a catalogue fact — a credential fact"},
		{"403 insufficient permissions", apiErr(403, "insufficient permissions for this operation"), faultRequest,
			"the exact trap a bare `insufficient` substring would turn into a five-vendor spend"},
		{"400 bad request", apiErr(400, "invalid 'messages': empty"), faultRequest, "malformed everywhere"},
		{"404 unknown model", apiErr(404, "model not found: gpt-99"), faultRequest,
			"a bad route entry we need to SEE, not paper over"},
		{"413 too large", apiErr(413, "payload too large"), faultRequest, "the request is the problem"},
		{"422 content filtered", apiErr(422, "content was filtered"), faultRequest, "refused everywhere"},
		{"409 unknown 4xx", apiErr(409, "conflict"), faultRequest,
			"unrecognised 4xx fails closed: we do not spend at three vendors to learn what it was"},
		{"context length exceeded", errors.New("This model's maximum context length is 8192 tokens"), faultRequest,
			"a request-size fact; every vendor refuses it identically"},
		{"unrecognised", errors.New("something went sideways"), faultRequest,
			"the default is stop, not spend"},
		{"partially written", errPartiallyWritten, faultRequest,
			"bytes are on the wire; nothing can be moved now"},
		{"nil", nil, faultRequest, "no failure, nothing to attribute"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := faultOf(tc.err); got != tc.want {
				t.Errorf("faultOf(%v) = %s, want %s\n  because: %s", tc.err, got, tc.want, tc.why)
			}
		})
	}
}

// The status must survive being wrapped. wrapUpstreamError restates an SDK error
// as *apiError, and if the reader only knew one of the two spellings the
// taxonomy would silently degrade to substring guessing after wrapping.
func TestStatusSurvivesWrapping(t *testing.T) {
	raw := apiErr(402, "Insufficient credits.")
	if got := upstreamHTTPStatus(raw); got != 402 {
		t.Fatalf("raw SDK error status = %d, want 402", got)
	}
	wrapped := wrapUpstreamError(raw)
	if got := upstreamHTTPStatus(wrapped); got != 402 {
		t.Errorf("wrapped status = %d, want 402 — the taxonomy would fall back to text", got)
	}
	if faultOf(wrapped) != faultProvider {
		t.Error("a wrapped 402 must still be a provider fault")
	}
}

// A vendor out of money is unwell for everything and rests a long time. A vendor
// that simply does not carry one model is perfectly healthy and must not be
// pushed down the queue for the models it serves fine.
func TestCooldownFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"402 rests long", apiErr(402, "Insufficient credits."), coolBroke},
		{"quota rests long", errors.New("insufficient_quota"), coolBroke},
		{"429 rests briefly", apiErr(429, "overloaded"), coolBusy},
		{"503 rests briefly", apiErr(503, "unavailable"), coolBusy},
		{"transport rests briefly", errors.New("connection refused"), coolBusy},
		{"absent model does NOT rest", apiErr(403, "this model is not available for your account"), 0},
		{"401 does NOT rest", apiErr(401, "invalid api key"), 0},
		{"400 does NOT rest", apiErr(400, "bad request"), 0},

		// The status is the evidence; the text is read only where the status is
		// absent or genuinely two-valued.
		{"429 saying quota rests long", apiErr(429, "You exceeded your current quota"), coolBroke},
		{"a 503 quoting the caller does NOT rest long", apiErr(503, `upstream said: {"prompt":"what does insufficient balance mean?"}`), coolBusy},
		{"a 500 quoting a payment code does NOT rest long", apiErr(500, "internal error handling invoice 402"), coolBusy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cooldownFor(tc.err); got != tc.want {
				t.Errorf("cooldownFor(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}

	// The table above names the constants on BOTH sides, so it survives either of
	// them being changed to anything at all — including to each other, which would
	// silently delete the distinction the two exist to draw. The magnitudes are
	// therefore pinned here, and the ordering with them: two clocks, because a busy
	// vendor clears itself in seconds and an empty account waits for a human with a
	// credit card.
	if coolBusy != 15*time.Second {
		t.Errorf("coolBusy = %v, want 15s — long enough to matter, short enough that a "+
			"vendor which recovers is re-probed on the next request", coolBusy)
	}
	if coolBroke != 5*time.Minute {
		t.Errorf("coolBroke = %v, want 5m — an empty account does not refill itself", coolBroke)
	}
	if coolBroke <= coolBusy {
		t.Errorf("coolBroke (%v) must outlast coolBusy (%v), or the two kinds of refusal "+
			"are one kind and the distinction buys nothing", coolBroke, coolBusy)
	}
}

func TestCooldownExpires(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)

	if cooled.cooling(credential{"", "do-ai"}) {
		t.Fatal("a provider nobody demoted must not be cooling")
	}
	cooled.demote(credential{"", "do-ai"}, 50*time.Millisecond)
	if !cooled.cooling(credential{"", "do-ai"}) {
		t.Fatal("a demoted provider must be cooling")
	}
	time.Sleep(60 * time.Millisecond)
	if cooled.cooling(credential{"", "do-ai"}) {
		t.Error("the cooldown must expire on its own — nothing else re-probes a recovered vendor")
	}
}

// One tenant's empty account must not cost another tenant a vendor.
//
// The measured leak: tenant A connected its own openai key, that key answered
// 402, and openai was demoted for EVERYONE. Because the queue is also capped,
// the demotion did not merely reorder tenant B's queue — it pushed openai past
// the cap and REMOVED a vendor B's own healthy key could have served from.
//
// The assertion is on B's queue rather than on the map, so it survives any
// future change to how the penalty is stored.
func TestOneTenantsEmptyAccountIsNotAnothersOutage(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)

	r := route("openai", "anthropic", "fireworks")

	// Tenant A's own connected openai account is empty.
	cooled.demote(credential{"org-a", "openai"}, coolBroke)

	got := names(candidates("org-b", r, nil))
	want := []string{"openai", "anthropic", "fireworks"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tenant B's queue = %v, want %v — A's empty BYO account is a fact about "+
			"A's credential, not about the vendor, and B does not hold it", got, want)
	}
	if len(got) == 0 || got[0] != "openai" {
		t.Errorf("openai is not first for B (queue %v): a tenant that never refused anything "+
			"must see its declared route untouched", got)
	}

	// The penalty is real for the tenant that earned it — otherwise this test
	// would also pass against a cooldown that simply stopped working.
	if a := names(candidates("org-a", r, nil)); len(a) == 0 || a[0] == "openai" {
		t.Errorf("tenant A's queue = %v, want openai sorted back — A's own key IS empty", a)
	}
}

// The mirror: our platform key is what every org without its own key spends, so
// a refusal on it is scoped to the org that observed it and each org pays at
// most one round trip to learn the same thing. What must never happen is the
// lesson jumping to a tenant whose request would spend a DIFFERENT credential.
func TestACooldownNamesTheOrgThatEarnedIt(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)

	cooled.demote(credential{"org-a", "openai"}, time.Minute)

	if !cooled.cooling(credential{"org-a", "openai"}) {
		t.Error("the org that refused must be cooling")
	}
	if cooled.cooling(credential{"org-b", "openai"}) {
		t.Error("a different org must NOT inherit it")
	}
	if cooled.cooling(credential{"org-a", "anthropic"}) {
		t.Error("a different vendor must NOT inherit it")
	}
	if cooled.cooling(credential{"", "openai"}) {
		t.Error("the org-less bucket must NOT inherit it")
	}
}

func route(primary string, fallbacks ...string) *modelRoute {
	r := &modelRoute{providerName: primary, upstreamModel: "up-" + primary}
	for _, f := range fallbacks {
		r.fallbacks = append(r.fallbacks, modelRouteFallback{providerName: f, upstreamModel: "up-" + f})
	}
	return r
}

func names(cs []candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.provider)
	}
	return out
}

func TestCandidatesOrder(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)

	t.Run("declared order is preserved when everyone is healthy", func(t *testing.T) {
		cooled.forget()
		got := names(candidates("", route("do-ai", "anthropic", "fireworks"), nil))
		want := []string{"do-ai", "anthropic", "fireworks"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("order = %v, want %v — an operator's intent must survive", got, want)
		}
	})

	t.Run("a cooling provider sorts to the back, it is not removed", func(t *testing.T) {
		cooled.forget()
		cooled.demote(credential{"", "do-ai"}, time.Minute)
		got := names(candidates("", route("do-ai", "anthropic", "fireworks"), nil))
		want := []string{"anthropic", "fireworks", "do-ai"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("order = %v, want %v", got, want)
		}
	})

	t.Run("when everyone is cooling the queue is still offered", func(t *testing.T) {
		cooled.forget()
		for _, p := range []string{"do-ai", "anthropic"} {
			cooled.demote(credential{"", p}, time.Minute)
		}
		got := names(candidates("", route("do-ai", "anthropic"), nil))
		if len(got) != 2 {
			t.Fatalf("got %v — a model whose every vendor is resting must still be attempted; "+
				"demotion is a latency preference, never an authorization", got)
		}
		if got[0] != "do-ai" {
			t.Errorf("with everyone equal the declared order stands, got %v", got)
		}
	})

	t.Run("a provider that already refused this request is dropped", func(t *testing.T) {
		cooled.forget()
		prior := []attempt{{provider: "enso", err: errors.New("402")}}
		got := names(candidates("", route("enso", "do-ai"), prior))
		if strings.Join(got, ",") != "do-ai" {
			t.Errorf("got %v, want [do-ai] — asking a vendor twice in one request is waste", got)
		}
	})

	// The numbers here are literal on purpose. Asserting against maxProviders
	// asserts nothing: the constant is on both sides, so it survives being
	// changed to any value and the bound stops being pinned at all.
	t.Run("a route longer than the cap is cut to three", func(t *testing.T) {
		cooled.forget()
		got := names(candidates("", route("p1", "p2", "p3", "p4", "p5"), nil))
		want := []string{"p1", "p2", "p3"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("offered %v, want %v — three is the width of a declared route, and an "+
				"unbounded walk turns one slow afternoon into 15 upstream calls", got, want)
		}
	})

	// A7: the family pipe is not part of the declared route, so its refusal must
	// not cost the route one of its own places. It used to, which meant a model
	// served by the family could never reach Fallback2 — the operator declared
	// three alternates and got two, on exactly the requests the alternates existed for.
	t.Run("a refusal from outside the route does not spend a place in it", func(t *testing.T) {
		cooled.forget()
		prior := []attempt{{provider: "enso", err: errors.New("402")}}
		got := names(candidates("", route("p1", "p2", "p3"), prior))
		want := []string{"p1", "p2", "p3"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("offered %v after the family refused, want %v — a declared route is exactly "+
				"as wide as the cap, so charging the family to it deletes the last alternate", got, want)
		}
	})

	t.Run("the cap still holds when something refused outside the route", func(t *testing.T) {
		cooled.forget()
		prior := []attempt{{provider: "enso", err: errors.New("402")}}
		got := names(candidates("", route("p1", "p2", "p3", "p4", "p5"), prior))
		if len(got) != 3 {
			t.Errorf("offered %v, want 3 — the route walk stays bounded whatever happened before it", got)
		}
	})

	t.Run("no route means no candidates", func(t *testing.T) {
		if got := candidates("", nil, nil); got != nil {
			t.Errorf("candidates(nil) = %v, want nil", got)
		}
	})
}

// When everyone refuses, the client must be told who was asked and why the last
// one said no — and must NOT be told they are out of money when it is OUR
// account that is empty.
func TestExhausted(t *testing.T) {
	tried := []attempt{
		{provider: "enso", status: 402, err: errors.New("Insufficient credits.")},
		{provider: "do-ai", status: 429, err: errors.New("Platform overloaded")},
	}
	err := exhausted("enso-flash", tried)

	if got := statusOf(err); got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 — forwarding the upstream's 402 tells the CUSTOMER "+
			"they owe money, which is false and sends them to fix a bill that is not theirs", got)
	}
	msg := err.Error()
	for _, want := range []string{"enso-flash", "enso", "402", "do-ai", "429", "Platform overloaded"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q is missing %q — an error that names nobody sends the reader nowhere", msg, want)
		}
	}
}

// A7-chain. exhausted() quotes the last refusal verbatim, so whatever
// upstreamErrorMessage returns is published to the customer. It used to return the
// whole raw body when it could not find error.message — which put the vendor's
// name and our buy price back into an answer, through the one path the envelope
// door does not sit on.
func TestARefusalDoesNotQuoteTheUpstreamsBodyBack(t *testing.T) {
	// An aggregator's 402, in a shape upstreamErrorMessage cannot read, carrying
	// exactly what envelope.go strips from a successful answer.
	body := []byte(`{"errors":[{"reason":"Insufficient credits."}],` +
		`"provider":"GMICloud","usage":{"cost":0.00123,` +
		`"cost_details":{"upstream_inference_cost":0.00098},"is_byok":false}}`)

	said := upstreamErrorMessage(body)
	for _, tell := range []string{"GMICloud", "cost", "0.00123", "upstream_inference_cost", "is_byok"} {
		if strings.Contains(said, tell) {
			t.Errorf("upstreamErrorMessage discloses %q: %s", tell, said)
		}
	}

	// And the whole way out: the refusal a customer actually reads.
	err := exhausted("enso-flash", []attempt{
		{provider: "enso", status: 402, err: &apiError{status: 402, msg: said}},
	})
	for _, tell := range []string{"GMICloud", "0.00123", "upstream_inference_cost", "is_byok"} {
		if strings.Contains(err.Error(), tell) {
			t.Errorf("the 503 a customer reads discloses %q:\n%s", tell, err.Error())
		}
	}
	if statusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", statusOf(err))
	}

	// Still diagnostic where the upstream said something we can read.
	for _, shape := range []string{
		`{"error":{"message":"Insufficient credits."}}`,
		`{"error":"Insufficient credits."}`,
		`{"message":"Insufficient credits."}`,
		`{"detail":"Insufficient credits."}`,
	} {
		if got := upstreamErrorMessage([]byte(shape)); got != "Insufficient credits." {
			t.Errorf("upstreamErrorMessage(%s) = %q, want the reason — dropping the body must "+
				"not cost the diagnostic", shape, got)
		}
	}
	if got := upstreamErrorMessage(nil); got != "upstream error" {
		t.Errorf("upstreamErrorMessage(nil) = %q", got)
	}
}

func TestExhaustedWithNobodyToAsk(t *testing.T) {
	err := exhausted("ghost-model", nil)
	if got := statusOf(err); got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", got)
	}
	if !strings.Contains(err.Error(), "no provider is configured") {
		t.Errorf("message = %q, want it to say no provider is configured", err.Error())
	}
}

// A 402 must never reach the client as a 402. Locks the one mapping that stops
// our empty vendor account from being reported as the customer's empty wallet.
func TestUpstreamPaymentIsNotTheCustomersProblem(t *testing.T) {
	err := exhausted("enso-flash", []attempt{
		{provider: "enso", status: 402, err: apiErr(402, "Insufficient credits.")},
	})
	if statusOf(err) == http.StatusPaymentRequired {
		t.Fatal("an upstream 402 was forwarded to the customer as a 402")
	}
}

func TestFaultString(t *testing.T) {
	if fmt.Sprint(faultProvider) != "provider" || fmt.Sprint(faultRequest) != "request" {
		t.Errorf("fault names must read plainly in logs, got %q and %q", faultProvider, faultRequest)
	}
}

// ── the proxied surfaces ─────────────────────────────────────────────────────

// proxied drives the real tool/vision proxy against an upstream answering status
// with body, and reports what the caller was told plus whether the upstream was
// actually dialled.
//
// The reach matters as much as the status: a proxy that refused before sending
// anything would answer 503 too, and pass a test that only reads the code while
// serving nobody.
func proxied(t *testing.T, status int, body string) (rec *httptest.ResponseRecorder, asked bool) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer up.Close()

	req := &openai.ChatCompletionRequest{
		Model:    "glm-5.2",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}},
		Tools: []openai.Tool{{Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{Name: "now"}}},
	}
	prov := &object.Provider{Owner: "admin", Name: "do-ai", Type: "DigitalOcean",
		ProviderUrl: up.URL, ClientSecret: "k", SubType: "glm-5.2-upstream"}

	rec = httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")))
	c := &ApiController{}
	c.Init(ctx, "ApiController", "X", nil)
	c.proxyToolRequest(prov, req, time.Now(), nil, false, "org-a", nil)
	return rec, asked
}

// A request carrying tools or images skips the failover pipeline — that pipeline is
// text-only and would drop both — and is proxied to the vendor raw. The proxy
// relayed whatever status came back, so the one rule exhausted() exists to hold
// was simply absent on the surface hanzo.chat and @hanzo/dev use most.
func TestAProxiedVendorRefusalIsNotTheCallersBill(t *testing.T) {
	rec, asked := proxied(t, http.StatusPaymentRequired, `{"error":{"message":"Insufficient credits."}}`)
	if !asked {
		t.Fatal("the upstream was never dialled; this test would pass on a proxy that serves nobody")
	}
	if rec.Code == http.StatusPaymentRequired {
		t.Errorf("the caller was handed the vendor's 402 — OUR account with that vendor is empty, " +
			"and a client that acts on 402 goes to fix a bill that is not theirs")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503 — the true statement is that we could not serve this", rec.Code)
	}
	if got := codeOf(exhausted("glm-5.2", nil)); !strings.Contains(rec.Body.String(), got) {
		t.Errorf("body %q does not carry %q, so a client cannot tell this from its own empty wallet",
			rec.Body.String(), got)
	}
}

// A refused call is not a served one. The proxy read usage off the error body,
// found none, tokenized it, and settled — so the customer paid for the round trip
// that told them our vendor was broke.
func TestAProxiedRefusalIsNotBilled(t *testing.T) {
	const subject = "prox/refused"
	object.GlobalBalanceLedger.SetBalance(subject, 1000)
	hold, ok := reserveBudget(subject, 100)
	if !ok {
		t.Fatal("reserve against a funded subject must succeed")
	}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"Insufficient credits."}}`))
	}))
	defer up.Close()

	req := &openai.ChatCompletionRequest{
		Model:    "glm-5.2",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: strings.Repeat("hi ", 200)}},
		Tools: []openai.Tool{{Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{Name: "now"}}},
	}
	prov := &object.Provider{Owner: "admin", Name: "do-ai", Type: "DigitalOcean",
		ProviderUrl: up.URL, ClientSecret: "k", SubType: "glm-5.2-upstream"}

	rec := httptest.NewRecorder()
	ctx := web.NewContext()
	ctx.Reset(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")))
	c := &ApiController{}
	c.Init(ctx, "ApiController", "X", nil)
	c.proxyToolRequest(prov, req, time.Now(), nil, false, "org-a", hold)

	hold.settle(0) // the call site's fail-safe; a real settle already won if one happened
	if avail, _ := object.GlobalBalanceLedger.Available(subject); avail != 1000 {
		t.Errorf("available = %d, want 1000 — the customer was charged for a call our vendor refused", avail)
	}
}

// The reverse case, and the one a fix here can silently eat: our OWN spend gate
// relayed back by a service that fronts for us wears the same 402 and means the
// opposite thing. It is the caller's debt and has to reach them.
func TestAFrontedBillingNoticeStillReachesTheCaller(t *testing.T) {
	notice := object.InsufficientBalance("api.hanzo.ai", "org-a", "request cost")
	rec, asked := proxied(t, http.StatusPaymentRequired, string(notice.ErrorJSON()))
	if !asked {
		t.Fatal("the upstream was never dialled")
	}
	if rec.Code != http.StatusPaymentRequired {
		t.Errorf("status=%d, want 402 — this refusal is our own gate speaking through a "+
			"service that fronts for us, and the debt really is the caller's", rec.Code)
	}
}

// scrape reads the text exposition through the SAME handler GET /v1/metrics
// serves, so what this asserts is what a collector would actually pull — not
// merely that a package variable moved.
func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	object.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics scrape = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// reading is the value of one series in a text exposition, or 0 when it is absent.
func reading(t *testing.T, body, series string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), series)
		if !ok || !strings.HasPrefix(rest, " ") {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(strings.TrimSpace(rest), "%g", &v); err != nil {
			t.Fatalf("series %q has an unreadable value %q: %v", series, rest, err)
		}
		return v
	}
	return 0
}

// An empty vendor account is invisible from the outside: the caller is told 503
// and owes nothing, so nobody on their side has a reason to report it. The only
// evidence used to be a log line, and a log line is read after somebody already
// noticed. This is the reading a collector can pull and an alert can watch.
func TestAVendorOutOfMoneyReachesAnOperator(t *testing.T) {
	// A counter is process-wide and every other test in this package feeds the same
	// registry, so what is asserted is the DELTA this call produced.
	const (
		funding = `cloud_supply_refused{provider="do-ai",reason="funding"}`
		generic = `cloud_supply_refused{provider="do-ai",reason="refused"}`
	)
	was := scrape(t)
	wasFunding, wasGeneric := reading(t, was, funding), reading(t, was, generic)

	if _, asked := proxied(t, http.StatusPaymentRequired,
		`{"error":{"message":"Insufficient credits."}}`); !asked {
		t.Fatal("the upstream was never dialled")
	}

	now := scrape(t)
	if got := reading(t, now, funding) - wasFunding; got != 1 {
		t.Errorf("%s moved by %v, want 1 — a vendor whose account is spent must be visible to a "+
			"collector, not only to whoever thinks to tail the log", funding, got)
	}
	// The reason has to be the one an operator acts on. Counting this as a generic
	// refusal buries "top this account up" among rate limits and timeouts.
	if got := reading(t, now, generic) - wasGeneric; got != 0 {
		t.Errorf("the 402 also moved %s by %v — the condition that needs a human with a card "+
			"must not be filed as a busy afternoon", generic, got)
	}
}

// A refusal the vendor is not to blame for keeps its own status: every vendor
// answers a malformed body the same way, so 503 would send the caller to wait out
// a condition only they can clear.
func TestAProxiedRequestFaultKeepsItsStatus(t *testing.T) {
	rec, asked := proxied(t, http.StatusBadRequest, `{"error":{"message":"invalid 'messages': empty"}}`)
	if !asked {
		t.Fatal("the upstream was never dialled")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400 — the request itself is wrong and the caller can fix it", rec.Code)
	}
}
