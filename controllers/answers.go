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
}

// Answers is the whole table. routers joins it to the route that reaches each
// handler; nothing else reads it.
func Answers() map[string]Answer { return answers }

// data is an answer carried in the envelope's data field.
func data(v any) Answer { return Answer{Shape: v, Data: true} }

// whole is an answer that IS the body.
func whole(v any) Answer { return Answer{Shape: v} }

var answers = map[string]Answer{
	// The OpenAI-compatible surface. These proxy an upstream or rebuild its
	// answer, so the shape is that wire format — our own fork of the Go types
	// every client of it already holds.
	"ChatCompletions":       whole(openai.ChatCompletionResponse{}),
	"ChatCompletionsPublic": whole(openai.ChatCompletionResponse{}),
	"Embeddings":            whole(openai.EmbeddingResponse{}),
	"ImagesGenerations":     whole(openai.ImageResponse{}),
	"AudioTranscriptions":   whole(openai.AudioResponse{}),
	"ListModels":            whole(modelList{}),
	"Rerank":                whole(ranking{}),
	"VideosGenerations":     whole(videoStatus{}),
	"RetrieveVideo":         whole(videoStatus{}),

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
