// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/ai/object"
)

// threeDGenerationsRequest is the /v1/3d/generations create body.
type threeDGenerationsRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// ThreeDGenerations implements POST /v1/3d/generations for Zen family 3D SKUs
// (mori, zen-3d). Forwards to zen's /v1/3d/generations and bills per unit.
//
// @Title ThreeDGenerations
// @Tag OpenAI Compatible API
// @Description Text-to-3D generation for Hanzo creative models.
// @router /3d/generations [post]
func (c *ApiController) ThreeDGenerations() {
	token, ok := c.bearerToken()
	if !ok {
		return
	}
	if isPublishableKey(token) {
		c.rejectPublishableKey()
		return
	}

	var req threeDGenerationsRequest
	badReq := ""
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		badReq = fmt.Sprintf("Failed to parse request: %s", parseProblem(err))
	} else if req.Model == "" {
		badReq = "3d request requires a \"model\" field"
	} else if req.Prompt == "" {
		badReq = "3d request requires a \"prompt\" field"
	}
	if badReq != "" {
		if authErr := c.authenticate(token); authErr != nil {
			c.ResponseAuthError(authErr)
			return
		}
		c.ResponseErrorWithStatus(http.StatusBadRequest, badReq)
		return
	}

	startTime := time.Now().UTC()
	orgId := c.GetOrg()
	provider, authUser, _, isPremium, err := c.authResolveProvider(token, req.Model, orgId)
	if err != nil {
		c.ResponseAuthError(err)
		return
	}
	if paying(c.Context(), provider) {
		c.ResponseAuthError(laneOff(c.Context(), req.Model))
		return
	}
	if provider.Type != "Zen" {
		c.ResponseError("model \"" + req.Model + "\" does not serve the /v1/3d/generations endpoint")
		return
	}
	if authUser == nil {
		c.ResponseAuthError(billingError("3D generation requires an authenticated Hanzo Cloud account. Sign in and add credits to your wallet at %s", object.PayURL(c.Host(), "")))
		return
	}
	c.serveZenMedia("3d/generations", req.Model, c.Body(), 1, orgId, authUser, isPremium, startTime)
}
