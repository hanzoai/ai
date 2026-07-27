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
	"fmt"
	"sort"
	"strings"
)

// The published API description of the resource surface is DERIVED from the same
// table that registers it — never written a second time.
//
// A hand-maintained spec beside a route table is two sources for one fact, and
// they drift in the direction that hurts: the spec is what customers and every
// generated SDK believe, so it goes stale silently while the routes move. The
// checked-in swagger.json in this repo is the standing proof — it still describes
// `/api/<verb>-<noun>`, a base path this service has never served.
//
// So OpenAPIPaths is the ONE accessor: cmd/openapi renders it into the canonical
// spec (hanzoai/openapi ai/openapi.yaml) between generated markers, and
// TestOpenAPISpecMatchesTheTable re-renders and fails on any difference. Adding a
// resource to the table publishes it; nothing else to remember.
//
// It owns PATHS only. The shared components (the response envelope, the reusable
// error responses) are stable and hand-authored in the spec, because they do not
// vary per resource — one is generated, the other is not, and the boundary is
// exactly where the repetition is.

// OpenAPIPaths returns the OpenAPI 3.1 Path Item Objects for every route the
// resource table generates, keyed by path. The result is deterministic: maps are
// emitted in sorted order by the caller, and nothing here reads the clock, the
// environment, or a random source.
func OpenAPIPaths() map[string]any {
	out := map[string]any{}

	add := func(path, method string, op map[string]any) {
		path = openAPIPath(path)
		item, ok := out[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			out[path] = item
		}
		item[strings.ToLower(method)] = op
	}

	for _, r := range resources {
		tag := titleCase(r.path)
		coll, member := r.collection(), r.member()

		if !r.noList {
			add(coll, "GET", op(tag, "List "+r.path, "Get"+r.plural(),
				"List the caller's "+r.path+".", nil, false))
		}
		if !r.noCreate {
			add(coll, "POST", op(tag, "Create a "+singularNoun(r), "Add"+r.one,
				"Create one "+singularNoun(r)+".", nil, true))
		}
		if r.global {
			add(coll+"/global", "GET", op(tag, "List "+r.path+" across tenants",
				"GetGlobal"+r.plural(),
				"Cross-tenant listing. Admin-only; a tenant caller is refused.", nil, false))
		}

		mp := memberParams()
		if !r.noRead {
			read := r.readMethod
			if read == "" {
				read = "Get" + r.one
			}
			add(member, "GET", op(tag, "Retrieve a "+singularNoun(r), read,
				"Read one "+singularNoun(r)+" by its (owner, name) key.", mp, false))
		}
		if !r.noUpdate {
			// One handler, two verbs — so two operations, and they MUST NOT share an
			// operationId (a code generator would emit one method name twice and most
			// keep only the last).
			add(member, "PATCH", op(tag, "Update a "+singularNoun(r), "Update"+r.one,
				"Update one "+singularNoun(r)+". PATCH and PUT reach the same handler, "+
					"which has always taken a whole object.", mp, true))
			add(member, "PUT", op(tag, "Replace a "+singularNoun(r), "Replace"+r.one,
				"Identical to PATCH — the handler takes a whole object either way.", mp, true))
		}
		if !r.noDelete {
			add(member, "DELETE", op(tag, "Delete a "+singularNoun(r), "Delete"+r.one,
				"Delete one "+singularNoun(r)+".", mp, false))
		}

		for _, a := range r.actions {
			verb := a.verb
			if verb == "" {
				verb = "POST"
			}
			p, params := coll+"/"+a.name, []any(nil)
			if !a.collection {
				p, params = member+"/"+a.name, mp
			}
			body := verb == "POST" || verb == "PUT" || verb == "PATCH"
			add(p, verb, op(tag, actionSummary(a.name, singularNoun(r)), a.method,
				"", params, body))
		}
	}

	for _, s := range singletons {
		tag := titleCase(strings.ReplaceAll(s.path, "/", "-"))
		// Iterate a FIXED verb order, not the map: Go randomizes map order, and a
		// generator whose output reorders between runs cannot be diffed for drift.
		for _, verb := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			method, ok := s.verbs[verb]
			if !ok {
				continue
			}
			body := verb == "POST" || verb == "PUT" || verb == "PATCH"
			// A singleton may map two verbs to ONE handler (PATCH+PUT -> Update…),
			// which would emit one operationId twice. The verb disambiguates.
			stem := method
			if dup := countVerbsFor(s, method); dup > 1 && verb != primaryVerbFor(s, method) {
				stem = titleCase(strings.ToLower(verb)) + method
			}
			add(s.url(), verb, op(tag, actionSummary(s.path, ""), stem, "", nil, body))
		}
	}
	return out
}

// countVerbsFor reports how many verbs of this singleton map to one handler.
func countVerbsFor(s singleton, method string) int {
	n := 0
	for _, m := range s.verbs {
		if m == method {
			n++
		}
	}
	return n
}

// primaryVerbFor picks the ONE verb that keeps the bare operationId when several
// share a handler — deterministic, so the emitted spec is stable.
func primaryVerbFor(s singleton, method string) string {
	for _, verb := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
		if s.verbs[verb] == method {
			return verb
		}
	}
	return ""
}

// op builds one Operation Object.
//
// Every response is the SAME envelope: this surface answers {status,msg,data,data2}
// on success AND on a handled failure, so a 200 does not imply success — `status`
// does. That is a property of the backend, not a description choice, and saying so
// here is the difference between a spec a client can trust and one that quietly
// misleads.
func op(tag, summary, operation, description string, params []any, body bool) map[string]any {
	o := map[string]any{
		"tags":        []any{tag},
		"summary":     summary,
		"operationId": lowerFirst(operation),
		"responses": map[string]any{
			"200": map[string]any{
				"description": "Envelope. `status` is \"ok\" or \"error\" — check it; a handled " +
					"failure is still HTTP 200.",
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{"$ref": "#/components/schemas/Envelope"},
					},
				},
			},
			"401": map[string]any{"$ref": "#/components/responses/Unauthorized"},
			"403": map[string]any{"$ref": "#/components/responses/Forbidden"},
		},
	}
	if description != "" {
		o["description"] = description
	}
	if len(params) > 0 {
		o["parameters"] = params
	}
	if body {
		o["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{"type": "object"},
				},
			},
		}
	}
	return o
}

// memberParams are the two segments that identify a member.
//
// They are two parameters, not one, because the identity of every object here is
// the PAIR (owner, name) and a single segment cannot carry it — a composite id
// URL-encoded into one segment is decoded back to a separator before routing and
// matches nothing. The spec has to say that or every generated SDK gets it wrong.
func memberParams() []any {
	return []any{
		map[string]any{
			"name": "owner", "in": "path", "required": true,
			"schema":      map[string]any{"type": "string"},
			"description": "Owning organization.",
		},
		map[string]any{
			"name": "name", "in": "path", "required": true,
			"schema":      map[string]any{"type": "string"},
			"description": "Resource name, unique within the owner.",
		},
	}
}

// actionSummary renders a URL segment as a human summary: "query-second" ->
// "Query second", "mcp-tools" -> "Mcp tools".
func actionSummary(name, subject string) string {
	s := titleCase(strings.ReplaceAll(name, "-", " "))
	if subject != "" {
		return s + " (" + subject + ")"
	}
	return s
}

// openAPIPath rewrites the router's ":name" parameter syntax into OpenAPI's
// "{name}". The two describe the same route; only the spelling differs, and a
// spec left in the router's spelling silently produces SDKs that request a
// literal ":owner".
func openAPIPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") {
			segs[i] = "{" + s[1:] + "}"
		}
	}
	return strings.Join(segs, "/")
}

func titleCase(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == ' ' || r == '/' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// OpenAPIPathsSorted returns the paths in a stable order, so a re-render produces
// byte-identical output and the drift test compares cleanly.
func OpenAPIPathsSorted() []string {
	p := OpenAPIPaths()
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AssertOperationIDsUnique reports a duplicate operationId, which every code
// generator turns into a duplicate method name and most turn into a silent
// overwrite.
func AssertOperationIDsUnique() error {
	seen := map[string]string{}
	for _, path := range OpenAPIPathsSorted() {
		item := OpenAPIPaths()[path].(map[string]any)
		methods := make([]string, 0, len(item))
		for m := range item {
			methods = append(methods, m)
		}
		sort.Strings(methods)
		for _, m := range methods {
			id, _ := item[m].(map[string]any)["operationId"].(string)
			if prev, dup := seen[id]; dup {
				return fmt.Errorf("operationId %q is used by both %s and %s %s", id, prev, strings.ToUpper(m), path)
			}
			seen[id] = strings.ToUpper(m) + " " + path
		}
	}
	return nil
}
