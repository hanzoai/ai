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

	"github.com/zap-proto/zip"
)

// The published API description of this service is DERIVED from the router that
// serves it — never written a second time.
//
// A hand-maintained spec beside a route table is two sources for one fact, and
// they drift in the direction that hurts: the spec is what customers and every
// generated SDK believe, so it goes stale silently while the routes move. The
// checked-in swagger.json in this repo is the standing proof — it still describes
// `/api/<verb>-<noun>`, a base path this service has never served.
//
// So [Document] is the ONE accessor, and it is a whole document rather than a
// fragment somebody else completes. hanzoai/cloud reaches this surface through a
// single `/v1/*` door and publishes what comes back as the fleet's contract, so
// what it asks for is everything an operation owes a reader: the address, the
// verb, the sentence, and — where this service declares one — the body.
//
// Membership comes from [App.Patterns], the live route table, because that is
// the only thing that knows what answers. Detail comes from wherever it is
// already written: the resource table for the half it generates, the doc comment
// on the handler for the rest. Nothing is copied.

// Document is everything this service serves, as one OpenAPI 3.1 document.
//
// The result is deterministic: it reads the route table and two package-level
// tables, and nothing here reads the clock, the environment, or a random source.
func Document() map[string]any {
	described := items()
	said := map[string]Doc{}
	for _, w := range wired {
		said[w.Method+" "+openAPIPath(w.Path)] = Doc{Summary: w.Summary, Description: w.Description}
	}

	paths := map[string]any{}
	for pattern, methods := range App.Patterns() {
		path := openAPIPath(pattern)
		for _, registered := range methods {
			for _, verb := range expand(registered) {
				item, ok := paths[path].(map[string]any)
				if !ok {
					item = map[string]any{}
					paths[path] = item
				}
				generated, _ := described[path].(map[string]any)
				o, _ := generated[strings.ToLower(verb)].(map[string]any)
				if o == nil {
					// A hand-written route. Its body is the OpenAI wire format or a
					// stream, not this service's envelope, so the only honest thing
					// to say about it is what its handler says.
					o = map[string]any{}
				}
				d, ok := said[verb+" "+path]
				if !ok {
					// One handler answering every verb says one thing about all of
					// them, and the table keeps the star it was registered under.
					d = said["* "+path]
				}
				if d.Summary != "" {
					o["summary"] = d.Summary
				}
				if d.Description != "" {
					o["description"] = d.Description
				}
				// Named HERE because this is the one place that knows both the verb
				// and the address. items() builds operations before it knows which
				// verbs the router actually registered for them.
				o["operationId"] = zip.ID(verb, path)
				// The PRODUCT this operation belongs to. hanzoai/cloud counts a
				// product by the tags its operations carry, so an untagged operation
				// belongs to nothing: it reaches the composed document and then
				// disappears from every per-product total, which is how a surface
				// loses whole products with the path count unchanged.
				if t := product(path); t != "" {
					o["tags"] = []any{t}
				}
				// A templated segment NAMES a parameter, and an operation that
				// leaves it undeclared reaches a client with the value in its
				// address and not in its signature — `get_videos_by_id(self)`
				// took no id and requested the literal "{id}". Merged rather
				// than assigned, so a hand-written list survives.
				if names := pathParams(path); len(names) > 0 {
					params, _ := o["parameters"].([]any)
					have := map[string]bool{}
					for _, p := range params {
						m, ok := p.(map[string]any)
						if !ok || m["in"] != "path" {
							continue
						}
						if n, _ := m["name"].(string); n != "" {
							have[n] = true
						}
					}
					for _, n := range names {
						if have[n] {
							continue
						}
						params = append(params, map[string]any{
							"name": n, "in": "path", "required": true,
							"schema": map[string]any{"type": "string"},
						})
					}
					o["parameters"] = params
				}
				item[strings.ToLower(verb)] = o
			}
		}
	}

	// Every type the resource table names, closed over what those types refer to.
	// The roots come from the table rather than a list beside it, so a resource
	// cannot publish a shape the document does not carry.
	var roots []reflect.Type
	for _, r := range resources {
		if r.shape != nil {
			roots = append(roots, reflect.TypeOf(r.shape))
		}
	}
	schemas := components(roots)
	schemas["Envelope"] = envelope()

	return map[string]any{
		"openapi":    "3.1.0",
		"info":       map[string]any{"title": "Hanzo AI", "version": "v1"},
		"paths":      paths,
		"components": map[string]any{"schemas": schemas},
	}
}

// expand is the verbs one registration publishes.
//
// A route registered "*" is ONE handler answering every method, so the document
// has to choose which of them to name. It names the five a client calls. TRACE
// echoes the request back and is the Cross-Site Tracing vector; OPTIONS is CORS
// preflight, which a browser sends and a person never does — advertising either
// puts a method in every generated SDK, CLI and tool list that nobody should
// reach for.
func expand(registered string) []string {
	all := []string{"DELETE", "GET", "PATCH", "POST", "PUT"}
	if registered == "*" {
		return all
	}
	registered = strings.ToUpper(registered)
	for _, v := range all {
		if v == registered {
			return []string{registered}
		}
	}
	return nil
}

// envelope is the one body this service returns from its resource surface.
//
// `data` is left open HERE because the envelope belongs to the surface and the
// result belongs to the operation: what comes back from listing stores is not
// what comes back from reading one. Each operation narrows it — see [result] —
// so the two facts compose instead of one of them having to be vague.
func envelope() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"status", "msg"},
		"description": "The resource surface's response. `status` is the verdict, not the HTTP code: " +
			"a handled failure is still 200.",
		"properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []any{"ok", "error"}},
			"msg":    map[string]any{"type": "string", "description": "Empty on success, the reason on failure."},
			"data":   map[string]any{"description": "The operation's own result."},
			"data2":  map[string]any{"description": "A second result the operation defines, when it has one."},
		},
	}
}

// items are the Path Item Objects the resource table generates, keyed by path.
func items() map[string]any {
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
		coll, member := r.collection(), r.member()
		// What this resource IS, read off its own row. A row with no shape has not
		// said, and its operations publish the bare envelope as before.
		var list, one map[string]any
		if r.shape != nil {
			t := reflect.TypeOf(r.shape)
			list, one = listOf(t), oneOf(t)
		}

		if !r.noList {
			add(coll, "GET", op("List "+r.path,
				"List the caller's "+r.path+".", nil, false, list))
		}
		if !r.noCreate {
			add(coll, "POST", op("Create a "+singularNoun(r),
				"Create one "+singularNoun(r)+".", nil, true, one))
		}
		if r.global {
			add(coll+"/global", "GET", op("List "+r.path+" across tenants",
				"Cross-tenant listing. Admin-only; a tenant caller is refused.", nil, false, list))
		}

		mp := memberParams()
		if !r.noRead {
			add(member, "GET", op("Retrieve a "+singularNoun(r),
				"Read one "+singularNoun(r)+" by its (owner, name) key.", mp, false, one))
		}
		if !r.noUpdate {
			add(member, "PATCH", op("Update a "+singularNoun(r),
				"Update one "+singularNoun(r)+". PATCH and PUT reach the same handler, "+
					"which has always taken a whole object.", mp, true, one))
			add(member, "PUT", op("Replace a "+singularNoun(r),
				"Identical to PATCH — the handler takes a whole object either way.", mp, true, one))
		}
		if !r.noDelete {
			add(member, "DELETE", op("Delete a "+singularNoun(r),
				"Delete one "+singularNoun(r)+".", mp, false, one))
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
			add(p, verb, op(actionSummary(a.name, singularNoun(r)), "", params, body, nil))
		}
	}

	for _, s := range singletons {
		// Iterate a FIXED verb order, not the map: Go randomizes map order, and an
		// accessor whose output reorders between runs cannot be diffed for drift.
		for _, verb := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if _, ok := s.verbs[verb]; !ok {
				continue
			}
			body := verb == "POST" || verb == "PUT" || verb == "PATCH"
			add(s.url(), verb, op(actionSummary(s.path, ""), "", nil, body, nil))
		}
	}

	return out
}

// op builds one Operation Object.
//
// Every response is the SAME envelope: this surface answers {status,msg,data,data2}
// on success AND on a handled failure, so a 200 does not imply success — `status`
// does. That is a property of the backend, not a description choice, and saying so
// here is the difference between a spec a client can trust and one that quietly
// misleads.
//
// The refusals are written out rather than pointed at a shared Response Object,
// because a $ref binds this document to component definitions it does not carry:
// the two it used to name lived in a hand-authored spec, and when that spec was
// deleted every operation here kept pointing at nothing.
// The `data` argument is what THIS operation puts in the envelope's data field.
// Nil means the operation has not said — the envelope alone, which is what every
// operation used to publish.
func op(summary, description string, params []any, body bool, data map[string]any) map[string]any {
	schema := map[string]any{"$ref": "#/components/schemas/Envelope"}
	if data != nil {
		schema = result(data)
	}
	o := map[string]any{
		"summary": summary,
		"responses": map[string]any{
			"200": map[string]any{
				"description": "Envelope. `status` is \"ok\" or \"error\" — check it; a handled " +
					"failure is still HTTP 200.",
				"content": map[string]any{
					"application/json": map[string]any{"schema": schema},
				},
			},
			"401": map[string]any{"description": "No credential, or one this service does not accept."},
			"403": map[string]any{"description": "A valid credential that may not do this."},
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

// pathParams names the templated segments of an OpenAPI path. Rewriting the
// spelling is only half of what that comment above promises: a client also
// needs the parameter DECLARED, or the generator emits a method that cannot be
// given the value its own name asks for.
func pathParams(p string) []string {
	var names []string
	for _, s := range strings.Split(p, "/") {
		if len(s) > 2 && s[0] == '{' && s[len(s)-1] == '}' {
			names = append(names, s[1:len(s)-1])
		}
	}
	return names
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
