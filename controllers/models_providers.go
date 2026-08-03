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

// modelProviders is the secret-free response of GET /v1/models/providers: the
// distinct providers serving the catalog that GET /v1/models lists. Names only —
// no keys, no URLs, no config — so it is safe served unauthenticated, exactly
// like the model list it projects.
//
//	GET https://api.hanzo.ai/v1/models/providers
//	200 {"status":"ok","data":{"providers":["do-ai","enso","zen"]}}
//
// It is a SUB-RESOURCE of /v1/models on purpose. The fact "who serves this
// catalog" belongs to the catalog, and deriving it from the same source makes
// disagreement unrepresentable rather than merely unlikely.
//
// This replaces GET /v1/provider-flags, which answered the same question from a
// DIFFERENT source — the provider DB (Category=="Model" && State=="Active") —
// and therefore drifted from what was actually served: it named `zen` while zen's
// models were listed by family discovery rather than by any provider row, and it
// named `openai-direct` and `fireworks` while no listed route resolved to them.
// A provider being configured in the DB and a provider actually serving a listed
// model are different facts; only the second one answers "what can I call?".
//
// It is also not a feature flag, which is what the old name claimed. Feature
// flags are POST /v1/flags (hanzoai/cloud apps/flags) — a different subsystem
// with a different shape. Two unrelated things must not share a word.
type modelProviders struct {
	Providers []string `json:"providers"`
}

// GetModelProviders
// @Title GetModelProviders
// @Tag Model API
// @Description Public, secret-free list of the providers serving the models that
//
//	GET /v1/models lists — the same source, projected. Safe unauthenticated: no
//	keys, URLs, or config are returned, and it reports a SET of names, never
//	which provider serves which model.
//
// @Success 200 {object} controllers.modelProviders The Response object
// @router /models/providers [get]
func (c *ApiController) GetModelProviders() {
	c.ResponseOk(modelProviders{Providers: listRouteProviders()})
}
