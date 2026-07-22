// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

// Package routers
// @APIVersion 1.70.0
// @Title Hanzo Cloud RESTful API
// @Description Swagger Docs of Hanzo Cloud Backend API
// @Contact cloud@hanzo.ai
// @SecurityDefinition AccessToken apiKey Authorization header
// @Schemes https,http
// @ExternalDocs Find out more about Hanzo Cloud
// @ExternalDocsUrl https://hanzo.ai/cloud
package routers

import (
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/web"
)

// App is the ai runtime's HTTP router: every /v1 route registers on it here,
// the filter chain is inserted at composition time, and the runtime serves it
// directly. Routes are explicit; there is no annotation-based registration.
var App = web.NewRouter()

func init() {
	initAPI()
}

func initAPI() {
	App.Router("/v1/signin", &controllers.ApiController{}, "POST:Signin")
	App.Router("/v1/signout", &controllers.ApiController{}, "POST:Signout")
	App.Router("/v1/get-account", &controllers.ApiController{}, "GET:GetAccount")
	App.Router("/v1/update-preferences", &controllers.ApiController{}, "POST:UpdatePreferences")

	App.Router("/v1/get-global-videos", &controllers.ApiController{}, "GET:GetGlobalVideos")
	App.Router("/v1/get-videos", &controllers.ApiController{}, "GET:GetVideos")
	App.Router("/v1/get-video", &controllers.ApiController{}, "GET:GetVideo")
	App.Router("/v1/update-video", &controllers.ApiController{}, "POST:UpdateVideo")
	App.Router("/v1/add-video", &controllers.ApiController{}, "POST:AddVideo")
	App.Router("/v1/delete-video", &controllers.ApiController{}, "POST:DeleteVideo")
	App.Router("/v1/upload-video", &controllers.ApiController{}, "POST:UploadVideo")

	App.Router("/v1/get-global-stores", &controllers.ApiController{}, "GET:GetGlobalStores")
	App.Router("/v1/get-stores", &controllers.ApiController{}, "GET:GetStores")
	App.Router("/v1/get-store", &controllers.ApiController{}, "GET:GetStore")
	App.Router("/v1/update-store", &controllers.ApiController{}, "POST:UpdateStore")
	App.Router("/v1/add-store", &controllers.ApiController{}, "POST:AddStore")
	App.Router("/v1/delete-store", &controllers.ApiController{}, "POST:DeleteStore")
	App.Router("/v1/refresh-store-vectors", &controllers.ApiController{}, "POST:RefreshStoreVectors")
	App.Router("/v1/get-storage-providers", &controllers.ApiController{}, "GET:GetStorageProviders")
	App.Router("/v1/get-store-names", &controllers.ApiController{}, "GET:GetStoreNames")

	App.Router("/v1/get-global-providers", &controllers.ApiController{}, "GET:GetGlobalProviders")
	App.Router("/v1/get-providers", &controllers.ApiController{}, "GET:GetProviders")
	App.Router("/v1/get-provider", &controllers.ApiController{}, "GET:GetProvider")
	App.Router("/v1/update-provider", &controllers.ApiController{}, "POST:UpdateProvider")
	App.Router("/v1/add-provider", &controllers.ApiController{}, "POST:AddProvider")
	App.Router("/v1/delete-provider", &controllers.ApiController{}, "POST:DeleteProvider")
	App.Router("/v1/refresh-mcp-tools", &controllers.ApiController{}, "POST:RefreshMcpTools")

	// Provider-admin management surface (super-admin gated in authz_filter.go).
	// Reads/writes the SAME object.Provider records as the CRUD routes above.
	App.Router("/v1/admin/providers", &controllers.ApiController{}, "GET:GetAdminProviders")
	App.Router("/v1/admin/providers/toggle", &controllers.ApiController{}, "POST:ToggleAdminProvider")
	App.Router("/v1/admin/providers/primary", &controllers.ApiController{}, "POST:SetPrimaryAdminProvider")
	// Public, secret-free enabled-provider-name feed for the pricing catalog sync.
	App.Router("/v1/provider-flags", &controllers.ApiController{}, "GET:GetProviderFlags")

	// AI login-manager: org-scoped connections to third-party AI accounts. Curated
	// surface over object.Provider — authenticated org user (NOT super-admin);
	// keys are sealed into KMS, never returned. See controllers/connections_api.go.
	App.Router("/v1/ai/connections", &controllers.ApiController{}, "GET:GetAIConnections;POST:AddAIConnection")
	App.Router("/v1/ai/connections/:provider", &controllers.ApiController{}, "DELETE:DeleteAIConnection;POST:DeleteAIConnection")
	// OAuth "connect your provider login": authorize sends the org (from the
	// session) to the provider; callback seals the returned token into KMS exactly
	// like the BYOK path. See controllers/connections_oauth.go.
	App.Router("/v1/ai/connections/:provider/authorize", &controllers.ApiController{}, "GET:ConnectAIProvider")
	App.Router("/v1/ai/connections/:provider/callback", &controllers.ApiController{}, "GET:CallbackAIProvider")
	// Import a connected account's usage: unseal the org's key server-side, call the
	// provider's own usage/cost API, normalize to ProviderUsage. See
	// controllers/connections_usage.go.
	App.Router("/v1/ai/connections/:provider/usage", &controllers.ApiController{}, "GET:GetAIConnectionUsage")

	App.Router("/v1/get-global-files", &controllers.ApiController{}, "GET:GetGlobalFiles")
	App.Router("/v1/get-files", &controllers.ApiController{}, "GET:GetFiles")
	App.Router("/v1/get-file", &controllers.ApiController{}, "GET:GetFileMy")
	App.Router("/v1/update-file", &controllers.ApiController{}, "POST:UpdateFile")
	App.Router("/v1/add-file", &controllers.ApiController{}, "POST:AddFile")
	App.Router("/v1/delete-file", &controllers.ApiController{}, "POST:DeleteFile")
	App.Router("/v1/refresh-file-vectors", &controllers.ApiController{}, "POST:RefreshFileVectors")
	// Unified RAG ingest (upload | github | crawl | s3) → parse + chunk + embed into
	// {owner}-{store}-docs (Hanzo Vector + Search). Handler exists (docs_ingest.go);
	// this is its only route — the swagger @router annotation alone does not register it.
	App.Router("/v1/docs/ingest", &controllers.ApiController{}, "POST:IngestDocs")

	App.Router("/v1/get-global-vectors", &controllers.ApiController{}, "GET:GetGlobalVectors")
	App.Router("/v1/get-vectors", &controllers.ApiController{}, "GET:GetVectors")
	App.Router("/v1/get-vector", &controllers.ApiController{}, "GET:GetVector")
	App.Router("/v1/update-vector", &controllers.ApiController{}, "POST:UpdateVector")
	App.Router("/v1/add-vector", &controllers.ApiController{}, "POST:AddVector")
	App.Router("/v1/delete-vector", &controllers.ApiController{}, "POST:DeleteVector")
	App.Router("/v1/delete-all-vectors", &controllers.ApiController{}, "POST:DeleteAllVectors")

	App.Router("/v1/generate-text-to-speech-audio", &controllers.ApiController{}, "POST:GenerateTextToSpeechAudio")
	App.Router("/v1/generate-text-to-speech-audio-stream", &controllers.ApiController{}, "GET:GenerateTextToSpeechAudioStream")
	App.Router("/v1/process-speech-to-text", &controllers.ApiController{}, "POST:ProcessSpeechToText")

	App.Router("/v1/get-global-chats", &controllers.ApiController{}, "GET:GetGlobalChats")
	App.Router("/v1/get-chats", &controllers.ApiController{}, "GET:GetChats")
	App.Router("/v1/get-chat", &controllers.ApiController{}, "GET:GetChat")
	App.Router("/v1/update-chat", &controllers.ApiController{}, "POST:UpdateChat")
	App.Router("/v1/add-chat", &controllers.ApiController{}, "POST:AddChat")
	App.Router("/v1/delete-chat", &controllers.ApiController{}, "POST:DeleteChat")

	App.Router("/v1/get-global-messages", &controllers.ApiController{}, "GET:GetGlobalMessages")
	App.Router("/v1/get-messages", &controllers.ApiController{}, "GET:GetMessages")
	App.Router("/v1/get-message", &controllers.ApiController{}, "GET:GetMessage")
	App.Router("/v1/get-message-answer", &controllers.ApiController{}, "GET:GetMessageAnswer")
	App.Router("/v1/get-answer", &controllers.ApiController{}, "GET:GetAnswer")
	App.Router("/v1/update-message", &controllers.ApiController{}, "POST:UpdateMessage")
	App.Router("/v1/add-message", &controllers.ApiController{}, "POST:AddMessage")
	App.Router("/v1/delete-message", &controllers.ApiController{}, "POST:DeleteMessage")
	App.Router("/v1/delete-welcome-message", &controllers.ApiController{}, "POST:DeleteWelcomeMessage")

	App.Router("/v1/get-global-graphs", &controllers.ApiController{}, "GET:GetGlobalGraphs")
	App.Router("/v1/get-graphs", &controllers.ApiController{}, "GET:GetGraphs")
	App.Router("/v1/get-graph", &controllers.ApiController{}, "GET:GetGraph")
	App.Router("/v1/update-graph", &controllers.ApiController{}, "POST:UpdateGraph")
	App.Router("/v1/add-graph", &controllers.ApiController{}, "POST:AddGraph")
	App.Router("/v1/delete-graph", &controllers.ApiController{}, "POST:DeleteGraph")

	App.Router("/v1/get-templates", &controllers.ApiController{}, "GET:GetTemplates")
	App.Router("/v1/get-template", &controllers.ApiController{}, "GET:GetTemplate")
	App.Router("/v1/update-template", &controllers.ApiController{}, "POST:UpdateTemplate")
	App.Router("/v1/add-template", &controllers.ApiController{}, "POST:AddTemplate")
	App.Router("/v1/delete-template", &controllers.ApiController{}, "POST:DeleteTemplate")
	App.Router("/v1/get-k8s-status", &controllers.ApiController{}, "GET:GetK8sStatus")

	App.Router("/v1/get-applications", &controllers.ApiController{}, "GET:GetApplications")
	App.Router("/v1/get-application", &controllers.ApiController{}, "GET:GetApplication")
	App.Router("/v1/update-application", &controllers.ApiController{}, "POST:UpdateApplication")
	App.Router("/v1/add-application", &controllers.ApiController{}, "POST:AddApplication")
	App.Router("/v1/delete-application", &controllers.ApiController{}, "POST:DeleteApplication")

	App.Router("/v1/deploy-application", &controllers.ApiController{}, "POST:DeployApplication")
	App.Router("/v1/undeploy-application", &controllers.ApiController{}, "POST:UndeployApplication")

	App.Router("/v1/get-usages", &controllers.ApiController{}, "GET:GetUsages")
	App.Router("/v1/get-range-usages", &controllers.ApiController{}, "GET:GetRangeUsages")
	App.Router("/v1/get-users", &controllers.ApiController{}, "GET:GetUsers")
	App.Router("/v1/get-user-table-infos", &controllers.ApiController{}, "GET:GetUserTableInfos")
	App.Router("/v1/get-cloud-usages", &controllers.ApiController{}, "GET:GetCloudUsages")
	// Super-admin (authz_filter.go superAdminEndpoints): backfill the usage ledger
	// from DigitalOcean billing for windows native metering missed. Dry-run by default.
	App.Router("/v1/admin/usage/backfill-do", &controllers.ApiController{}, "POST:PostBackfillDOUsage")

	App.Router("/v1/get-activities", &controllers.ApiController{}, "GET:GetActivities")
	// App.Router("/v1/get-range-activities", &controllers.ApiController{}, "GET:GetRangeActivities")

	App.Router("/v1/get-global-workflows", &controllers.ApiController{}, "GET:GetGlobalWorkflows")
	App.Router("/v1/get-workflows", &controllers.ApiController{}, "GET:GetWorkflows")
	App.Router("/v1/get-workflow", &controllers.ApiController{}, "GET:GetWorkflow")
	App.Router("/v1/update-workflow", &controllers.ApiController{}, "POST:UpdateWorkflow")
	App.Router("/v1/add-workflow", &controllers.ApiController{}, "POST:AddWorkflow")
	App.Router("/v1/delete-workflow", &controllers.ApiController{}, "POST:DeleteWorkflow")

	App.Router("/v1/get-global-tasks", &controllers.ApiController{}, "GET:GetGlobalTasks")
	App.Router("/v1/get-tasks", &controllers.ApiController{}, "GET:GetTasks")
	App.Router("/v1/get-task", &controllers.ApiController{}, "GET:GetTask")
	App.Router("/v1/update-task", &controllers.ApiController{}, "POST:UpdateTask")
	App.Router("/v1/add-task", &controllers.ApiController{}, "POST:AddTask")
	App.Router("/v1/delete-task", &controllers.ApiController{}, "POST:DeleteTask")
	App.Router("/v1/upload-task-document", &controllers.ApiController{}, "POST:UploadTaskDocument")
	App.Router("/v1/analyze-task", &controllers.ApiController{}, "POST:AnalyzeTask")

	App.Router("/v1/get-global-scales", &controllers.ApiController{}, "GET:GetGlobalScales")
	App.Router("/v1/get-scales", &controllers.ApiController{}, "GET:GetScales")
	App.Router("/v1/get-scale", &controllers.ApiController{}, "GET:GetScale")
	App.Router("/v1/get-public-scales", &controllers.ApiController{}, "GET:GetPublicScales")
	App.Router("/v1/update-scale", &controllers.ApiController{}, "POST:UpdateScale")
	App.Router("/v1/add-scale", &controllers.ApiController{}, "POST:AddScale")
	App.Router("/v1/delete-scale", &controllers.ApiController{}, "POST:DeleteScale")

	App.Router("/v1/get-global-forms", &controllers.ApiController{}, "GET:GetGlobalForms")
	App.Router("/v1/get-forms", &controllers.ApiController{}, "GET:GetForms")
	App.Router("/v1/get-form", &controllers.ApiController{}, "GET:GetForm")
	App.Router("/v1/update-form", &controllers.ApiController{}, "POST:UpdateForm")
	App.Router("/v1/add-form", &controllers.ApiController{}, "POST:AddForm")
	App.Router("/v1/delete-form", &controllers.ApiController{}, "POST:DeleteForm")

	App.Router("/v1/get-form-data", &controllers.ApiController{}, "GET:GetFormData")

	App.Router("/v1/get-global-articles", &controllers.ApiController{}, "GET:GetGlobalArticles")
	App.Router("/v1/get-articles", &controllers.ApiController{}, "GET:GetArticles")
	App.Router("/v1/get-article", &controllers.ApiController{}, "GET:GetArticle")
	App.Router("/v1/update-article", &controllers.ApiController{}, "POST:UpdateArticle")
	App.Router("/v1/add-article", &controllers.ApiController{}, "POST:AddArticle")
	App.Router("/v1/delete-article", &controllers.ApiController{}, "POST:DeleteArticle")

	App.Router("/v1/update-tree-file", &controllers.ApiController{}, "POST:UpdateTreeFile")
	App.Router("/v1/add-tree-file", &controllers.ApiController{}, "POST:AddTreeFile")
	App.Router("/v1/delete-tree-file", &controllers.ApiController{}, "POST:DeleteTreeFile")
	App.Router("/v1/activate-file", &controllers.ApiController{}, "POST:ActivateFile")
	App.Router("/v1/get-active-file", &controllers.ApiController{}, "GET:GetActiveFile")

	App.Router("/v1/upload-file", &controllers.ApiController{}, "POST:UploadFile")

	App.Router("/v1/get-permissions", &controllers.ApiController{}, "GET:GetPermissions")
	App.Router("/v1/get-permission", &controllers.ApiController{}, "GET:GetPermission")
	App.Router("/v1/update-permission", &controllers.ApiController{}, "POST:UpdatePermission")
	App.Router("/v1/add-permission", &controllers.ApiController{}, "POST:AddPermission")
	App.Router("/v1/delete-permission", &controllers.ApiController{}, "POST:DeletePermission")

	App.Router("/v1/get-nodes", &controllers.ApiController{}, "GET:GetNodes")
	App.Router("/v1/get-node", &controllers.ApiController{}, "GET:GetNode")
	App.Router("/v1/update-node", &controllers.ApiController{}, "POST:UpdateNode")
	App.Router("/v1/add-node", &controllers.ApiController{}, "POST:AddNode")
	App.Router("/v1/delete-node", &controllers.ApiController{}, "POST:DeleteNode")

	App.Router("/v1/get-machines", &controllers.ApiController{}, "GET:GetMachines")
	App.Router("/v1/get-machine", &controllers.ApiController{}, "GET:GetMachine")
	App.Router("/v1/update-machine", &controllers.ApiController{}, "POST:UpdateMachine")
	App.Router("/v1/add-machine", &controllers.ApiController{}, "POST:AddMachine")
	App.Router("/v1/delete-machine", &controllers.ApiController{}, "POST:DeleteMachine")

	App.Router("/v1/get-assets", &controllers.ApiController{}, "GET:GetAssets")
	App.Router("/v1/get-asset", &controllers.ApiController{}, "GET:GetAsset")
	App.Router("/v1/update-asset", &controllers.ApiController{}, "POST:UpdateAsset")
	App.Router("/v1/add-asset", &controllers.ApiController{}, "POST:AddAsset")
	App.Router("/v1/delete-asset", &controllers.ApiController{}, "POST:DeleteAsset")
	App.Router("/v1/scan-asset", &controllers.ApiController{}, "POST:ScanAsset")
	App.Router("/v1/scan-assets", &controllers.ApiController{}, "POST:ScanAssets")

	App.Router("/v1/get-scans", &controllers.ApiController{}, "GET:GetScans")
	App.Router("/v1/get-scan", &controllers.ApiController{}, "GET:GetScan")
	App.Router("/v1/update-scan", &controllers.ApiController{}, "POST:UpdateScan")
	App.Router("/v1/add-scan", &controllers.ApiController{}, "POST:AddScan")
	App.Router("/v1/delete-scan", &controllers.ApiController{}, "POST:DeleteScan")

	App.Router("/v1/install-patch", &controllers.ApiController{}, "POST:InstallPatch")

	App.Router("/v1/get-images", &controllers.ApiController{}, "GET:GetImages")
	App.Router("/v1/get-image", &controllers.ApiController{}, "GET:GetImage")
	App.Router("/v1/update-image", &controllers.ApiController{}, "POST:UpdateImage")
	App.Router("/v1/add-image", &controllers.ApiController{}, "POST:AddImage")
	App.Router("/v1/delete-image", &controllers.ApiController{}, "POST:DeleteImage")

	App.Router("/v1/get-containers", &controllers.ApiController{}, "GET:GetContainers")
	App.Router("/v1/get-container", &controllers.ApiController{}, "GET:GetContainer")
	App.Router("/v1/update-container", &controllers.ApiController{}, "POST:UpdateContainer")
	App.Router("/v1/add-container", &controllers.ApiController{}, "POST:AddContainer")
	App.Router("/v1/delete-container", &controllers.ApiController{}, "POST:DeleteContainer")

	App.Router("/v1/get-pods", &controllers.ApiController{}, "GET:GetPods")
	App.Router("/v1/get-pod", &controllers.ApiController{}, "GET:GetPod")
	App.Router("/v1/update-pod", &controllers.ApiController{}, "POST:UpdatePod")
	App.Router("/v1/add-pod", &controllers.ApiController{}, "POST:AddPod")
	App.Router("/v1/delete-pod", &controllers.ApiController{}, "POST:DeletePod")

	App.Router("/v1/add-node-tunnel", &controllers.ApiController{}, "POST:AddNodeTunnel")
	App.Router("/v1/get-node-tunnel", &controllers.ApiController{}, "GET:GetNodeTunnel")
	App.Router("/v1/dev-bridge", &controllers.ApiController{}, "GET:DevBridge")

	App.Router("/v1/get-sessions", &controllers.ApiController{}, "GET:GetSessions")
	App.Router("/v1/get-session", &controllers.ApiController{}, "GET:GetSession")
	App.Router("/v1/update-session", &controllers.ApiController{}, "POST:UpdateSession")
	App.Router("/v1/add-session", &controllers.ApiController{}, "POST:AddSession")
	App.Router("/v1/delete-session", &controllers.ApiController{}, "POST:DeleteSession")
	App.Router("/v1/is-session-duplicated", &controllers.ApiController{}, "GET:IsSessionDuplicated")

	App.Router("/v1/get-connections", &controllers.ApiController{}, "GET:GetConnections")
	App.Router("/v1/get-connection", &controllers.ApiController{}, "GET:GetConnection")
	App.Router("/v1/update-connection", &controllers.ApiController{}, "POST:UpdateConnection")
	App.Router("/v1/add-connection", &controllers.ApiController{}, "POST:AddConnection")
	App.Router("/v1/delete-connection", &controllers.ApiController{}, "POST:DeleteConnection")
	App.Router("/v1/start-connection", &controllers.ApiController{}, "POST:StartConnection")
	App.Router("/v1/stop-connection", &controllers.ApiController{}, "POST:StopConnection")

	App.Router("/v1/get-records", &controllers.ApiController{}, "GET:GetRecords")
	App.Router("/v1/get-record", &controllers.ApiController{}, "GET:GetRecord")
	App.Router("/v1/update-record", &controllers.ApiController{}, "POST:UpdateRecord")
	App.Router("/v1/add-record", &controllers.ApiController{}, "POST:AddRecord")
	App.Router("/v1/add-records", &controllers.ApiController{}, "POST:AddRecords")
	App.Router("/v1/delete-record", &controllers.ApiController{}, "POST:DeleteRecord")

	App.Router("/v1/commit-record", &controllers.ApiController{}, "POST:CommitRecord")
	App.Router("/v1/commit-record-second", &controllers.ApiController{}, "POST:CommitRecordSecond")
	App.Router("/v1/query-record", &controllers.ApiController{}, "GET:QueryRecord")
	App.Router("/v1/query-record-second", &controllers.ApiController{}, "GET:QueryRecordSecond")

	App.Router("/v1/get-hospitals", &controllers.ApiController{}, "GET:GetHospitals")
	App.Router("/v1/get-hospital", &controllers.ApiController{}, "GET:GetHospital")
	App.Router("/v1/update-hospital", &controllers.ApiController{}, "POST:UpdateHospital")
	App.Router("/v1/add-hospital", &controllers.ApiController{}, "POST:AddHospital")
	App.Router("/v1/delete-hospital", &controllers.ApiController{}, "POST:DeleteHospital")

	App.Router("/v1/get-doctors", &controllers.ApiController{}, "GET:GetDoctors")
	App.Router("/v1/get-doctor", &controllers.ApiController{}, "GET:GetDoctor")
	App.Router("/v1/update-doctor", &controllers.ApiController{}, "POST:UpdateDoctor")
	App.Router("/v1/add-doctor", &controllers.ApiController{}, "POST:AddDoctor")
	App.Router("/v1/delete-doctor", &controllers.ApiController{}, "POST:DeleteDoctor")

	App.Router("/v1/get-patients", &controllers.ApiController{}, "GET:GetPatients")
	App.Router("/v1/get-patient", &controllers.ApiController{}, "GET:GetPatient")
	App.Router("/v1/update-patient", &controllers.ApiController{}, "POST:UpdatePatient")
	App.Router("/v1/add-patient", &controllers.ApiController{}, "POST:AddPatient")
	App.Router("/v1/delete-patient", &controllers.ApiController{}, "POST:DeletePatient")

	App.Router("/v1/get-caases", &controllers.ApiController{}, "GET:GetCaases")
	App.Router("/v1/get-caase", &controllers.ApiController{}, "GET:GetCaase")
	App.Router("/v1/update-caase", &controllers.ApiController{}, "POST:UpdateCaase")
	App.Router("/v1/add-caase", &controllers.ApiController{}, "POST:AddCaase")
	App.Router("/v1/delete-caase", &controllers.ApiController{}, "POST:DeleteCaase")

	App.Router("/v1/get-consultations", &controllers.ApiController{}, "GET:GetConsultations")
	App.Router("/v1/get-consultation", &controllers.ApiController{}, "GET:GetConsultation")
	App.Router("/v1/update-consultation", &controllers.ApiController{}, "POST:UpdateConsultation")
	App.Router("/v1/add-consultation", &controllers.ApiController{}, "POST:AddConsultation")
	App.Router("/v1/delete-consultation", &controllers.ApiController{}, "POST:DeleteConsultation")

	App.Router("/v1/get-system-info", &controllers.ApiController{}, "GET:GetSystemInfo")
	App.Router("/v1/get-version-info", &controllers.ApiController{}, "GET:GetVersionInfo")
	App.Router("/v1/health", &controllers.ApiController{}, "GET:Health")
	App.Router("/v1/get-prometheus-info", &controllers.ApiController{}, "GET:GetPrometheusInfo")
	App.Router("/v1/metrics", &controllers.ApiController{}, "GET:GetMetrics")

	// Unified chat — OpenAI-compatible completions with optional RAG.
	// /v1/chat is the new canonical route; /v1/chat/completions is kept as an
	// alias for OpenAI SDK compatibility.
	App.Router("/v1/chat", &controllers.ApiController{}, "POST:ChatCompletions")
	App.Router("/v1/chat/completions", &controllers.ApiController{}, "POST:ChatCompletions")
	App.Router("/v1/completions", &controllers.ApiController{}, "POST:ChatCompletions")
	// OpenAI Responses API — the native wire protocol used by current Codex.
	// The controller adapts onto ChatCompletions so auth, routing, billing and
	// failover remain one policy path.
	App.Router("/v1/responses", &controllers.ApiController{}, "POST:Responses")
	App.Router("/v1/models", &controllers.ApiController{}, "GET:ListModels")
	// Access gating for limited-preview SKUs (enso): a caller requests/reads their own
	// standing; a SuperAdmin grants and lists. Registered as a deeper path than
	// /v1/models so the literal segment is not captured as a :param.
	App.Router("/v1/models/:model/access", &controllers.ApiController{}, "GET:GetModelAccessStatus;POST:RequestModelAccess")
	App.Router("/v1/admin/model-access", &controllers.ApiController{}, "GET:AdminListModelAccess;POST:AdminGrantModelAccess")
	App.Router("/v1/admin/reload-model-config", &controllers.ApiController{}, "POST:ReloadModelConfig")
	App.Router("/v1/admin/refresh-model-pricing", &controllers.ApiController{}, "POST:RefreshModelPricing")

	// OpenAI-compatible embeddings and Cohere/Jina-compatible rerank. Both ride
	// the same auth + provider routing as /v1/chat/completions.
	App.Router("/v1/embeddings", &controllers.ApiController{}, "POST:Embeddings")
	App.Router("/v1/rerank", &controllers.ApiController{}, "POST:Rerank")

	// OpenAI-compatible image generation. Same auth + provider routing; the
	// zen3-image family routes to do-ai's fal-hosted diffusion models.
	App.Router("/v1/images/generations", &controllers.ApiController{}, "POST:ImagesGenerations")

	// OpenAI Sora-style ASYNC text-to-video. Same auth + provider routing; the
	// zen3-video family (and wan2-2-t2v-a14b) route to the spark-video backend's
	// async /v1/videos API. Create returns a job id immediately; the client polls
	// {id} and downloads {id}/content — so the pod never holds a request open for
	// the minutes a generation takes (that ~104s hold was the console-proxy 502).
	// The static /generations route is registered BEFORE the /:id wildcard so a
	// POST to /generations can never be captured as an :id.
	App.Router("/v1/videos/generations", &controllers.ApiController{}, "POST:VideosGenerations")
	App.Router("/v1/videos/:id", &controllers.ApiController{}, "GET:RetrieveVideo")
	App.Router("/v1/videos/:id/content", &controllers.ApiController{}, "GET:VideoContent")

	// OpenAI-compatible text-to-speech (/v1/audio/speech). Same auth + model-route
	// resolution as chat/images/video → a BYO TTS provider works transparently.
	// Completes native audio+image+video: /v1/audio/speech, /v1/images/generations,
	// /v1/videos/generations all OpenAI-shaped on the one router.
	App.Router("/v1/audio/speech", &controllers.ApiController{}, "POST:AudioSpeech")
	// Zen-native generative audio verbs: voice (TTS), music, foley.
	App.Router("/v1/audio/voice", &controllers.ApiController{}, "POST:AudioMedia")
	App.Router("/v1/audio/music", &controllers.ApiController{}, "POST:AudioMedia")
	App.Router("/v1/audio/foley", &controllers.ApiController{}, "POST:AudioMedia")

	App.Router("/v1/get-model-routes", &controllers.ApiController{}, "GET:GetModelRoutes")
	App.Router("/v1/get-model-route", &controllers.ApiController{}, "GET:GetModelRoute")
	App.Router("/v1/add-model-route", &controllers.ApiController{}, "POST:AddModelRoute")
	App.Router("/v1/update-model-route", &controllers.ApiController{}, "POST:UpdateModelRoute")
	App.Router("/v1/delete-model-route", &controllers.ApiController{}, "POST:DeleteModelRoute")

	// The router-config surface — per-org settings (/v1/org/settings + /list), routing
	// defaults (/v1/router/defaults), the policy noun (GET|PUT /v1/router/policy), the
	// artifact-meta write (/v1/router/artifact-meta), and the ledger/rewards exports
	// (/v1/router/{ledger,rewards}) — is served ZAP-native, the ONE implementation
	// (controllers/zap_router-policy-stats.go + zap_verticals-and-misc.go). No beego
	// twin: RouterConfigBridge is only the HTTP transport binding — it dispatches
	// in-process through the SAME gateway registry, so there is one handler, no
	// split-brain (the twin drift is exactly what silently dropped customer data).
	// "*": the native handler is method-aware (GET/PUT policy, GET/PUT/DELETE settings)
	// and returns 405 for a verb it does not own. /list is a distinct path segment, so
	// it needs its own route (the matcher keys on exact segment count).
	App.Router("/v1/router/policy", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/router/defaults", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/router/ledger", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/router/rewards", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/router/artifact-meta", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/org/settings", &controllers.ApiController{}, "*:RouterConfigBridge")
	App.Router("/v1/org/settings/list", &controllers.ApiController{}, "*:RouterConfigBridge")

	// Per-request reward signal for the enso training loop: clients POST an outcome
	// keyed by the request_id they hold, scoped to their own org. /v1/feedback is the
	// signal-typed front door ({request_id, signal: up|down|regenerate|switch|…}) onto
	// the ONE reward join; the engine's online LinUCB observe is driven from here.
	App.Router("/v1/feedback", &controllers.ApiController{}, "POST:AddRoutingReward")

	// Self-scoped data ownership (org-admin, own org only): export or delete the
	// caller's OWN content-free routing ledger — the customer-facing right-to-
	// access + right-to-be-forgotten that pairs with the training opt-in.
	App.Router("/v1/export-my-routing-data", &controllers.ApiController{}, "GET:ExportMyRoutingData")
	App.Router("/v1/delete-my-routing-data", &controllers.ApiController{}, "POST:DeleteMyRoutingData")

	// Router observability aggregate (aggregates only, never raw events): the
	// admin savings-vs-perf panel reads the org-scoped form; world.hanzo.ai polls
	// ?scope=platform, a PUBLIC-safe aggregate with no $ levels or org identity.
	// Plus the per-org opt-in for contributing events to the shared base refresh
	// and the retrain job's published-state write.
	App.Router("/v1/router/stats", &controllers.ApiController{}, "GET:GetRouterStats")
	// Improvement time-series (reward + cost-saved + adoption over time, retrain
	// markers) — the world.hanzo.ai flywheel view. PUBLIC ?scope=platform, aggregates
	// only (task mix, never model ids). Balance+auth-exempt like /v1/router/stats.
	App.Router("/v1/router/history", &controllers.ApiController{}, "GET:GetRouterHistory")
	// Live Mean-Field Judge Panel state for the world.hanzo.ai dashboard. PUBLIC,
	// platform-global (model ids + scalars only, no org/user rows), balance+auth-exempt
	// like /v1/router/stats — the world widget polls it the same way.
	App.Router("/v1/router/judge-panel", &controllers.ApiController{}, "GET:GetRouterJudgePanel")

	// Live request-geo aggregate for the world.hanzo.ai Hanzo-mode globe. PUBLIC:
	// aggregates only (country/region counts + throughput rates), no auth, no IPs.
	// Balance-exempt via isBalanceExempt; the authz filter passes it through as a
	// non get-/update- controller name (same class as /v1/router/stats).
	App.Router("/v1/traffic/globe", &controllers.ApiController{}, "GET:GetTrafficGlobe")
	App.Router("/v1/get-training-contribution", &controllers.ApiController{}, "GET:GetTrainingContribution")
	App.Router("/v1/update-training-contribution", &controllers.ApiController{}, "POST:UpdateTrainingContribution")

	// Anthropic Messages API compatible endpoints
	App.Router("/v1/messages", &controllers.ApiController{}, "POST:AnthropicMessages")
	App.Router("/v1/messages/count_tokens", &controllers.ApiController{}, "POST:AnthropicCountTokens")

	App.Router("/v1/wecom-bot/callback/:botId", &controllers.ApiController{}, "GET:WecomBotVerifyUrl;POST:WecomBotHandleMessage")

	App.Router("/v1/get-agents-dashboard-url", &controllers.ApiController{}, "GET:GetAgentsDashboardUrl")
	App.Router("/v1/get-vm-dashboard-url", &controllers.ApiController{}, "GET:GetVmDashboardUrl")

	// Normalised document APIs (public).
	// Retrieval / RAG lives on /v1/chat itself — no separate chat-docs route.
	App.Router("/v1/search", &controllers.ApiController{}, "POST:SearchDocs")
	App.Router("/v1/index", &controllers.ApiController{}, "POST:IndexDocs")
	App.Router("/v1/search/stats", &controllers.ApiController{}, "GET:SearchDocsStats")
	App.Router("/v1/scrape", &controllers.ApiController{}, "POST:ScrapeDocs")
	App.Router("/v1/scrape/preview", &controllers.ApiController{}, "POST:ScrapePreview")
	App.Router("/v1/crawl", &controllers.ApiController{}, "POST:Crawl")

	// File-scoped RAG — the ONE canonical uploaded-file RAG surface (consolidates
	// the retired standalone chat-rag-api). Embed a file under a file_id, then
	// retrieve chunks scoped to that file (or a set of files) over the SAME
	// Search+Vector index as doc RAG.
	App.Router("/v1/rag/embed", &controllers.ApiController{}, "POST:RagEmbed")
	App.Router("/v1/rag/query", &controllers.ApiController{}, "POST:RagQuery")
	App.Router("/v1/rag/query-multiple", &controllers.ApiController{}, "POST:RagQueryMultiple")
	App.Router("/v1/rag/delete", &controllers.ApiController{}, "POST:RagDelete")
	App.Router("/v1/rag/context", &controllers.ApiController{}, "GET:RagContext")

	// LibreChat-compat RAG — the FIXED contract hanzo.chat's RAG client calls at
	// RAG_API_URL. Pointing RAG_API_URL=https://api.hanzo.ai/v1 retires the
	// standalone chat-rag-api with no chat-repo change. Thin projection over the
	// same object.Rag* logic as /v1/rag/*.
	App.Router("/v1/embed", &controllers.ApiController{}, "POST:RagEmbedMultipart")
	App.Router("/v1/query", &controllers.ApiController{}, "POST:RagQueryCompat")
	App.Router("/v1/query_multiple", &controllers.ApiController{}, "POST:RagQueryMultipleCompat")
	App.Router("/v1/documents", &controllers.ApiController{}, "DELETE:RagDeleteDocuments")
	App.Router("/v1/documents/:file_id/context", &controllers.ApiController{}, "GET:RagDocumentContext")

	// Memory subsystem — cloud backend of the unified memory interface.
	// Per-user scoped; identity comes from gateway IAM headers, never the body.
	App.Router("/v1/memory/remember", &controllers.ApiController{}, "POST:MemoryRemember")
	App.Router("/v1/memory/search", &controllers.ApiController{}, "GET:MemorySearch")
	App.Router("/v1/memory/list", &controllers.ApiController{}, "GET:MemoryList")
	App.Router("/v1/memory/recall", &controllers.ApiController{}, "GET:MemoryRecall")
	App.Router("/v1/memory/facts", &controllers.ApiController{}, "GET:MemoryFacts")
	App.Router("/v1/memory/update", &controllers.ApiController{}, "POST:MemoryUpdate")
	App.Router("/v1/memory/delete", &controllers.ApiController{}, "POST:MemoryDelete")
}
