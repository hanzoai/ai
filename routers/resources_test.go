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

// TestPolicyKeyRoundTrip is the auth-safety proof. The filters' policy maps are
// keyed by the OLD flat names; if policyKey stopped producing them, a
// super-admin-only endpoint would silently stop being recognised as one. Each
// case below is a real key those maps contain.
func TestPolicyKeyRoundTrip(t *testing.T) {
	cases := []struct{ path, method, want string }{
		{"/v1/iam/users", "GET", "get-users"},
		{"/v1/iam/applications", "GET", "get-applications"},
		{"/v1/iam/applications", "POST", "add-application"},
		{"/v1/iam/applications/abc", "GET", "get-application"},
		{"/v1/iam/applications/abc", "PATCH", "update-application"},
		{"/v1/iam/applications/abc", "PUT", "update-application"},
		{"/v1/iam/applications/abc", "DELETE", "delete-application"},
		{"/v1/chat/chats", "POST", "add-chat"},
		{"/v1/chat/chats/x", "DELETE", "delete-chat"},
		{"/v1/chat/messages", "POST", "add-message"},
		{"/v1/chat/messages/x", "PATCH", "update-message"},
		{"/v1/chat/chats/global", "GET", "get-global-chats"},
		{"/v1/rag/stores/global", "GET", "get-global-stores"},
		{"/v1/rag/files/global", "GET", "get-global-files"},
		// Irregular plural: activities, not activitys.
		{"/v1/ops/activities", "GET", "get-activities"},
		// The URL noun and the flat noun differ here — the table says so.
		{"/v1/ai/routes", "GET", "get-model-routes"},
		{"/v1/ai/routes", "POST", "add-model-route"},
		{"/v1/ai/routes/x", "DELETE", "delete-model-route"},
		// Actions keep the exact spelling their flat route had.
		{"/v1/ops/connections/x/start", "POST", "start-connection"},
		{"/v1/ops/connections/x/stop", "POST", "stop-connection"},
		{"/v1/rag/stores/x/vectors", "POST", "refresh-store-vectors"},
		{"/v1/compute/nodes/x/tunnel", "POST", "add-node-tunnel"},
		{"/v1/rag/files/upload", "POST", "upload-file"},
		{"/v1/ops/records/commit", "POST", "commit-record"},
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
		member := r.collection() + "/someid"
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
