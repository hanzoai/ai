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

package routers

import (
	"strings"

	"github.com/beego/beego"
	"github.com/beego/beego/context"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/controllers"
	"github.com/hanzoai/cloud/util"
)

func AuthzFilter(ctx *context.Context) {
	method := ctx.Request.Method
	urlPath := ctx.Request.URL.Path

	adminDomain := conf.GetConfigString("adminDomain")
	if adminDomain != "" && ctx.Request.Host == adminDomain {
		return
	}

	if conf.IsDemoMode() {
		if !isAllowedInDemoMode(method, urlPath) {
			controllers.DenyRequest(ctx)
		}
	}
	permissionFilter(ctx)
}

func isAllowedInDemoMode(method string, urlPath string) bool {
	if method != "POST" {
		return true
	}

	if strings.HasPrefix(urlPath, "/v1/signin") || urlPath == "/v1/signout" || urlPath == "/v1/add-chat" || urlPath == "/v1/add-message" || urlPath == "/v1/update-message" || urlPath == "/v1/delete-welcome-message" || urlPath == "/v1/generate-text-to-speech-audio" || urlPath == "/v1/add-node-tunnel" || urlPath == "/v1/start-connection" || urlPath == "/v1/stop-connection" || urlPath == "/v1/commit-record" || urlPath == "/v1/commit-record-second" || urlPath == "/v1/update-chat" || urlPath == "/v1/delete-chat" || urlPath == "/v1/search-docs" || urlPath == "/v1/chat-docs" {
		return true
	}

	return false
}

func permissionFilter(ctx *context.Context) {
	path := ctx.Request.URL.Path
	controllerName := strings.TrimPrefix(path, "/v1/")

	if !strings.HasPrefix(path, "/v1/") {
		return
	}

	disablePreviewMode, _ := beego.AppConfig.Bool("disablePreviewMode")

	isUpdateRequest := strings.HasPrefix(controllerName, "update-") || strings.HasPrefix(controllerName, "add-") || strings.HasPrefix(controllerName, "delete-") || strings.HasPrefix(controllerName, "refresh-") || strings.HasPrefix(controllerName, "deploy-")
	isGetRequest := strings.HasPrefix(controllerName, "get-")

	if !disablePreviewMode && isGetRequest {
		return
	}
	if !isGetRequest && !isUpdateRequest {
		return
	}

	exemptedPaths := []string{
		"get-account", "get-chats", "get-forms", "get-global-videos", "get-videos", "get-video", "get-messages",
		"delete-welcome-message", "get-message-answer", "get-answer",
		"get-storage-providers", "get-store", "get-providers", "get-global-stores",
		"update-chat", "add-chat", "delete-chat", "update-message", "add-message",
		"get-chat", "get-message",
		"get-tasks", "get-task", "get-public-scales", "update-task", "add-task", "delete-task", "upload-task-document",
		"search-docs", "chat-docs", "search-docs/stats",
	}

	for _, exemptPath := range exemptedPaths {
		if controllerName == exemptPath {
			return
		}
	}

	user := GetSessionUser(ctx)

	if !util.IsAdmin(user) {
		responseError(ctx, "auth:this operation requires admin privilege")
		return
	}
}
