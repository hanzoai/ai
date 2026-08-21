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
	"reflect"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/controllers"
)

// How a path becomes a handler. One function, and this is the only one.
//
// Every route in this package — the hand-written ones below and the CRUD set
// generated from the table in resources.go — arrives here as a path and a mapping
// string: "GET:ListStores", or "GET:GetChat;POST:UpdateChat" for a path that
// serves two verbs. The mapping is kept as a string because it is the ONE place a
// verb is bound to a handler; splitting it into per-verb calls would be the same
// table written twice, in two orders, for someone to reconcile later.

// verbs is the registrar for each verb a mapping may name. "*" is any verb, which
// is what a method-aware handler asks for: /v1/ai/router/policy splits GET from PUT
// itself and answers 405 for a verb it does not own.
func verbs(app *zip.App) map[string]func(string, ...zip.Handler) zip.Router {
	return map[string]func(string, ...zip.Handler) zip.Router{
		"GET":    app.Get,
		"POST":   app.Post,
		"PUT":    app.Put,
		"PATCH":  app.Patch,
		"DELETE": app.Delete,
		"*":      app.All,
	}
}

// route binds one path's verbs to controller methods.
//
// It PANICS on a mapping it cannot honour — an unknown verb, a missing colon, a
// method no controller has. That is deliberate and it is the whole reason the
// method is resolved here rather than per request: a typo in this table used to
// mean a 500 the first time a customer called that address, discovered in
// production by the customer. Now the binary refuses to start.
func route(app *zip.App, path, mapping string) {
	add := verbs(app)
	for _, pair := range strings.Split(mapping, ";") {
		verb, name, ok := strings.Cut(pair, ":")
		if !ok {
			panic(fmt.Sprintf("routers: %s: mapping %q is not VERB:Method", path, pair))
		}
		verb = strings.ToUpper(strings.TrimSpace(verb))
		register, known := add[verb]
		if !known {
			panic(fmt.Sprintf("routers: %s: unknown verb %q", path, verb))
		}
		register(path, serve(strings.TrimSpace(name)))
	}
}

// serve turns a controller method NAME into a handler.
//
// The name is resolved ONCE, at registration, and the reflected method is what the
// handler closes over — so the per-request cost is a call, not a lookup, and an
// unknown name cannot reach a request at all.
//
// A fresh controller per request, holding that request's context. It has to be
// fresh: the controller IS the request's state (the resolved principal, the org,
// the writer it answers through), so one shared instance would hand two callers
// each other's identity.
func serve(name string) zip.Handler {
	method, ok := reflect.TypeOf(&controllers.ApiController{}).MethodByName(name)
	if !ok {
		panic("routers: no controller method named " + name)
	}
	return func(c *zip.Ctx) error {
		method.Func.Call([]reflect.Value{
			reflect.ValueOf(&controllers.ApiController{Ctx: c}),
		})
		return nil
	}
}

// Patterns is the live route table as path → verbs.
//
// zip declares routes as a flat list of (method, pattern); this is the same
// information keyed the way its callers ask it — "what does this address serve" —
// which is the question both the OpenAPI document and the filter-path test have. It
// is derived, never maintained, so it cannot claim a verb the app does not serve.
func Patterns(app *zip.App) map[string][]string {
	out := map[string][]string{}
	for _, r := range app.Routes() {
		out[r.Pattern] = append(out[r.Pattern], r.Method)
	}
	return out
}
