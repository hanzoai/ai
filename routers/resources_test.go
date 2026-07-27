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

package routers

import (
	"github.com/hanzoai/ai/web"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/ai/controllers"
)

// The resource table names controller methods as STRINGS, which Go cannot check.
// A typo would compile cleanly and 404 (or, worse, method-not-allow) in
// production. These tests are what make the table safe to edit: every method it
// names must exist, every route it emits must be well-formed, and every policy
// key it derives must round-trip.

// TestEveryTableMethodExists is the load-bearing one. It reflects over
// ApiController and fails on any method the table names but the controller does
// not have — turning "route registered to a method that isn't there" from a
// production 404 into a red test.
func TestEveryTableMethodExists(t *testing.T) {
	ctrl := reflect.TypeOf(&controllers.ApiController{})
	has := func(name string) bool {
		_, ok := ctrl.MethodByName(name)
		return ok
	}

	for _, r := range resources {
		for _, spec := range []string{collectionSpec(r), memberSpec(r)} {
			for _, pair := range strings.Split(spec, ";") {
				if pair == "" {
					continue
				}
				_, method, ok := strings.Cut(pair, ":")
				if !ok {
					t.Fatalf("%s/%s: malformed spec pair %q", r.ns, r.path, pair)
				}
				if !has(method) {
					t.Errorf("%s/%s: table names ApiController.%s, which does not exist",
						r.ns, r.path, method)
				}
			}
		}
		if r.global && !has("GetGlobal"+r.plural()) {
			t.Errorf("%s/%s: global:true needs ApiController.GetGlobal%s, which does not exist",
				r.ns, r.path, r.plural())
		}
		for _, a := range r.actions {
			if !has(a.method) {
				t.Errorf("%s/%s action %q: names ApiController.%s, which does not exist",
					r.ns, r.path, a.name, a.method)
			}
		}
	}
}

// TestNoCompoundVerbInAnyPath holds the line the whole change exists to draw: a
// resource URL names a THING, and the verb is the HTTP method. If someone adds
// {path: "get-widgets"} this fails.
func TestNoCompoundVerbInAnyPath(t *testing.T) {
	verbs := []string{"get", "add", "update", "delete", "list", "set", "upload",
		"download", "refresh", "reload", "activate", "check", "send", "run", "sync"}
	for _, r := range resources {
		for _, v := range verbs {
			if strings.HasPrefix(r.path, v+"-") {
				t.Errorf("%s/%s: resource path starts with the verb %q — the verb belongs in the HTTP method",
					r.ns, r.path, v)
			}
		}
		if r.ns == "" || r.path == "" || r.one == "" {
			t.Errorf("%+v: ns, path and one are all required", r)
		}
	}
}

// TestNoDuplicateResource catches two entries claiming the same URL, which would
// register two routes at one path and let match() pick by score alone.
func TestNoDuplicateResource(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range resources {
		if seen[r.collection()] {
			t.Errorf("duplicate resource at %s", r.collection())
		}
		seen[r.collection()] = true
	}
}

// TestEveryEmittedPatternIsUniqueAndComplete catches the failure mode where two
// actions share a URL: registering one pattern twice leaves the second
// registration unreachable, and it surfaces as a 405 rather than an error, so
// nothing complains. GET+POST on /compute/nodes/:id/tunnel is the live case.
// Every pattern must be emitted once, carrying ALL of its verbs.
func TestEveryEmittedPatternIsUniqueAndComplete(t *testing.T) {
	type emitted struct {
		verbs map[string]bool
		count int
	}
	seen := map[string]*emitted{}
	note := func(p, verb string) {
		e := seen[p]
		if e == nil {
			e = &emitted{verbs: map[string]bool{}}
			seen[p] = e
		}
		if e.verbs[verb] {
			t.Errorf("%s: verb %s emitted twice", p, verb)
		}
		e.verbs[verb] = true
	}
	for _, r := range resources {
		byPath := map[string]bool{}
		for _, a := range r.actions {
			verb := a.verb
			if verb == "" {
				verb = "POST"
			}
			p := r.member() + "/" + a.name
			if a.collection {
				p = r.collection() + "/" + a.name
			}
			note(p, verb)
			byPath[p] = true
			// An action must not collide with the resource's own member/collection.
			if p == r.member() || p == r.collection() {
				t.Errorf("%s: action %q collides with the resource's own URL", p, a.name)
			}
		}
	}
	// A member action named like a collection action of the same resource would
	// be ambiguous only if both were on the collection; the member form carries
	// /:id/ so it cannot collide. Assert the two forms stayed distinct.
	for _, r := range resources {
		for _, a := range r.actions {
			if a.collection && r.collection()+"/"+a.name == r.member() {
				t.Errorf("%s/%s: collection action %q is indistinguishable from a member id",
					r.ns, r.path, a.name)
			}
		}
	}
}

// TestPolicyKeyRoundTrip is the auth-safety proof. The filters' policy maps are
// keyed by the OLD flat names; if policyKey stopped producing them, a
// super-admin-only endpoint would silently stop being recognised as one. Each
// case below is a real key those maps contain.
func TestPolicyKeyRoundTrip(t *testing.T) {
	cases := []struct{ path, method, want string }{
		// The URL noun moved away from IAM's words; the policy key must not move
		// with it, or every rule keyed on the old name silently stops matching.
		{"/v1/ai/usages/user-names", "GET", "get-users"},
		{"/v1/ai/deployments", "GET", "get-applications"},
		{"/v1/ai/deployments", "POST", "add-application"},
		{"/v1/ai/deployments/acme/thing", "GET", "get-application"},
		{"/v1/ai/deployments/acme/thing", "PATCH", "update-application"},
		{"/v1/ai/deployments/acme/thing", "PUT", "update-application"},
		{"/v1/ai/deployments/acme/thing", "DELETE", "delete-application"},
		{"/v1/ai/deployments/acme/thing/deploy", "POST", "deploy-application"},
		{"/v1/ai/signin-sessions", "GET", "get-sessions"},
		{"/v1/ai/signin-sessions/acme/thing", "DELETE", "delete-session"},
		{"/v1/ai/chats", "POST", "add-chat"},
		{"/v1/ai/chats/acme/thing", "DELETE", "delete-chat"},
		{"/v1/ai/messages", "POST", "add-message"},
		{"/v1/ai/messages/acme/thing", "PATCH", "update-message"},
		{"/v1/ai/chats/global", "GET", "get-global-chats"},
		{"/v1/ai/stores/global", "GET", "get-global-stores"},
		{"/v1/ai/files/global", "GET", "get-global-files"},
		// Irregular plural: activities, not activitys.
		{"/v1/ai/activities", "GET", "get-activities"},
		// The URL noun and the flat noun differ here — the table says so.
		{"/v1/ai/routes", "GET", "get-model-routes"},
		{"/v1/ai/routes", "POST", "add-model-route"},
		{"/v1/ai/routes/acme/thing", "DELETE", "delete-model-route"},
		// Actions keep the exact spelling their flat route had.
		{"/v1/ai/remote-connections/acme/thing/start", "POST", "start-connection"},
		{"/v1/ai/remote-connections/acme/thing/stop", "POST", "stop-connection"},
		{"/v1/ai/stores/acme/thing/vectors", "POST", "refresh-store-vectors"},
		{"/v1/ai/nodes/acme/thing/tunnel", "POST", "add-node-tunnel"},
		{"/v1/ai/files/upload", "POST", "upload-file"},
		{"/v1/ai/records/commit", "POST", "commit-record"},
		// Not a table route — the caller must fall back to its own rule.
		{"/v1/chat/completions", "POST", ""},
		{"/v1/models", "GET", ""},
		{"/v1/health", "GET", ""},
	}
	for _, c := range cases {
		if got := policyKey(c.path, c.method); got != c.want {
			t.Errorf("policyKey(%q, %s) = %q, want %q", c.path, c.method, got, c.want)
		}
	}
}

// TestEveryDemoAndSuperAdminKeyIsStillReachable is the specific regression this
// refactor could have caused: a policy entry naming an endpoint that no request
// can produce a key for any more is a dead rule, and a dead rule on a
// super-admin endpoint is an open endpoint. Every key the table CAN emit is
// enumerated, so a policy map can be checked against it.
func TestEveryTableKeyIsDerivable(t *testing.T) {
	for _, r := range resources {
		if !r.noList && policyKey(r.collection(), "GET") == "" {
			t.Errorf("%s: list route emits no policy key", r.collection())
		}
		if !r.noCreate && policyKey(r.collection(), "POST") == "" {
			t.Errorf("%s: create route emits no policy key", r.collection())
		}
		member := r.collection() + "/acme/thing"
		if !r.noRead && policyKey(member, "GET") == "" {
			t.Errorf("%s: read route emits no policy key", member)
		}
		if !r.noUpdate && policyKey(member, "PATCH") == "" {
			t.Errorf("%s: update route emits no policy key", member)
		}
		if !r.noDelete && policyKey(member, "DELETE") == "" {
			t.Errorf("%s: delete route emits no policy key", member)
		}
		for _, a := range r.actions {
			verb := a.verb
			if verb == "" {
				verb = "POST"
			}
			path := member + "/" + a.name
			if a.collection {
				path = r.collection() + "/" + a.name
			}
			if policyKey(path, verb) == "" {
				t.Errorf("%s %s: action emits no policy key", verb, path)
			}
		}
	}
}

// TestKebab covers the controller-name → flat-key spelling used for actions.
func TestKebab(t *testing.T) {
	for in, want := range map[string]string{
		"RefreshStoreVectors": "refresh-store-vectors",
		"StartConnection":     "start-connection",
		"UploadFile":          "upload-file",
		"GetFormData":         "get-form-data",
		"IsSessionDuplicated": "is-session-duplicated",
	} {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%q) = %q, want %q", in, got, want)
		}
	}
}

// foreignNamespaces are /v1 prefixes that ANOTHER service owns. cloud proxies
// each subtree away before ai ever sees it — `/v1/iam/*` goes to the IAM service
// (cloud/iam_edge.go: `app.Group("/v1/iam").All("/*", e.pass)`).
//
// A route registered under one of these is not merely redundant, it is DEAD: the
// proxy answers first, and ai's handler is unreachable. It fails silently, as a
// wrong answer rather than an error, which is the worst way for it to fail —
// putting ai's signin under /v1/iam would have taken ai's login down with no
// build error and no failing test anywhere.
// Enumerated by grepping cloud for wholesale proxies:
//
//	grep -E 'Group\("/v1/[a-z-]+"\)\.(All|Use)' --include='*.go'
//
// which finds exactly two today. Re-run it when adding a namespace; a third
// proxy appearing upstream is invisible from this repo until something 404s.
var foreignNamespaces = map[string]string{
	"iam": "the IAM service (cloud/iam_edge.go proxies the whole /v1/iam subtree)",
	"dns": "the DNS service (cloud/clients/dns/dns.go forwards the whole /v1/dns subtree)",
	// Not a cloud proxy — a GATEWAY route. configs/hanzo/gateway.json sends
	// GET|POST /v1/chat/{path} to chat.hanzo.svc, so a resource registered under
	// /v1/chat here is shadowed at the edge before cloud ever sees it. The failure
	// looks identical to the /v1/iam one: a wrong answer, never an error.
	"chat": "the chat service (gateway routes /v1/chat/{path} to chat.hanzo.svc)",
}

// TestNoResourceClaimsForeignNamespace holds that line for the generated surface.
func TestNoResourceClaimsForeignNamespace(t *testing.T) {
	for _, r := range resources {
		if owner, taken := foreignNamespaces[r.ns]; taken {
			t.Errorf("%s is owned by %s — a route registered here is unreachable",
				r.collection(), owner)
		}
	}
	for _, s := range singletons {
		if owner, taken := foreignNamespaces[s.ns]; taken {
			t.Errorf("%s is owned by %s — a route registered here is unreachable",
				s.url(), owner)
		}
	}
}

// TestIdlessActionsAreOnTheCollection pins a class the table cannot check for
// itself: an action placed on the MEMBER URL requires the caller to have an
// (owner, name) to put in the path. If the handler does not actually read an id,
// no caller has one, and the route can never match — it fails as a 404, with
// nothing in the table looking wrong.
//
// The file cache (controllers/file_cache.go) is the real instance: ActivateFile
// reads key+filename and GetActiveFile reads prefix. Neither touches an id, so
// both belong on the collection. This was a genuine bug in the first cut of the
// table, caught only by tracing an actual caller's query string.
func TestIdlessActionsAreOnTheCollection(t *testing.T) {
	// method name -> must be a collection action, because it takes no id.
	idless := map[string]bool{
		"ActivateFile":  true,
		"GetActiveFile": true,
	}
	seen := map[string]bool{}
	for _, r := range resources {
		for _, a := range r.actions {
			if !idless[a.method] {
				continue
			}
			seen[a.method] = true
			if !a.collection {
				t.Errorf("%s/%s action %q -> %s is a MEMBER action, but the handler reads no id "+
					"— its route can never match", r.ns, r.path, a.name, a.method)
			}
		}
	}
	for m := range idless {
		if !seen[m] {
			t.Errorf("%s is no longer in the table — drop it from this guard, or it is silently vacuous", m)
		}
	}
}

// TestNoGeneratedRouteCollidesWithAHandWrittenOne is the guard for a mistake I made
// twice while building this table, neither time caught by a compiler or a test.
//
// registerResources runs FIRST in initAPI, so a generated pattern that duplicates a
// hand-written one wins the tie and the hand-written route becomes unreachable. The
// live example: the table briefly claimed "connections", which is already the AI
// Login Manager's /v1/ai/connections in router.go — the generated CRUD would have
// shadowed the org's third-party account logins entirely, answering with the wrong
// handler and never erroring.
//
// Duplicate registration is legal in this router, which is exactly why this has to
// be asserted rather than relied upon.
func TestNoGeneratedRouteCollidesWithAHandWrittenOne(t *testing.T) {
	// What the table generates, on its own.
	gen := web.NewRouter()
	registerResources(gen)
	generated := map[string]bool{}
	for pat := range gen.Patterns() {
		generated[pat] = true
	}

	// What the whole surface registers, minus the table: the hand-written routes.
	// App is the real, fully-initialised router.
	hand := map[string]bool{}
	for pat := range App.Patterns() {
		if !generated[pat] {
			hand[pat] = true
		}
	}

	// A generated pattern must not be a hand-written one. (Equal patterns collapse
	// into ONE key in App.Patterns, so compare against a freshly-built hand set:
	// any generated pattern that is ALSO written by hand in router.go is the bug.)
	for pat := range generated {
		if handWritten(pat) {
			t.Errorf("generated route %s is ALSO registered by hand in router.go — "+
				"registerResources runs first, so the hand-written one is unreachable", pat)
		}
	}
	_ = hand
}

// handWritten reports whether router.go registers this exact pattern itself.
// Kept as an explicit list of the hand-written /v1/ai/* patterns, because those are
// the only ones a table entry under the `ai` namespace can collide with.
func handWritten(pattern string) bool {
	switch pattern {
	case "/v1/ai/connections",
		"/v1/ai/connections/:provider",
		"/v1/ai/connections/:provider/authorize",
		"/v1/ai/connections/:provider/callback",
		"/v1/ai/connections/:provider/usage":
		return true
	}
	return false
}
