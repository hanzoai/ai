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

import "strings"

//go:generate go run ../cmd/routerdoc -C ..

// THE SENTENCE FOR EVERY ROUTE THIS SERVICE SERVES, from the handler that answers
// it.
//
// This surface reaches api.hanzo.ai through ONE `app.All("/v1/*")` in the host, so
// reading the host's router alone published `/v1/{wildcard1}` and seven operations
// for the whole model API — no generated SDK carried chat completions, no MCP tool
// list carried one, and the CLI grew a `{wildcard1}` command because that is what
// the document named. The host can ask what is behind its own door; what it needs
// back is what every published operation owes a reader: the address, the verb, and
// a sentence.
//
// [App.Patterns] already answers the first two, from the live route table. This
// answers the third, and it answers it from the handler's own doc comment — lifted
// at build time by cmd/routerdoc, because Go drops comments at compile time and a
// sentence written anywhere else is a second source for one fact. Between them
// there is nothing left to hand-copy: a route added without a sentence fails the
// generate step, and a sentence written for a route that does not exist renders
// nowhere.
type wire struct {
	Path, Method, Handler string
	Summary, Description  string
}

// Doc is one operation's prose: the opening sentence, and the whole comment.
type Doc struct{ Summary, Description string }

// Prose is the sentence for every route App registers, keyed "METHOD /path" with
// the path in OpenAPI's `{name}` spelling.
//
// The key carries the verb AS REGISTERED, "*" included: a route mapped "*" is one
// handler answering every method, so every method a projection chooses to publish
// there inherits that one handler's sentence. Expanding the star here would be
// this package deciding which verbs somebody else's document lists, which is not
// its question.
//
// Both halves of the surface are here because both are described at their own
// source and neither is described twice: the resource half by routers/openapi.go,
// from the same table in resources.go that registers it, and the hand-written half
// by the doc comment on the handler.
func Prose() map[string]Doc {
	out := map[string]Doc{}
	for path, item := range OpenAPIPaths() {
		ops, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, o := range ops {
			op, ok := o.(map[string]any)
			if !ok {
				continue
			}
			text, _ := op["summary"].(string)
			desc, _ := op["description"].(string)
			out[strings.ToUpper(method)+" "+path] = Doc{Summary: text, Description: desc}
		}
	}
	for _, w := range wired {
		out[w.Method+" "+openAPIPath(w.Path)] = Doc{Summary: w.Summary, Description: w.Description}
	}
	return out
}
