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

import (
	"strings"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam"
)

// principalUser resolves the request principal: the session user (cookie auth)
// if present, else the VERIFIED Bearer JWT user (signature + issuer/audience
// validated via object.ParseAndValidateJWT). Returns nil for an unauthenticated
// or provider/widget-key request. This is the one identity source the org
// resolver trusts — never a raw client header.
func (c *ApiController) principalUser() *iam.User {
	if u := c.GetSessionUser(); u != nil {
		return u
	}
	token := bearerTokenFromRequest(c.Ctx.Request)
	if token == "" || !isJwtToken(token) {
		return nil
	}
	claims, err := object.ParseAndValidateJWT(token)
	if err != nil {
		return nil
	}
	return &claims.User
}

// GetEffectiveOrg resolves the organization for data-scoping and pricing from
// the VERIFIED request principal — never a raw client header.
//
// X-IAM-Org-Id is honored ONLY when it matches the authenticated principal's own
// org, or the principal is a global admin (cross-org platform access). A
// non-admin can never act as another org via a spoofed header; an
// unauthenticated caller's header is ignored. Behind the gateway the injected
// header equals the JWT owner, so the gateway path resolves identically.
//
// Note: chat/embeddings BILLING is keyed on the validated authUser.Owner, not on
// this value — this governs routing, pricing, usage reads, and record
// attribution, all of which must also be tenant-safe.
func (c *ApiController) GetEffectiveOrg() string {
	requested := strings.TrimSpace(c.Ctx.Input.Header("X-IAM-Org-Id"))

	user := c.principalUser()
	if user != nil && user.Owner != "" {
		if requested != "" && (requested == user.Owner || util.IsGlobalAdmin(user)) {
			return requested
		}
		return user.Owner
	}

	// No verified principal: never trust a client-supplied org header.
	return conf.GetConfigString("IAM_ORG")
}
