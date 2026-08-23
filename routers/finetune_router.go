// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package routers

import (
	"github.com/zap-proto/zip"
)

// init registers the /v1/ai/finetune/* fine-tuning broker routes. They live in their
// OWN file (not router.go) on purpose: Go runs every init() in the `routers`
// package before the router serves, so these registrations compose with the main route
// table additively — the finetune surface is added without editing the shared
// router.go. Verb:method pairs are semicolon-separated, exactly like the two-verb
// routes in router.go.
//
// These 8 paths / 9 operations are HTTP routes and stay HTTP routes. They are not
// registered for native ZAP dispatch (controllers/zap_registry.go), and that is a
// decision, not an oversight — writing it down so it does not read as a to-do:
//
// The body-only registry cannot carry them. registerGatewayPath keys on the path
// alone and hands the handler (ctx, auth, body) — no verb, no query. But
// /v1/ai/finetune/jobs answers GET (list) AND POST (create), so one registration
// necessarily answers both, silently, with whichever handler was passed; and
// seven of the nine ops carry their whole input in the query string (?id, ?name,
// ?q, ?baseModel …), which that handler never receives. The HTTP-shaped registry
// (registerGatewayRoute) does carry the request line and would fit — which is
// where the second reason bites.
//
// There is nothing here to make faster. Native dispatch buys one thing: skipping
// the HTTP machinery, which pays on hot inference paths. This is job CRUD behind
// a console poll. And it would be skipping the machinery on a surface that
// answers 401 anyway — every op authenticates with RequireSignedInUser, the
// console SESSION COOKIE, and no cookie crosses the gateway (serveGatewayViaRouter
// forwards Authorization, Content-Type and Accept, and stops there).
//
// What would reopen this: a fine-tuning client that carries an IAM bearer instead
// of a console session. The move then is RequirePrincipal on these handlers — one
// identity answer for both entry points — not a second registry entry. Until then
// the fallback dispatches them through this router with every filter intact, which
// is the right answer for a surface with one caller.
//
// controllers/zap_finetune_test.go holds both reasons as tests, so a conversion
// attempt goes red before it goes to the wire.
func registerFinetune(app *zip.App) {
	// Job lifecycle.
	route(app, "/v1/ai/finetune/jobs", "GET:ListFinetuneJobs;POST:CreateFinetuneJob")
	route(app, "/v1/ai/finetune/job", "GET:GetFinetuneJob")
	route(app, "/v1/ai/finetune/cancel", "POST:CancelFinetuneJob")
	route(app, "/v1/ai/finetune/deploy", "POST:DeployFinetuneJob")

	// Recommended defaults (the "Unsloth-class" presets brain).
	route(app, "/v1/ai/finetune/presets", "GET:GetFinetunePresets")

	// HuggingFace discovery (base-model + dataset pickers, private via KMS token).
	route(app, "/v1/ai/finetune/hf/models", "GET:SearchHfModels")
	route(app, "/v1/ai/finetune/hf/datasets", "GET:SearchHfDatasets")
	route(app, "/v1/ai/finetune/hf/repo", "GET:GetHfRepo")
}
