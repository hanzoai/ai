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
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/beego/beego/context"
	iamsdk "github.com/casdoor/casdoor-go-sdk/casdoorsdk"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/object"
)

const (
	headerOrigin           = "Origin"
	headerAllowOrigin      = "Access-Control-Allow-Origin"
	headerAllowMethods     = "Access-Control-Allow-Methods"
	headerAllowHeaders     = "Access-Control-Allow-Headers"
	headerAllowCredentials = "Access-Control-Allow-Credentials"
	headerExposeHeaders    = "Access-Control-Expose-Headers"
)

func setCorsHeaders(ctx *context.Context, origin string) {
	ctx.Output.Header(headerAllowOrigin, origin)
	ctx.Output.Header(headerAllowMethods, "GET, POST, DELETE, PUT, PATCH, OPTIONS")
	ctx.Output.Header(headerAllowHeaders, "Origin, X-Requested-With, Content-Type, Accept")
	ctx.Output.Header(headerExposeHeaders, "Content-Length")
	ctx.Output.Header(headerAllowCredentials, "true")

	if ctx.Input.Method() == "OPTIONS" {
		ctx.ResponseWriter.WriteHeader(http.StatusOK)
	}
}

func CorsFilter(ctx *context.Context) {
	origin := ctx.Input.Header(headerOrigin)

	if origin == "" || origin == "null" {
		return
	}

	// Check if origin is allowed based on IAM application's RedirectUris
	setCorsHeaders(ctx, origin)
	ok, err := isOriginAllowed(origin)
	if err != nil {
		// If IAM is not configured, allow the origin for backwards compatibility
		iamEndpoint := conf.GetConfigString("iamEndpoint")
		if iamEndpoint == "" {
			return
		}
		// Otherwise, reject the request
		ctx.ResponseWriter.WriteHeader(http.StatusForbidden)
		responseError(ctx, fmt.Sprintf("CORS error: %s, path: %s", err.Error(), ctx.Request.URL.Path))
		return
	}

	if !ok {
		ctx.ResponseWriter.WriteHeader(http.StatusForbidden)
		responseError(ctx, fmt.Sprintf("CORS error: origin [%s] is not allowed, path: %s", origin, ctx.Request.URL.Path))
	}

	if object.CloudHost == "" {
		object.CloudHost = origin
	}
}

func isOriginAllowed(origin string) (bool, error) {
	iamEndpoint := conf.GetConfigString("iamEndpoint")
	iamApplication := conf.GetConfigString("iamApplication")

	// If IAM is not configured, return error to trigger backwards compatibility
	if iamEndpoint == "" || iamApplication == "" {
		return false, fmt.Errorf("iamEndpoint or iamApplication is empty")
	}

	application, err := iamsdk.GetApplication(iamApplication)
	if err != nil {
		return false, err
	}
	if application == nil {
		return false, fmt.Errorf("The application: %s does not exist", iamApplication)
	}

	// Check if origin matches any RedirectUri
	for _, redirectUri := range application.RedirectUris {
		parsedUrl, err := url.Parse(redirectUri)
		if err != nil {
			continue
		}
		allowedOrigin := parsedUrl.Scheme + "://" + parsedUrl.Host
		if origin == allowedOrigin || strings.Contains(origin, allowedOrigin) {
			return true, nil
		}
	}

	return false, nil
}
