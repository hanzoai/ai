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

package controllers

import (
	openai "github.com/hanzoai/go-openai"

	"github.com/hanzoai/ai/cluster"
	"github.com/hanzoai/ai/object"
)

// What each hand-written handler writes back.
//
// The resource table in routers/resources.go names the shape of every generated
// operation, and routers/shape.go reflects it into the published document. The
// hand-written half had no equivalent, so 91 operations — /v1/models,
// /v1/chat/completions, /v1/embeddings, every address a client actually calls —
// reached the fleet document with no response at all, and every generated SDK
// handed back an untyped bag.
//
// This is that missing half, and it lives HERE rather than beside the route table
// because most of these shapes are unexported types in this package: a table in
// routers could not name aiConnResponse or modelList at all. What it costs is the
// one accessor below; what it buys is that a shape is a Go VALUE, so the compiler
// refuses a type that has been renamed or removed, and answers_test.go refuses one
// that has quietly stopped matching what the handler passes to ResponseOk.
//
// A handler absent from the table answers something that has no JSON shape — a
// stream, an audio or video body, a redirect. Absent is the honest word for that;
// a guessed schema would be worse than none.

// Answer is what one handler writes back.
//
// Data distinguishes the surface's two dialects, which is a real difference and
// not a flag: an operation reached through ResponseOk answers the {status,msg,data}
// envelope with Shape in the data field, and an OpenAI- or Anthropic-compatible
// operation answers Shape and nothing around it.
type Answer struct {
	Shape any
	Data  bool
	// Refusals are the statuses the handler refuses with, each with the body it
	// carries. A handler that states none refuses with 401 and 403 and says no more.
	Refusals map[int]Refusal
	// Traced says every answer carries X-Request-Id.
	Traced bool
}

// Refusal is one status a handler refuses with: what it means to the caller, the
// body it carries, and whether it asks the caller to wait (Retry-After and
// Retry-After-Ms).
type Refusal struct {
	Says  string
	Shape any
	Wait  bool
}

// Answers is the whole table. routers joins it to the route that reaches each
// handler; nothing else reads it.
func Answers() map[string]Answer { return answers }

// data is an answer carried in the envelope's data field.
func data(v any) Answer { return Answer{Shape: v, Data: true} }

// whole is an answer that IS the body.
func whole(v any) Answer { return Answer{Shape: v} }

var answers = map[string]Answer{
	// The routing catalog editor (routing.go), in the envelope.
	"RouterCatalogRead":     data([]routingView{}),
	"RouterCatalogApply":    data(object.RoutingVersion{}),
	"RouterCatalogPropose":  data(routingProposal{}),
	"RouterCatalogRollback": data(object.RoutingVersion{}),
	"RouterCatalogTest":     data(routingProbe{}),
	// The OpenAI-compatible surface. These proxy an upstream or rebuild its
	// answer, so the shape is that wire format — our own fork of the Go types
	// every client of it already holds.
	"ChatCompletions":           whole(openai.ChatCompletionResponse{}),
	"ChatCompletionsPublic":     whole(openai.ChatCompletionResponse{}),
	"Embeddings":                whole(openai.EmbeddingResponse{}),
	"ImagesGenerations":         whole(openai.ImageResponse{}),
	"AudioTranscriptions":       whole(openai.AudioResponse{}),
	"AudioTranscriptionsPublic": whole(openai.AudioResponse{}),
	"ListModels":                whole(modelList{}),
	"Rerank":                    whole(ranking{}),
	"Decisions":                 {Shape: decisionsResponse{}, Refusals: decisionRefusals(), Traced: true},
	"VideosGenerations":         whole(videoStatus{}),
	"RetrieveVideo":             whole(videoStatus{}),

	"Responses": whole(responsesResource{}),

	// The Anthropic-compatible surface.
	"AnthropicMessages":    whole(AnthropicResponse{}),
	"AnthropicCountTokens": whole(tokenCount{}),

	// RAG answers a bare array, matching the retired rag-api it replaced.
	"RagQuery":         whole([]object.DocSearchResult{}),
	"RagQueryMultiple": whole([]object.DocSearchResult{}),
	"RagContext":       whole([]object.DocSearchResult{}),

	// The router-config nouns come back over the ZAP gateway, which answers this
	// service's own envelope — so the bridge writes the envelope, not a payload
	// inside one.
	"RouterConfigBridge": whole(Response{}),

	// Everything below answers through ResponseOk.
	"GetAIConnections":     data([]aiConnResponse{}),
	"AddAIConnection":      data(aiConnResponse{}),
	"DeleteAIConnection":   data(aiConnResponse{}),
	"ConnectAIProvider":    data(map[string]string{}),
	"GetAIConnectionUsage": data(ProviderUsage{}),

	"CreateFinetuneJob":  data(&object.FinetuneJob{}),
	"GetFinetuneJob":     data(&object.FinetuneJob{}),
	"ListFinetuneJobs":   data([]*object.FinetuneJob{}),
	"CancelFinetuneJob":  data(&object.FinetuneJob{}),
	"DeployFinetuneJob":  data(&cluster.Serving{}),
	"GetFinetunePresets": data(map[string]any{}),
	"GetHfRepo":          data(&object.HfRepoInfo{}),
	"SearchHfModels":     data([]*object.HfModel{}),
	"SearchHfDatasets":   data([]*object.HfDataset{}),

	"MemoryRemember": data(&object.Memory{}),
	"MemoryList":     data([]*object.Memory{}),
	"MemoryFacts":    data([]*object.Memory{}),
	"MemoryRecall":   data([]*object.Memory{}),
	"MemorySearch":   data([]*object.Memory{}),
	"MemoryUpdate":   data(false),
	"MemoryDelete":   data(false),

	"IngestDocs": data(&object.IngestStats{}),
	"RagEmbed":   data(&object.RagEmbedResult{}),
	"RagDelete":  data(map[string]any{}),

	"GetModelProviders":    data(modelProviders{}),
	"GetModelAccessStatus": data(map[string]string{}),
	"RequestModelAccess":   data(&object.ModelAccess{}),

	"AdminListModelAccess":    data([]*object.ModelAccess{}),
	"AdminGrantModelAccess":   data(&object.ModelAccess{}),
	"GetAdminProviders":       data([]adminProviderView{}),
	"SetPrimaryAdminProvider": data([]adminProviderView{}),
	"ToggleAdminProvider":     data(adminProviderView{}),
	"RefreshModelPricing":     data(pricingRefresh{}),
	"PostBackfillDOUsage":     data(DOBackfillPlan{}),

	"SearchDocs":      whole(docHits{}),
	"SearchDocsStats": data(&object.DocStatsResponse{}),
	"IndexDocs":       data(0),

	// ResponseOk with no argument: the envelope says it worked and carries nothing.
	"ReloadModelConfig": whole(Response{}),

	"GetRouterStats":      data(routerStats{}),
	"GetRouterHistory":    data(routerHistory{}),
	"GetRouterJudgePanel": data(judgePanelState{}),
	"AddRoutingReward":    data(routingRewardResult{}),
	"GetTrafficGlobe":     data(object.TrafficGlobe{}),
}

// decisionRefusals are the refusals /v1/decisions answers with, each in the
// service's error shape, {"error":{"code","message"}}.
func decisionRefusals() map[int]Refusal {
	says := map[int]string{
		400: "Malformed JSON, or a model this path does not serve.",
		401: "No credential, or one this service does not accept.",
		402: "The balance cannot cover the call. Retry-After is when a top-up is read.",
		403: "A credential whose kind may not call this, such as a publishable (pk-) key.",
		422: "A question that is not valid, a state beyond what the checkpoint reads (code state_too_long), or a body past the bound both the gateway and the service hold (code request_too_long).",
		429: "Rate limited, or the queue is full.",
		502: "The decision service failed.",
		503: "The model is known and not served.",
		529: "Overloaded.",
	}
	out := make(map[int]Refusal, len(says))
	for status, s := range says {
		_, wait := pause(status)
		out[status] = Refusal{Says: s, Shape: decisionsRefused{}, Wait: wait}
	}
	return out
}
