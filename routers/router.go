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
	"github.com/beego/beego"
	"github.com/hanzoai/cloud/controllers"
)

func init() {
	initAPI()
}

func initAPI() {
	// Single namespace: /v1/*. Never /api/* (the subdomain is api.*, so /api/
	// would double-prefix). Controller @router annotations still register the
	// implicit routes under this namespace.
	ns := beego.NewNamespace("/v1",
		beego.NSInclude(
			&controllers.ApiController{},
		),
	)
	beego.AddNamespace(ns)

	beego.Router("/v1/signin", &controllers.ApiController{}, "POST:Signin")
	beego.Router("/v1/signout", &controllers.ApiController{}, "POST:Signout")
	beego.Router("/v1/get-account", &controllers.ApiController{}, "GET:GetAccount")

	beego.Router("/v1/get-global-videos", &controllers.ApiController{}, "GET:GetGlobalVideos")
	beego.Router("/v1/get-videos", &controllers.ApiController{}, "GET:GetVideos")
	beego.Router("/v1/get-video", &controllers.ApiController{}, "GET:GetVideo")
	beego.Router("/v1/update-video", &controllers.ApiController{}, "POST:UpdateVideo")
	beego.Router("/v1/add-video", &controllers.ApiController{}, "POST:AddVideo")
	beego.Router("/v1/delete-video", &controllers.ApiController{}, "POST:DeleteVideo")
	beego.Router("/v1/upload-video", &controllers.ApiController{}, "POST:UploadVideo")

	beego.Router("/v1/get-global-stores", &controllers.ApiController{}, "GET:GetGlobalStores")
	beego.Router("/v1/get-stores", &controllers.ApiController{}, "GET:GetStores")
	beego.Router("/v1/get-store", &controllers.ApiController{}, "GET:GetStore")
	beego.Router("/v1/update-store", &controllers.ApiController{}, "POST:UpdateStore")
	beego.Router("/v1/add-store", &controllers.ApiController{}, "POST:AddStore")
	beego.Router("/v1/delete-store", &controllers.ApiController{}, "POST:DeleteStore")
	beego.Router("/v1/refresh-store-vectors", &controllers.ApiController{}, "POST:RefreshStoreVectors")
	beego.Router("/v1/get-storage-providers", &controllers.ApiController{}, "GET:GetStorageProviders")
	beego.Router("/v1/get-store-names", &controllers.ApiController{}, "GET:GetStoreNames")

	beego.Router("/v1/get-global-providers", &controllers.ApiController{}, "GET:GetGlobalProviders")
	beego.Router("/v1/get-providers", &controllers.ApiController{}, "GET:GetProviders")
	beego.Router("/v1/get-provider", &controllers.ApiController{}, "GET:GetProvider")
	beego.Router("/v1/update-provider", &controllers.ApiController{}, "POST:UpdateProvider")
	beego.Router("/v1/add-provider", &controllers.ApiController{}, "POST:AddProvider")
	beego.Router("/v1/delete-provider", &controllers.ApiController{}, "POST:DeleteProvider")
	beego.Router("/v1/refresh-mcp-tools", &controllers.ApiController{}, "POST:RefreshMcpTools")

	beego.Router("/v1/get-global-files", &controllers.ApiController{}, "GET:GetGlobalFiles")
	beego.Router("/v1/get-files", &controllers.ApiController{}, "GET:GetFiles")
	beego.Router("/v1/get-file", &controllers.ApiController{}, "GET:GetFileMy")
	beego.Router("/v1/update-file", &controllers.ApiController{}, "POST:UpdateFile")
	beego.Router("/v1/add-file", &controllers.ApiController{}, "POST:AddFile")
	beego.Router("/v1/delete-file", &controllers.ApiController{}, "POST:DeleteFile")
	beego.Router("/v1/refresh-file-vectors", &controllers.ApiController{}, "POST:RefreshFileVectors")

	beego.Router("/v1/get-global-vectors", &controllers.ApiController{}, "GET:GetGlobalVectors")
	beego.Router("/v1/get-vectors", &controllers.ApiController{}, "GET:GetVectors")
	beego.Router("/v1/get-vector", &controllers.ApiController{}, "GET:GetVector")
	beego.Router("/v1/update-vector", &controllers.ApiController{}, "POST:UpdateVector")
	beego.Router("/v1/add-vector", &controllers.ApiController{}, "POST:AddVector")
	beego.Router("/v1/delete-vector", &controllers.ApiController{}, "POST:DeleteVector")
	beego.Router("/v1/delete-all-vectors", &controllers.ApiController{}, "POST:DeleteAllVectors")

	beego.Router("/v1/generate-text-to-speech-audio", &controllers.ApiController{}, "POST:GenerateTextToSpeechAudio")
	beego.Router("/v1/generate-text-to-speech-audio-stream", &controllers.ApiController{}, "GET:GenerateTextToSpeechAudioStream")
	beego.Router("/v1/process-speech-to-text", &controllers.ApiController{}, "POST:ProcessSpeechToText")

	beego.Router("/v1/get-global-chats", &controllers.ApiController{}, "GET:GetGlobalChats")
	beego.Router("/v1/get-chats", &controllers.ApiController{}, "GET:GetChats")
	beego.Router("/v1/get-chat", &controllers.ApiController{}, "GET:GetChat")
	beego.Router("/v1/update-chat", &controllers.ApiController{}, "POST:UpdateChat")
	beego.Router("/v1/add-chat", &controllers.ApiController{}, "POST:AddChat")
	beego.Router("/v1/delete-chat", &controllers.ApiController{}, "POST:DeleteChat")

	beego.Router("/v1/get-global-messages", &controllers.ApiController{}, "GET:GetGlobalMessages")
	beego.Router("/v1/get-messages", &controllers.ApiController{}, "GET:GetMessages")
	beego.Router("/v1/get-message", &controllers.ApiController{}, "GET:GetMessage")
	beego.Router("/v1/get-message-answer", &controllers.ApiController{}, "GET:GetMessageAnswer")
	beego.Router("/v1/get-answer", &controllers.ApiController{}, "GET:GetAnswer")
	beego.Router("/v1/update-message", &controllers.ApiController{}, "POST:UpdateMessage")
	beego.Router("/v1/add-message", &controllers.ApiController{}, "POST:AddMessage")
	beego.Router("/v1/delete-message", &controllers.ApiController{}, "POST:DeleteMessage")
	beego.Router("/v1/delete-welcome-message", &controllers.ApiController{}, "POST:DeleteWelcomeMessage")

	beego.Router("/v1/get-global-graphs", &controllers.ApiController{}, "GET:GetGlobalGraphs")
	beego.Router("/v1/get-graphs", &controllers.ApiController{}, "GET:GetGraphs")
	beego.Router("/v1/get-graph", &controllers.ApiController{}, "GET:GetGraph")
	beego.Router("/v1/update-graph", &controllers.ApiController{}, "POST:UpdateGraph")
	beego.Router("/v1/add-graph", &controllers.ApiController{}, "POST:AddGraph")
	beego.Router("/v1/delete-graph", &controllers.ApiController{}, "POST:DeleteGraph")

	beego.Router("/v1/get-templates", &controllers.ApiController{}, "GET:GetTemplates")
	beego.Router("/v1/get-template", &controllers.ApiController{}, "GET:GetTemplate")
	beego.Router("/v1/update-template", &controllers.ApiController{}, "POST:UpdateTemplate")
	beego.Router("/v1/add-template", &controllers.ApiController{}, "POST:AddTemplate")
	beego.Router("/v1/delete-template", &controllers.ApiController{}, "POST:DeleteTemplate")
	beego.Router("/v1/get-k8s-status", &controllers.ApiController{}, "GET:GetK8sStatus")

	beego.Router("/v1/get-applications", &controllers.ApiController{}, "GET:GetApplications")
	beego.Router("/v1/get-application", &controllers.ApiController{}, "GET:GetApplication")
	beego.Router("/v1/update-application", &controllers.ApiController{}, "POST:UpdateApplication")
	beego.Router("/v1/add-application", &controllers.ApiController{}, "POST:AddApplication")
	beego.Router("/v1/delete-application", &controllers.ApiController{}, "POST:DeleteApplication")

	beego.Router("/v1/deploy-application", &controllers.ApiController{}, "POST:DeployApplication")
	beego.Router("/v1/undeploy-application", &controllers.ApiController{}, "POST:UndeployApplication")

	beego.Router("/v1/get-usages", &controllers.ApiController{}, "GET:GetUsages")
	beego.Router("/v1/get-range-usages", &controllers.ApiController{}, "GET:GetRangeUsages")
	beego.Router("/v1/get-users", &controllers.ApiController{}, "GET:GetUsers")
	beego.Router("/v1/get-user-table-infos", &controllers.ApiController{}, "GET:GetUserTableInfos")

	beego.Router("/v1/get-activities", &controllers.ApiController{}, "GET:GetActivities")
	// beego.Router("/v1/get-range-activities", &controllers.ApiController{}, "GET:GetRangeActivities")

	beego.Router("/v1/get-global-workflows", &controllers.ApiController{}, "GET:GetGlobalWorkflows")
	beego.Router("/v1/get-workflows", &controllers.ApiController{}, "GET:GetWorkflows")
	beego.Router("/v1/get-workflow", &controllers.ApiController{}, "GET:GetWorkflow")
	beego.Router("/v1/update-workflow", &controllers.ApiController{}, "POST:UpdateWorkflow")
	beego.Router("/v1/add-workflow", &controllers.ApiController{}, "POST:AddWorkflow")
	beego.Router("/v1/delete-workflow", &controllers.ApiController{}, "POST:DeleteWorkflow")

	beego.Router("/v1/get-global-tasks", &controllers.ApiController{}, "GET:GetGlobalTasks")
	beego.Router("/v1/get-tasks", &controllers.ApiController{}, "GET:GetTasks")
	beego.Router("/v1/get-task", &controllers.ApiController{}, "GET:GetTask")
	beego.Router("/v1/update-task", &controllers.ApiController{}, "POST:UpdateTask")
	beego.Router("/v1/add-task", &controllers.ApiController{}, "POST:AddTask")
	beego.Router("/v1/delete-task", &controllers.ApiController{}, "POST:DeleteTask")
	beego.Router("/v1/upload-task-document", &controllers.ApiController{}, "POST:UploadTaskDocument")
	beego.Router("/v1/analyze-task", &controllers.ApiController{}, "POST:AnalyzeTask")

	beego.Router("/v1/get-global-scales", &controllers.ApiController{}, "GET:GetGlobalScales")
	beego.Router("/v1/get-scales", &controllers.ApiController{}, "GET:GetScales")
	beego.Router("/v1/get-scale", &controllers.ApiController{}, "GET:GetScale")
	beego.Router("/v1/get-public-scales", &controllers.ApiController{}, "GET:GetPublicScales")
	beego.Router("/v1/update-scale", &controllers.ApiController{}, "POST:UpdateScale")
	beego.Router("/v1/add-scale", &controllers.ApiController{}, "POST:AddScale")
	beego.Router("/v1/delete-scale", &controllers.ApiController{}, "POST:DeleteScale")

	beego.Router("/v1/get-global-forms", &controllers.ApiController{}, "GET:GetGlobalForms")
	beego.Router("/v1/get-forms", &controllers.ApiController{}, "GET:GetForms")
	beego.Router("/v1/get-form", &controllers.ApiController{}, "GET:GetForm")
	beego.Router("/v1/update-form", &controllers.ApiController{}, "POST:UpdateForm")
	beego.Router("/v1/add-form", &controllers.ApiController{}, "POST:AddForm")
	beego.Router("/v1/delete-form", &controllers.ApiController{}, "POST:DeleteForm")

	beego.Router("/v1/get-form-data", &controllers.ApiController{}, "GET:GetFormData")

	beego.Router("/v1/get-global-articles", &controllers.ApiController{}, "GET:GetGlobalArticles")
	beego.Router("/v1/get-articles", &controllers.ApiController{}, "GET:GetArticles")
	beego.Router("/v1/get-article", &controllers.ApiController{}, "GET:GetArticle")
	beego.Router("/v1/update-article", &controllers.ApiController{}, "POST:UpdateArticle")
	beego.Router("/v1/add-article", &controllers.ApiController{}, "POST:AddArticle")
	beego.Router("/v1/delete-article", &controllers.ApiController{}, "POST:DeleteArticle")

	beego.Router("/v1/update-tree-file", &controllers.ApiController{}, "POST:UpdateTreeFile")
	beego.Router("/v1/add-tree-file", &controllers.ApiController{}, "POST:AddTreeFile")
	beego.Router("/v1/delete-tree-file", &controllers.ApiController{}, "POST:DeleteTreeFile")
	beego.Router("/v1/activate-file", &controllers.ApiController{}, "POST:ActivateFile")
	beego.Router("/v1/get-active-file", &controllers.ApiController{}, "GET:GetActiveFile")

	beego.Router("/v1/upload-file", &controllers.ApiController{}, "POST:UploadFile")

	beego.Router("/v1/get-permissions", &controllers.ApiController{}, "GET:GetPermissions")
	beego.Router("/v1/get-permission", &controllers.ApiController{}, "GET:GetPermission")
	beego.Router("/v1/update-permission", &controllers.ApiController{}, "POST:UpdatePermission")
	beego.Router("/v1/add-permission", &controllers.ApiController{}, "POST:AddPermission")
	beego.Router("/v1/delete-permission", &controllers.ApiController{}, "POST:DeletePermission")

	beego.Router("/v1/get-nodes", &controllers.ApiController{}, "GET:GetNodes")
	beego.Router("/v1/get-node", &controllers.ApiController{}, "GET:GetNode")
	beego.Router("/v1/update-node", &controllers.ApiController{}, "POST:UpdateNode")
	beego.Router("/v1/add-node", &controllers.ApiController{}, "POST:AddNode")
	beego.Router("/v1/delete-node", &controllers.ApiController{}, "POST:DeleteNode")

	beego.Router("/v1/get-machines", &controllers.ApiController{}, "GET:GetMachines")
	beego.Router("/v1/get-machine", &controllers.ApiController{}, "GET:GetMachine")
	beego.Router("/v1/update-machine", &controllers.ApiController{}, "POST:UpdateMachine")
	beego.Router("/v1/add-machine", &controllers.ApiController{}, "POST:AddMachine")
	beego.Router("/v1/delete-machine", &controllers.ApiController{}, "POST:DeleteMachine")

	beego.Router("/v1/get-assets", &controllers.ApiController{}, "GET:GetAssets")
	beego.Router("/v1/get-asset", &controllers.ApiController{}, "GET:GetAsset")
	beego.Router("/v1/update-asset", &controllers.ApiController{}, "POST:UpdateAsset")
	beego.Router("/v1/add-asset", &controllers.ApiController{}, "POST:AddAsset")
	beego.Router("/v1/delete-asset", &controllers.ApiController{}, "POST:DeleteAsset")
	beego.Router("/v1/scan-asset", &controllers.ApiController{}, "POST:ScanAsset")
	beego.Router("/v1/scan-assets", &controllers.ApiController{}, "POST:ScanAssets")

	beego.Router("/v1/get-scans", &controllers.ApiController{}, "GET:GetScans")
	beego.Router("/v1/get-scan", &controllers.ApiController{}, "GET:GetScan")
	beego.Router("/v1/update-scan", &controllers.ApiController{}, "POST:UpdateScan")
	beego.Router("/v1/add-scan", &controllers.ApiController{}, "POST:AddScan")
	beego.Router("/v1/delete-scan", &controllers.ApiController{}, "POST:DeleteScan")

	beego.Router("/v1/install-patch", &controllers.ApiController{}, "POST:InstallPatch")

	beego.Router("/v1/get-images", &controllers.ApiController{}, "GET:GetImages")
	beego.Router("/v1/get-image", &controllers.ApiController{}, "GET:GetImage")
	beego.Router("/v1/update-image", &controllers.ApiController{}, "POST:UpdateImage")
	beego.Router("/v1/add-image", &controllers.ApiController{}, "POST:AddImage")
	beego.Router("/v1/delete-image", &controllers.ApiController{}, "POST:DeleteImage")

	beego.Router("/v1/get-containers", &controllers.ApiController{}, "GET:GetContainers")
	beego.Router("/v1/get-container", &controllers.ApiController{}, "GET:GetContainer")
	beego.Router("/v1/update-container", &controllers.ApiController{}, "POST:UpdateContainer")
	beego.Router("/v1/add-container", &controllers.ApiController{}, "POST:AddContainer")
	beego.Router("/v1/delete-container", &controllers.ApiController{}, "POST:DeleteContainer")

	beego.Router("/v1/get-pods", &controllers.ApiController{}, "GET:GetPods")
	beego.Router("/v1/get-pod", &controllers.ApiController{}, "GET:GetPod")
	beego.Router("/v1/update-pod", &controllers.ApiController{}, "POST:UpdatePod")
	beego.Router("/v1/add-pod", &controllers.ApiController{}, "POST:AddPod")
	beego.Router("/v1/delete-pod", &controllers.ApiController{}, "POST:DeletePod")

	beego.Router("/v1/add-node-tunnel", &controllers.ApiController{}, "POST:AddNodeTunnel")
	beego.Router("/v1/get-node-tunnel", &controllers.ApiController{}, "GET:GetNodeTunnel")
	beego.Router("/v1/dev-bridge", &controllers.ApiController{}, "GET:DevBridge")

	beego.Router("/v1/get-sessions", &controllers.ApiController{}, "GET:GetSessions")
	beego.Router("/v1/get-session", &controllers.ApiController{}, "GET:GetSession")
	beego.Router("/v1/update-session", &controllers.ApiController{}, "POST:UpdateSession")
	beego.Router("/v1/add-session", &controllers.ApiController{}, "POST:AddSession")
	beego.Router("/v1/delete-session", &controllers.ApiController{}, "POST:DeleteSession")
	beego.Router("/v1/is-session-duplicated", &controllers.ApiController{}, "GET:IsSessionDuplicated")

	beego.Router("/v1/get-connections", &controllers.ApiController{}, "GET:GetConnections")
	beego.Router("/v1/get-connection", &controllers.ApiController{}, "GET:GetConnection")
	beego.Router("/v1/update-connection", &controllers.ApiController{}, "POST:UpdateConnection")
	beego.Router("/v1/add-connection", &controllers.ApiController{}, "POST:AddConnection")
	beego.Router("/v1/delete-connection", &controllers.ApiController{}, "POST:DeleteConnection")
	beego.Router("/v1/start-connection", &controllers.ApiController{}, "POST:StartConnection")
	beego.Router("/v1/stop-connection", &controllers.ApiController{}, "POST:StopConnection")

	beego.Router("/v1/get-records", &controllers.ApiController{}, "GET:GetRecords")
	beego.Router("/v1/get-record", &controllers.ApiController{}, "GET:GetRecord")
	beego.Router("/v1/update-record", &controllers.ApiController{}, "POST:UpdateRecord")
	beego.Router("/v1/add-record", &controllers.ApiController{}, "POST:AddRecord")
	beego.Router("/v1/add-records", &controllers.ApiController{}, "POST:AddRecords")
	beego.Router("/v1/delete-record", &controllers.ApiController{}, "POST:DeleteRecord")

	beego.Router("/v1/commit-record", &controllers.ApiController{}, "POST:CommitRecord")
	beego.Router("/v1/commit-record-second", &controllers.ApiController{}, "POST:CommitRecordSecond")
	beego.Router("/v1/query-record", &controllers.ApiController{}, "GET:QueryRecord")
	beego.Router("/v1/query-record-second", &controllers.ApiController{}, "GET:QueryRecordSecond")

	beego.Router("/v1/get-hospitals", &controllers.ApiController{}, "GET:GetHospitals")
	beego.Router("/v1/get-hospital", &controllers.ApiController{}, "GET:GetHospital")
	beego.Router("/v1/update-hospital", &controllers.ApiController{}, "POST:UpdateHospital")
	beego.Router("/v1/add-hospital", &controllers.ApiController{}, "POST:AddHospital")
	beego.Router("/v1/delete-hospital", &controllers.ApiController{}, "POST:DeleteHospital")

	beego.Router("/v1/get-doctors", &controllers.ApiController{}, "GET:GetDoctors")
	beego.Router("/v1/get-doctor", &controllers.ApiController{}, "GET:GetDoctor")
	beego.Router("/v1/update-doctor", &controllers.ApiController{}, "POST:UpdateDoctor")
	beego.Router("/v1/add-doctor", &controllers.ApiController{}, "POST:AddDoctor")
	beego.Router("/v1/delete-doctor", &controllers.ApiController{}, "POST:DeleteDoctor")

	beego.Router("/v1/get-patients", &controllers.ApiController{}, "GET:GetPatients")
	beego.Router("/v1/get-patient", &controllers.ApiController{}, "GET:GetPatient")
	beego.Router("/v1/update-patient", &controllers.ApiController{}, "POST:UpdatePatient")
	beego.Router("/v1/add-patient", &controllers.ApiController{}, "POST:AddPatient")
	beego.Router("/v1/delete-patient", &controllers.ApiController{}, "POST:DeletePatient")

	beego.Router("/v1/get-caases", &controllers.ApiController{}, "GET:GetCaases")
	beego.Router("/v1/get-caase", &controllers.ApiController{}, "GET:GetCaase")
	beego.Router("/v1/update-caase", &controllers.ApiController{}, "POST:UpdateCaase")
	beego.Router("/v1/add-caase", &controllers.ApiController{}, "POST:AddCaase")
	beego.Router("/v1/delete-caase", &controllers.ApiController{}, "POST:DeleteCaase")

	beego.Router("/v1/get-consultations", &controllers.ApiController{}, "GET:GetConsultations")
	beego.Router("/v1/get-consultation", &controllers.ApiController{}, "GET:GetConsultation")
	beego.Router("/v1/update-consultation", &controllers.ApiController{}, "POST:UpdateConsultation")
	beego.Router("/v1/add-consultation", &controllers.ApiController{}, "POST:AddConsultation")
	beego.Router("/v1/delete-consultation", &controllers.ApiController{}, "POST:DeleteConsultation")

	beego.Router("/v1/get-system-info", &controllers.ApiController{}, "GET:GetSystemInfo")
	beego.Router("/v1/get-version-info", &controllers.ApiController{}, "GET:GetVersionInfo")
	beego.Router("/v1/health", &controllers.ApiController{}, "GET:Health")
	beego.Router("/v1/get-prometheus-info", &controllers.ApiController{}, "GET:GetPrometheusInfo")
	beego.Router("/v1/metrics", &controllers.ApiController{}, "GET:GetMetrics")

	// Unified chat — OpenAI-compatible completions with optional RAG.
	// /v1/chat is the new canonical route; /v1/chat/completions is kept as an
	// alias for OpenAI SDK compatibility.
	beego.Router("/v1/chat", &controllers.ApiController{}, "POST:ChatCompletions")
	beego.Router("/v1/chat/completions", &controllers.ApiController{}, "POST:ChatCompletions")
	beego.Router("/v1/completions", &controllers.ApiController{}, "POST:ChatCompletions")
	beego.Router("/v1/models", &controllers.ApiController{}, "GET:ListModels")
	beego.Router("/v1/reload-model-config", &controllers.ApiController{}, "POST:ReloadModelConfig")

	beego.Router("/v1/get-model-routes", &controllers.ApiController{}, "GET:GetModelRoutes")
	beego.Router("/v1/get-model-route", &controllers.ApiController{}, "GET:GetModelRoute")
	beego.Router("/v1/add-model-route", &controllers.ApiController{}, "POST:AddModelRoute")
	beego.Router("/v1/update-model-route", &controllers.ApiController{}, "POST:UpdateModelRoute")
	beego.Router("/v1/delete-model-route", &controllers.ApiController{}, "POST:DeleteModelRoute")

	// Anthropic Messages API compatible endpoints
	beego.Router("/v1/messages", &controllers.ApiController{}, "POST:AnthropicMessages")

	beego.Router("/v1/wecom-bot/callback/:botId", &controllers.ApiController{}, "GET:WecomBotVerifyUrl;POST:WecomBotHandleMessage")

	beego.Router("/v1/get-agents-dashboard-url", &controllers.ApiController{}, "GET:GetAgentsDashboardUrl")
	beego.Router("/v1/get-vm-dashboard-url", &controllers.ApiController{}, "GET:GetVmDashboardUrl")

	// Normalised document APIs (public).
	// Retrieval / RAG lives on /v1/chat itself — no separate chat-docs route.
	beego.Router("/v1/search", &controllers.ApiController{}, "POST:SearchDocs")
	beego.Router("/v1/index", &controllers.ApiController{}, "POST:IndexDocs")
	beego.Router("/v1/search/stats", &controllers.ApiController{}, "GET:SearchDocsStats")
	beego.Router("/v1/scrape", &controllers.ApiController{}, "POST:ScrapeDocs")
	beego.Router("/v1/scrape/preview", &controllers.ApiController{}, "POST:ScrapePreview")
}
