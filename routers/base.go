// Copyright 2023-2025 Hanzo AI Inc.. All Rights Reserved.
// Portions Copyright 2024 The OpenAgent Authors. All Rights Reserved.
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
	"net/http"
	"strings"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/i18n"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/util"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/ai/controllers"
)

type Response struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   interface{} `json:"data"`
	Data2  interface{} `json:"data2"`
}

// GetSessionUser is the identity a FILTER sees, and it is the same identity the
// handler sees: the request's verified IAM token, read by the one function that
// reads it. Filters and handlers cannot disagree about who is calling.
//
// It used to read a per-pod in-memory session store, and its own comment recorded
// the cost: the store is filled by a session hook that does not run when the
// embedded binary serves these routes, so this answered nil on every request and
// the filters billed and rate-limited by the raw key instead. A store that is nil
// in production is not an identity source. The token always travels with the
// request.
func GetSessionUser(c *zip.Ctx) *iam.User {
	if c == nil {
		return nil
	}
	return (&controllers.ApiController{Ctx: c}).GetSessionUser()
}

// getUsername is who to attribute a recorded request to.
//
// The token, and only the token. It used to fall back to comparing a clientId and
// clientSecret out of the query string against the configured pair — a credential
// check of our own, for an attribution string. IAM mints credentials; this reads
// the one presented.
func getUsername(c *zip.Ctx) string {
	user := GetSessionUser(c)
	if user == nil {
		return ""
	}
	return util.GetIdFromOwnerAndName(user.Owner, user.Name)
}

// responseError writes the standard error envelope with its status, translated
// into the caller's language.
//
// The status is a REQUIRED argument. It used to default to 200 with a second
// function beside it for callers who wanted a real one — so a filter could deny a
// request and still answer success, which is exactly what that second function's
// comment warned about. One function, and every caller says what it means.
func responseError(c *zip.Ctx, status int, msg string, data ...interface{}) error {
	language := c.Header("Accept-Language")
	if len(language) > 2 {
		language = language[0:2]
	}
	language = conf.GetLanguage(language)

	translated := msg
	if strings.Contains(msg, ":") {
		translated = i18n.Translate(language, msg)
	}

	resp := Response{Status: "error", Msg: translated}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	return c.JSON(status, resp)
}

// denyUnauthorized renders a 401: no credential, or one that did not verify.
//
// Returning the write WITHOUT continuing is the denial. A filter that wrote a body
// and then let the chain run would have the handler answer over the top of it.
func denyUnauthorized(c *zip.Ctx, msg string, data ...interface{}) error {
	return responseError(c, http.StatusUnauthorized, msg, data...)
}

// denyForbidden renders a 403: verified, and not permitted.
func denyForbidden(c *zip.Ctx, msg string, data ...interface{}) error {
	return responseError(c, http.StatusForbidden, msg, data...)
}

// parseBearerToken reads the credential the caller presented.
func parseBearerToken(c *zip.Ctx) string {
	prefix, token, ok := strings.Cut(c.Header("Authorization"), " ")
	if !ok || prefix != "Bearer" {
		return ""
	}
	return token
}
