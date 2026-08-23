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

package controllers

import (
	"encoding/gob"
	"strings"
	"time"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/zap-proto/zip"
)

// ApiController is a request. It embeds the zip context, so a handler reads its
// body, params, query and headers straight from the wire with no second object
// in between.
//
// Identity is NOT taken from the embedded context. zip's User/IsAdmin/Org read
// gateway-set X-User-* headers; ai derives the same facts from a principal it
// verified itself (GetSessionUser, and principalUser -> ParseAndValidateJWT,
// which checks the signature and the iss/aud policy). The methods below shadow
// the embedded ones deliberately: a verified principal is not interchangeable
// with a header.
type ApiController struct {
	*zip.Ctx
}

func init() {
	gob.Register(iam.Claims{})
}

func GetUserName(user *iam.User) string {
	if user == nil {
		return ""
	}

	return user.Name
}

// Who this request is, and there is one answer.
//
// The credential is the IAM access token: an Authorization bearer, or the
// first-party cookie a browser sign-in left (setIamTokenCookie). Both carry the
// SAME signed token, and it is verified here — signature plus the iss/aud policy
// — so identity is never taken on trust and a missing or unreadable credential
// grants nothing.
//
// There is no session store behind this. There used to be, and the id it handed
// the browser was itself a credential: a value an attacker could plant and then
// have the victim's sign-in authenticate. A signed token cannot be fixated that
// way — the client cannot choose one that verifies — so rotating an id on the way
// in is no longer a property to remember, it is a shape the credential does not
// have. IAM mints and revokes; ai reads.
func (c *ApiController) GetSessionClaims() *iam.Claims {
	token := bearerToken(c.Header("Authorization"), c.Fiber().Cookies(iamTokenCookieName))
	if token == "" {
		return nil
	}
	claims, err := object.ParseAndValidateJWT(token)
	if err != nil {
		return nil
	}
	return claims
}

// SetSessionClaims writes who the caller is for the rest of their visit, as the
// cookie that carries their token. A nil claim ends the visit.
//
// It takes claims rather than a token because the callers hold claims; the token
// they were parsed from rides along on AccessToken, and that is what the browser
// gets back.
func (c *ApiController) SetSessionClaims(claims *iam.Claims) {
	if claims == nil {
		c.clearIamTokenCookie()
		return
	}
	if claims.AccessToken != "" {
		c.setIamTokenCookie(claims.AccessToken, claims.ExpiresAt.Time)
	}
}

// GetSessionUser is the caller, or nil when the request carries no credential
// this process can verify.
func (c *ApiController) GetSessionUser() *iam.User {
	claims := c.GetSessionClaims()
	if claims == nil {
		return nil
	}
	return &claims.User
}

// SetSessionUser refreshes the identity on the visit's own credential. A nil
// user ends it.
func (c *ApiController) SetSessionUser(user *iam.User) {
	if user == nil {
		c.SetSessionClaims(nil)
		return
	}
	claims := c.GetSessionClaims()
	if claims == nil {
		return
	}
	claims.User = *user
	c.SetSessionClaims(claims)
}

func (c *ApiController) GetSessionUsername() string {
	user := c.GetSessionUser()
	if user == nil {
		return ""
	}

	return GetUserName(user)
}

func (c *ApiController) GetRequestTenantOrgID() string {
	if c == nil || c.Ctx == nil {
		return ""
	}
	return strings.TrimSpace(c.Header("X-Org-Id"))
}

func (c *ApiController) GetRequestTenantProjectID() string {
	if c == nil || c.Ctx == nil {
		return ""
	}
	// Canonical project sub-scope (what console2 stamps); the X-IAM- variant was
	// never sent.
	return strings.TrimSpace(c.Header("X-Project-Id"))
}

// GetSessionOwner returns the organization (owner) of the authenticated user.
// This ensures multi-tenant resource scoping — users only see their own org's resources.
func (c *ApiController) GetSessionOwner() string {
	user := c.GetSessionUser()
	if user != nil {
		return user.Owner
	}
	return ""
}

// RequireSessionOwner ensures the caller is authenticated and returns their org owner.
// IAM headers are trusted when injected by the gateway, but session auth is primary here.
func (c *ApiController) RequireSessionOwner() (string, bool) {
	user := c.GetSessionUser()
	if user != nil {
		return user.Owner, true
	}
	c.ResponseUnauthorized(c.T("auth:Please sign in first"))
	return "", false
}

// GetScopedOwner resolves owner from the authenticated session.
// Non-admin users are always scoped to their own org, ignoring request owner params.
// Super admins can optionally target a specific owner via query parameter.
func (c *ApiController) GetScopedOwner() (string, bool) {
	user, ok := c.RequireSignedInUser()
	if !ok {
		return "", false
	}

	return util.ScopeOwner(user.Owner, c.Input().Get("owner")), true
}

// EnforceStoreIsolation enforces store isolation based on user's Homepage field.
// Returns the enforced store name and true if isolation check passes, or empty string and false if access denied.
func (c *ApiController) EnforceStoreIsolation(requestedStoreName string) (string, bool) {
	user := c.GetSessionUser()
	if user == nil || user.Homepage == "" {
		// No user or no Homepage binding, no isolation
		return requestedStoreName, true
	}

	// User is bound to a specific store via Homepage
	if requestedStoreName == "" || requestedStoreName == "All" {
		// Force the store to be their bound store
		return user.Homepage, true
	} else if requestedStoreName != user.Homepage {
		// User is trying to access a different store, deny access
		c.ResponseError(c.T("controllers:You can only access data from your assigned store"))
		return "", false
	}

	return requestedStoreName, true
}

// FilterStoresByHomepage filters stores based on user's Homepage field.
func FilterStoresByHomepage(stores []*object.Store, user *iam.User) []*object.Store {
	if user == nil || user.Homepage == "" {
		// No Homepage binding, return all stores
		return stores
	}

	// Check if Homepage matches any store name
	var filteredStores []*object.Store
	for _, store := range stores {
		if store.Name == user.Homepage {
			filteredStores = append(filteredStores, store)
			break
		}
	}

	// If Homepage matches a store, only return that store
	if len(filteredStores) > 0 {
		return filteredStores
	}

	// If Homepage doesn't match any store, return all stores (no isolation)
	return stores
}

func wrapActionResponse(affected bool, e ...error) *Response {
	if len(e) != 0 && e[0] != nil {
		return &Response{Status: "error", Msg: e[0].Error()}
	} else if affected {
		return &Response{Status: "ok", Msg: "", Data: "Affected"}
	} else {
		return &Response{Status: "ok", Msg: "", Data: "Unaffected"}
	}
}

func wrapActionResponse2(affected bool, data interface{}, e ...error) *Response {
	if len(e) != 0 && e[0] != nil {
		return &Response{Status: "error", Msg: e[0].Error()}
	} else if affected {
		return &Response{Status: "ok", Msg: "", Data: "Affected", Data2: data}
	} else {
		return &Response{Status: "ok", Msg: "", Data: "Unaffected", Data2: data}
	}
}

func (c *ApiController) Finish() {
	if strings.HasPrefix(c.Path(), "/api") {
		startTime := c.Locals("startTime")
		if startTime != nil {
			latency := time.Since(startTime.(time.Time)).Milliseconds()
			object.ApiLatency.WithLabelValues(c.Path(), c.Method()).Observe(float64(latency))
		}
	}
	c.errorLogFilter()
}

func (c *ApiController) errorLogFilter() {
	if c.Fiber().Response().StatusCode() >= 400 {
		method := c.Method()
		path := c.Path()
		query := object.RedactQuery(string(c.Fiber().Request().URI().QueryString()))
		body := object.RedactBody(string(c.Body()))
		if len(body) > 4096 {
			body = body[:4096] + "...(truncated)"
		}
		// Never the raw header: this logged 417 replayable user JWTs in
		// production. A fingerprint still correlates repeated failures from
		// one credential without being usable. See logredact.go.
		token := object.RedactCredential(c.Header("Authorization"))
		// What went out, not a second rendering of it.
		respStr := string(c.Fiber().Response().Body())
		if len(respStr) > 4096 {
			respStr = respStr[:4096] + "...(truncated)"
		}
		log.Error("API error: method=%s path=%s query=%s token=%s body=%s response=%s", method, path, query, token, body, respStr)
	}
}
