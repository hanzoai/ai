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
	"encoding/gob"
	"encoding/json"
	"strings"
	"time"

	"github.com/beego/beego"
	"github.com/beego/beego/logs"
	"github.com/hanzoai/cloud/object"
	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
)

type ApiController struct {
	beego.Controller
}

func init() {
	gob.Register(iamsdk.Claims{})
}

func GetUserName(user *iamsdk.User) string {
	if user == nil {
		return ""
	}

	return user.Name
}

func (c *ApiController) GetSessionClaims() *iamsdk.Claims {
	s := c.GetSession("user")
	if s == nil {
		return nil
	}

	claims := s.(iamsdk.Claims)
	return &claims
}

func (c *ApiController) SetSessionClaims(claims *iamsdk.Claims) {
	if claims == nil {
		c.DelSession("user")
		return
	}

	c.SetSession("user", *claims)
}

func (c *ApiController) GetSessionUser() *iamsdk.User {
	claims := c.GetSessionClaims()
	if claims == nil {
		return nil
	}

	return &claims.User
}

func (c *ApiController) SetSessionUser(user *iamsdk.User) {
	if user == nil {
		c.DelSession("user")
		return
	}

	claims := c.GetSessionClaims()
	if claims != nil {
		claims.User = *user
		c.SetSessionClaims(claims)
	}
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
	return strings.TrimSpace(c.Ctx.Input.Header("X-Org-ID"))
}

func (c *ApiController) GetRequestTenantProjectID() string {
	if c == nil || c.Ctx == nil {
		return ""
	}
	return strings.TrimSpace(c.Ctx.Input.Header("X-Project-ID"))
}

// GetSessionOwner returns the organization (owner) of the authenticated user.
// This ensures multi-tenant resource scoping -- users only see their own org's resources.
// Returns empty string if no authenticated session exists. X-Org-ID is NOT trusted
// as a session owner source since it can be spoofed by unauthenticated callers.
func (c *ApiController) GetSessionOwner() string {
	user := c.GetSessionUser()
	if user != nil {
		return user.Owner
	}
	return ""
}

// RequireSessionOwner ensures the caller is authenticated and returns their org owner.
// X-Org-ID header is NOT trusted here -- authentication via session is required.
func (c *ApiController) RequireSessionOwner() (string, bool) {
	user := c.GetSessionUser()
	if user != nil {
		return user.Owner, true
	}
	c.ResponseError(c.T("auth:Please sign in first"))
	return "", false
}

// GetScopedOwner resolves owner from the authenticated session.
// Non-admin users are always scoped to their own org, ignoring request owner params.
// Global admins can optionally target a specific owner via query parameter.
func (c *ApiController) GetScopedOwner() (string, bool) {
	user, ok := c.RequireSignedInUser()
	if !ok {
		return "", false
	}

	if user.Owner == "admin" {
		requestedOwner := strings.TrimSpace(c.Input().Get("owner"))
		if requestedOwner != "" {
			return requestedOwner, true
		}
	}

	return user.Owner, true
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
func FilterStoresByHomepage(stores []*object.Store, user *iamsdk.User) []*object.Store {
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
	if strings.HasPrefix(c.Ctx.Input.URL(), "/api") {
		startTime := c.Ctx.Input.GetData("startTime")
		if startTime != nil {
			latency := time.Since(startTime.(time.Time)).Milliseconds()
			object.ApiLatency.WithLabelValues(c.Ctx.Input.URL(), c.Ctx.Input.Method()).Observe(float64(latency))
		}
	}
	c.errorLogFilter()
	c.Controller.Finish()
}

func (c *ApiController) errorLogFilter() {
	if v, ok := c.Data["json"]; ok {
		var status string
		switch r := v.(type) {
		case Response:
			status = r.Status
		case *Response:
			if r != nil {
				status = r.Status
			}
		default:
			status = ""
		}
		if status == "error" {
			method := c.Ctx.Input.Method()
			path := c.Ctx.Input.URL()
			query := ""
			if c.Ctx.Request != nil && c.Ctx.Request.URL != nil {
				query = c.Ctx.Request.URL.RawQuery
			}
			body := string(c.Ctx.Input.RequestBody)
			if len(body) > 4096 {
				body = body[:4096] + "...(truncated)"
			}
			token := c.Ctx.Request.Header.Get("Authorization")
			respJSON, _ := json.Marshal(v)
			respStr := string(respJSON)
			if len(respStr) > 4096 {
				respStr = respStr[:4096] + "...(truncated)"
			}
			logs.Error("API error: method=%s path=%s query=%s token=%s body=%s response=%s", method, path, query, token, body, respStr)
		}
	}
}
