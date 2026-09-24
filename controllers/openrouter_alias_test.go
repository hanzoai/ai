package controllers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
)

// orSKU is the shape of an OpenRouter id: a vendor, a slash, and the vendor's name
// for the model, lowercase. A `:free` or `:batch` variant does not match, because an
// alias that pointed at one would change the terms or the latency the caller bought.
var orSKU = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9._-]*$`)

// aliasVendor is an OpenRouter that lists every alias target at a price and answers
// chat, recording the model id each completion was sent under.
type aliasVendor struct {
	mu     sync.Mutex
	asked  []string
	bodies []string
}

func (v *aliasVendor) models() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.asked...)
}

// withAliasVendor points the OpenRouter family at that vendor for one test, on the
// static route table (no catalog file loaded), and discovers it once.
func withAliasVendor(t *testing.T) *aliasVendor {
	t.Helper()
	cooled.forget()
	forgetKeys()
	t.Cleanup(cooled.forget)
	t.Cleanup(forgetKeys)

	prevCfg := globalModelConfig
	globalModelConfig = nil
	t.Cleanup(func() { globalModelConfig = prevCfg })

	targets := map[string]bool{}
	for _, sku := range openrouterAliases {
		targets[sku] = true
	}
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var catalog strings.Builder
	catalog.WriteString(`{"data":[`)
	for i, id := range ids {
		if i > 0 {
			catalog.WriteString(",")
		}
		fmt.Fprintf(&catalog, `{"id":%q,"context_length":128000,`+
			`"architecture":{"input_modalities":["text"],"output_modalities":["text"]},`+
			`"pricing":{"prompt":"0.0000025","completion":"0.00001"}}`, id)
	}
	catalog.WriteString(`]}`)

	v := &aliasVendor{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, catalog.String())
		case "/v1/chat/completions":
			var in struct {
				Model string `json:"model"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &in)
			v.mu.Lock()
			v.asked = append(v.asked, in.Model)
			v.bodies = append(v.bodies, string(body))
			v.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(servedHeader, in.Model)
			fmt.Fprintf(w, `{"id":"gen-1","object":"chat.completion","model":%q,"provider":"OpenAI",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"4"},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`, in.Model)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(openrouterFam.urlKey, srv.URL)
	for _, k := range object.OpenRouterKeys {
		t.Setenv(k, "")
	}

	restore(t, openrouterFam)
	openrouterFam.mu.Lock()
	openrouterFam.byID, openrouterFam.ids, openrouterFam.spares = nil, nil, nil
	openrouterFam.loaded, openrouterFam.fetchedAt = false, time.Time{}
	openrouterFam.mu.Unlock()
	openrouterFam.fresh()
	return v
}

// Every alias names an OpenRouter SKU and resolves to it through the family, never
// through a route of its own: the route is the family's, the upstream id is the
// SKU, and the price is the SKU's to the cent. An alias with a static route beside
// it would be served by that route instead, so none may have one.
func TestEveryAliasResolvesThroughTheOpenRouterFamily(t *testing.T) {
	withAliasVendor(t)

	if len(openrouterAliases) == 0 {
		t.Fatal("no aliases — this check is reading nothing")
	}
	for alias, sku := range openrouterAliases {
		if alias != strings.ToLower(strings.TrimSpace(alias)) {
			t.Errorf("alias key %q is not lowercase and trimmed — lookup would never find it", alias)
		}
		if !orSKU.MatchString(sku) {
			t.Errorf("alias %q → %q: not a vendor/model OpenRouter id", alias, sku)
		}
		if alias == sku {
			t.Errorf("alias %q names itself — an OpenRouter id is served directly and needs no entry", alias)
		}
		if _, chained := openrouterAliases[sku]; chained {
			t.Errorf("alias %q → %q, which is itself an alias", alias, sku)
		}
		if r, static := modelRoutes[alias]; static {
			t.Errorf("alias %q also has a static route to %s/%s, which shadows it", alias, r.providerName, r.upstreamModel)
		}

		r := resolveModelRoute(alias)
		if r == nil {
			t.Errorf("alias %q resolved to no route", alias)
			continue
		}
		if r.providerName != openrouterFam.name || r.upstreamModel != sku {
			t.Errorf("alias %q routes to %s/%s, want %s/%s", alias, r.providerName, r.upstreamModel, openrouterFam.name, sku)
		}
		want, _ := familyModelPrice(sku)
		if got, ok := familyModelPrice(alias); !ok || got != want {
			t.Errorf("alias %q priced %+v (found %v), want the SKU's %+v", alias, got, ok, want)
		}
	}
}

// A request for gpt-4o is served as openai/gpt-4o is: the route names the
// OpenRouter family, the provider it resolves to dispatches to that family's pipe,
// the vendor is asked for its own id, the answer wears the id the caller sent, the
// terms are the priced route's, and the price is the SKU's retail.
func TestGPT4oIsServedByTheOpenRouterFamily(t *testing.T) {
	vendor := withAliasVendor(t)

	route := resolveModelRoute("GPT-4o")
	if route == nil {
		t.Fatal("gpt-4o has no route — the alias did not reach the OpenRouter family")
	}
	if route.providerName != "openrouter" || route.upstreamModel != "openai/gpt-4o" {
		t.Fatalf("gpt-4o routes to %s/%s, want openrouter/openai/gpt-4o", route.providerName, route.upstreamModel)
	}
	prov, err := object.GetModelProviderByName(route.providerName)
	if err != nil || prov == nil {
		t.Fatalf("provider %q did not resolve: %v", route.providerName, err)
	}
	fam := familyForProviderType(prov.Type)
	if fam != openrouterFam {
		t.Fatalf("provider type %q dispatches to %v, want the openrouter family", prov.Type, fam)
	}

	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"2+2?"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if refused := c.pipeToFamily(fam, "chat/completions", "openai", "gpt-4o", body, false, 0, "acme", nil, false, nil, time.Now()); refused != nil {
		t.Fatalf("the family refused the request: %+v", refused)
	}

	if asked := vendor.models(); len(asked) != 1 || asked[0] != "openai/gpt-4o" {
		t.Fatalf("the vendor was asked for %v, want [openai/gpt-4o] — the vendor's own id", asked)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(sent(c)), &answer); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, sent(c))
	}
	if answer["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o — the answer wears the id the caller sent", answer["model"])
	}
	if got := string(c.Fiber().Response().Header.Peek(headerCollection)); got != collectionDeny {
		t.Errorf("%s = %q, want %q — a priced route keeps nothing", headerCollection, got, collectionDeny)
	}
	if got := string(c.Fiber().Response().Header.Peek(servedHeader)); got != "openai/gpt-4o" {
		t.Errorf("%s = %q, want openai/gpt-4o — the arm the family named reaches the caller", servedHeader, got)
	}

	alias, sku := getModelPrice("gpt-4o"), getModelPrice("openai/gpt-4o")
	if alias != sku {
		t.Errorf("gpt-4o priced %+v, openai/gpt-4o %+v — one model, one price", alias, sku)
	}
	// $2.50 per MTok upstream at the default 1.20 margin.
	if alias.InputPerMillion != 3.0 || alias.CostInPerMillion != 2.5 {
		t.Errorf("gpt-4o input retail %.4f cost %.4f, want 3.00 over 2.50", alias.InputPerMillion, alias.CostInPerMillion)
	}
}

// An alias names a model and never a stand-in for one: when the vendor stops
// listing the SKU, the alias resolves to nothing rather than to whatever else the
// family serves.
func TestAnAliasWhoseSKUIsNotListedResolvesToNothing(t *testing.T) {
	prevCfg := globalModelConfig
	globalModelConfig = nil
	t.Cleanup(func() { globalModelConfig = prevCfg })
	withOpenRouter(t, orBody) // lists anthropic/claude-sonnet-4 and nothing of OpenAI's

	if r := resolveModelRoute("gpt-4o"); r != nil {
		t.Errorf("gpt-4o resolved to %s/%s with openai/gpt-4o unlisted", r.providerName, r.upstreamModel)
	}
	r := resolveModelRoute("claude-sonnet-4")
	if r == nil || r.providerName != "openrouter" || r.upstreamModel != "anthropic/claude-sonnet-4" {
		t.Errorf("claude-sonnet-4 = %+v, want openrouter/anthropic/claude-sonnet-4", r)
	}
}

// A route that falls over to OpenRouter is sent under the catalog's own id. The
// fallback is relayed verbatim, so an alias on the wire would be an id the vendor
// has never heard of.
func TestTheOpenRouterTailSendsTheVendorsID(t *testing.T) {
	cooled.forget()
	t.Cleanup(cooled.forget)
	catalogOf(t, "anthropic/claude-sonnet-4.5")

	r := route("do-ai")
	r.upstreamModel = "claude-sonnet-4-5" // an alias here, never an id OpenRouter lists
	for _, c := range candidates("", r, nil) {
		if c.provider == "openrouter" {
			if c.upstream != "anthropic/claude-sonnet-4.5" {
				t.Errorf("the tail sends %q, want anthropic/claude-sonnet-4.5", c.upstream)
			}
			return
		}
	}
	t.Error("no OpenRouter candidate — the same model is listed there")
}
