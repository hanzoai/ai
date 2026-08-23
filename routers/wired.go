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

//go:generate go run ../cmd/routerdoc -C ..

// THE SENTENCE FOR EVERY ROUTE THIS SERVICE SERVES, from the handler that answers
// it.
//
// This surface reaches api.hanzo.ai through ONE `app.All("/v1/*")` in the host, so
// reading the host's router alone published `/v1/{wildcard1}` and seven operations
// for the whole model API — no generated SDK carried chat completions, no MCP tool
// list carried one, and the CLI grew a `{wildcard1}` command because that is what
// the document named. The host can ask what is behind its own entry point; what it
// needs back is what every published operation owes a reader: the address, the verb,
// and a sentence.
//
// [App.Patterns] answers the first two, from the live route table. This answers
// the third for the hand-written half of the surface, and it answers it from the
// handler's own doc comment — lifted at build time by cmd/routerdoc, because Go
// drops comments at compile time and a sentence written anywhere else is a second
// source for one fact. The generated half is described by routers/openapi.go, from
// the same table that registers it. [Document] joins them.
//
// The method is recorded AS REGISTERED, "*" included: a route mapped "*" is one
// handler answering every method, so every method the document names there
// inherits that one handler's sentence.
type wire struct {
	Path, Method, Handler string
	Summary, Description  string
}

// Doc is one operation's prose: the opening sentence, and the whole comment.
type Doc struct{ Summary, Description string }
