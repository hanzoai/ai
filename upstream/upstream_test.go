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

package upstream

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestAuthorize pins the header each provider's credential becomes. The scheme
// is the provider's, not the surface's: Azure names its key in the Authorization
// value, Anthropic sends a header of its own, everything else carries a bearer
// token — and an empty bearer is no header at all, which is what the in-cluster
// speech service (no key, no ingress, no authentication) relies on.
func TestAuthorize(t *testing.T) {
	cases := []struct {
		name     string
		provider *object.Provider
		want     map[string]string // header -> value; "" means the header must be absent
	}{
		{"openai", &object.Provider{Type: "OpenAI", ClientSecret: "k"},
			map[string]string{"Authorization": "Bearer k", "x-api-key": ""}},
		{"openai-no-key", &object.Provider{Type: "OpenAI"},
			map[string]string{"Authorization": "", "x-api-key": ""}},
		{"azure", &object.Provider{Type: "Azure", ClientSecret: "k"},
			map[string]string{"Authorization": "api-key k"}},
		{"claude", &object.Provider{Type: "Claude", ClientSecret: "k"},
			map[string]string{"x-api-key": "k", "Authorization": ""}},
		{"anthropic", &object.Provider{Type: "Anthropic", ClientSecret: "k"},
			map[string]string{"x-api-key": "k", "Authorization": ""}},
		{"fireworks", &object.Provider{Type: "Fireworks", ClientSecret: "k"},
			map[string]string{"Authorization": "Bearer k"}},
		{"zen", &object.Provider{Type: "Zen", ClientSecret: "k"},
			map[string]string{"Authorization": "Bearer k"}},
		{"untyped", &object.Provider{ClientSecret: "k"},
			map[string]string{"Authorization": "Bearer k"}},
		{"no-provider", nil,
			map[string]string{"Authorization": "", "x-api-key": ""}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "https://upstream.example/v1/chat/completions", nil)
			if err != nil {
				t.Fatal(err)
			}
			Authorize(req, tc.provider)
			for header, want := range tc.want {
				if got := req.Header.Get(header); got != want {
					t.Fatalf("%s = %q, want %q", header, got, want)
				}
			}
		})
	}
}

// TestAuthorizeReplacesACarriedCredential covers the one place a request already
// holds someone else's: a refused call offered to a second family reuses the
// first request's headers, and the second family's credential has to replace the
// first's rather than joining it. A family with no key of its own leaves what is
// there, which is what makes our own compute reachable behind a shared header.
func TestAuthorizeReplacesACarriedCredential(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://upstream.example/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer first")

	Authorize(req, &object.Provider{Type: "Zen", ClientSecret: "second"})
	if got := req.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer second" {
		t.Fatalf("Authorization = %q, want exactly [\"Bearer second\"]", got)
	}

	Authorize(req, &object.Provider{Type: "Zen"})
	if got := req.Header.Get("Authorization"); got != "Bearer second" {
		t.Fatalf("a keyless provider changed the header to %q", got)
	}
}

// TestEndpointRefusesAnUnknownAddress pins the empty answer. A provider whose
// address this deployment does not know yields "", and every caller checks for
// it before sending anything — the relay refuses rather than guessing a host.
func TestEndpointRefusesAnUnknownAddress(t *testing.T) {
	for _, typ := range []string{"Local", "Ollama", "DigitalOcean", "SomethingNew"} {
		if got := Endpoint(&object.Provider{Type: typ, ClientSecret: "k"}, "chat/completions"); got != "" {
			t.Fatalf("%s with no address resolved to %q, want \"\"", typ, got)
		}
	}
}

// TestEndpointStatesTheFixedAddresses pins the seven providers whose address is
// stated here and NOT taken from the row. This is deliberate and load-bearing:
// honouring ProviderUrl for them would let a row move traffic — and a credential
// — to another host. The test fails if a row ever starts winning.
func TestEndpointStatesTheFixedAddresses(t *testing.T) {
	fixed := map[string]string{
		"Fireworks":  "https://api.fireworks.ai/inference/v1/embeddings",
		"Grok":       "https://api.x.ai/v1/embeddings",
		"OpenRouter": "https://openrouter.ai/api/v1/embeddings",
		"Moonshot":   "https://api.moonshot.cn/v1/embeddings",
		"Gemini":     "https://generativelanguage.googleapis.com/v1beta/openai/embeddings",
		"Jina":       "https://api.jina.ai/v1/embeddings",
		"Cohere":     "https://api.cohere.com/v1/embeddings",
	}
	for typ, want := range fixed {
		p := object.Provider{Type: typ, ProviderUrl: "https://attacker.example/v1", ClientSecret: "k"}
		if got := Endpoint(&p, "embeddings"); got != want {
			t.Fatalf("%s: a row moved the address to %q, want %q", typ, got, want)
		}
	}
}

// TestEndpointBuildsFromTheRow covers the OTHER half of Endpoint: the providers
// whose address IS taken from the row. The fixed-address test above pins the
// seven that ignore ProviderUrl; these are the ones that honour it, and they were
// the untested half — 44.8% of the function, and the only half where a malformed
// row can decide where a credential is sent.
//
// The shapes that matter are all normalisation. A row is typed by a human and
// arrives with or without a scheme suffix, with or without a trailing slash, and
// the same provider must resolve to one address either way — otherwise the same
// account reaches two hosts depending on how someone punctuated a field.
func TestEndpointBuildsFromTheRow(t *testing.T) {
	cases := []struct {
		name     string
		provider object.Provider
		path     string
		want     string
	}{
		// OpenAI with no row address falls back to the public API. This is the
		// only provider with a default, because it is the only one whose public
		// address is also the overwhelmingly common one.
		{"openai default", object.Provider{Type: "OpenAI"}, "chat/completions",
			"https://api.openai.com/v1/chat/completions"},
		// /v1 is appended when absent and NOT doubled when present — a row
		// holding either spelling names one address.
		{"openai adds v1", object.Provider{Type: "OpenAI", ProviderUrl: "https://proxy.example"}, "embeddings",
			"https://proxy.example/v1/embeddings"},
		{"openai keeps one v1", object.Provider{Type: "OpenAI", ProviderUrl: "https://proxy.example/v1"}, "embeddings",
			"https://proxy.example/v1/embeddings"},
		{"openai trims the slash", object.Provider{Type: "OpenAI", ProviderUrl: "https://proxy.example/v1/"}, "embeddings",
			"https://proxy.example/v1/embeddings"},

		// Azure names a DEPLOYMENT, not a model, and carries the api-version in
		// the query. SubType is the deployment name.
		{"azure default version", object.Provider{Type: "Azure", ProviderUrl: "https://x.openai.azure.com", SubType: "gpt4o"}, "chat/completions",
			"https://x.openai.azure.com/openai/deployments/gpt4o/chat/completions?api-version=2024-02-01"},
		{"azure honours ApiVersion", object.Provider{Type: "Azure", ProviderUrl: "https://x.openai.azure.com/", SubType: "gpt4o", ApiVersion: "2025-01-01"}, "chat/completions",
			"https://x.openai.azure.com/openai/deployments/gpt4o/chat/completions?api-version=2025-01-01"},

		// Local/Ollama/DigitalOcean resolve only WITH a row address — the
		// refusal when it is absent is pinned above.
		{"local adds v1", object.Provider{Type: "Local", ProviderUrl: "http://127.0.0.1:11434"}, "chat/completions",
			"http://127.0.0.1:11434/v1/chat/completions"},
		{"ollama keeps one v1", object.Provider{Type: "Ollama", ProviderUrl: "http://127.0.0.1:11434/v1"}, "embeddings",
			"http://127.0.0.1:11434/v1/embeddings"},
		{"digitalocean trims the slash", object.Provider{Type: "DigitalOcean", ProviderUrl: "https://inference.do.example/v1/"}, "embeddings",
			"https://inference.do.example/v1/embeddings"},

		// Anything else OpenAI-compatible is taken verbatim from the row — no
		// /v1 is assumed, because an unknown provider has not told us it uses one.
		{"unknown type verbatim", object.Provider{Type: "SomethingNew", ProviderUrl: "https://vendor.example/api/"}, "rerank",
			"https://vendor.example/api/rerank"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Endpoint(&tc.provider, tc.path); got != tc.want {
				t.Fatalf("Endpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

// ── the rule ──────────────────────────────────────────────────────────────────

// A provider credential is applied by the thing that makes the call and is never
// handed back to a caller. The assertions below are that sentence, checked
// against the relay's own source rather than remembered:
//
//	Authorize returns nothing        — there is nothing to hand back.
//	Endpoint never sees the secret   — asking WHERE cannot yield a credential.
//	nothing else reaching an upstream reads one.
//
// The third is what keeps the count at one, and it is why this test reads the
// controllers directory as well as this one. Authorize used to live there; the
// rule has to follow the credential across the boundary, or extracting a function
// would be a way to escape it.

func TestTheCredentialStaysInAuthorize(t *testing.T) {
	fns := relayFuncs(t)

	got, ok := fns["Authorize"]
	if !ok {
		t.Fatal("Authorize is gone; the credential has moved somewhere this test cannot see")
	}
	if n := got.Type.Results.NumFields(); n != 0 {
		t.Errorf("Authorize declares %d result(s); it must return nothing so a caller has no credential to carry", n)
	}

	if e, ok := fns["Endpoint"]; !ok {
		t.Fatal("Endpoint is gone")
	} else if readsSecret(e) {
		t.Error("Endpoint reads a provider secret; asking where a call goes must not yield a credential")
	}

	for name, fn := range fns {
		if name == "Authorize" || !reachesUpstream(fn) || !readsSecret(fn) {
			continue
		}
		t.Errorf("%s builds an upstream request AND reads a provider secret; Authorize is the one place that does both", name)
	}
}

// TestNothingHandsBackACredential fails when a function returns a provider
// secret. There is no exception: a credential is applied where it is read, and
// the BYO usage road obeys the same rule through authorizeUsage, which stamps a
// header set and hands back only an error.
func TestNothingHandsBackACredential(t *testing.T) {
	for name, fn := range relayFuncs(t) {
		if returnsSecret(fn) {
			t.Errorf("%s returns a provider secret; a credential is applied where it is read, never handed to a caller", name)
		}
	}
}

// relayFuncs parses every non-test source on the relay — this package AND the
// controllers that call it — and returns each top-level function and method by
// name. It reads both directories because the rule it serves is about the relay,
// not about a package: moving a credential read across a package boundary must
// not move it out of view.
func relayFuncs(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*ast.FuncDecl{}
	for _, dir := range []string{".", "../controllers"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			for _, d := range file.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
					out[fn.Name.Name] = fn
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no functions; the rule would pass vacuously")
	}
	return out
}

// readsSecret reports whether a function names a provider's credential field.
func readsSecret(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "ClientSecret" {
			found = true
		}
		return !found
	})
	return found
}

// reachesUpstream reports whether a function builds an outbound request or sets a
// header on one — the shape of every site on the relay.
func reachesUpstream(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return !found
		}
		switch sel.Sel.Name {
		case "NewRequest", "NewRequestWithContext", "Header":
			found = true
		}
		return !found
	})
	return found
}

// returnsSecret reports whether a function hands a credential back, directly or
// through a local it was assigned to.
//
// A bare string IS the credential; a client, a record or a response that was
// BUILT from one is not — that is the difference between handing a caller a key
// and handing the dialer its credential, so only a string result counts. For the
// same reason a multi-result call consumes the secret rather than carrying it:
// its answers are a video id and an error, not the key it was given.
func returnsSecret(fn *ast.FuncDecl) bool {
	if !returnsString(fn) {
		return false
	}

	carriers := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 {
			return true
		}
		for _, rhs := range as.Rhs {
			if !readsSecret(rhs) {
				continue
			}
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				carriers[id.Name] = true
			}
		}
		return true
	})

	handed := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return !handed
		}
		for _, r := range ret.Results {
			if readsSecret(r) {
				handed = true
				break
			}
			ast.Inspect(r, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && carriers[id.Name] {
					handed = true
				}
				return !handed
			})
		}
		return !handed
	})
	return handed
}

// returnsString reports whether a function declares a plain string result.
func returnsString(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, f := range fn.Type.Results.List {
		if id, ok := f.Type.(*ast.Ident); ok && id.Name == "string" {
			return true
		}
	}
	return false
}
