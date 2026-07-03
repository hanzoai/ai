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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/beego/beego"
	"github.com/beego/beego/context"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam"
)

type Response struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   interface{} `json:"data"`
	Data2  interface{} `json:"data2"`
}

func (c *ApiController) ResponseOk(data ...interface{}) {
	resp := Response{Status: "ok"}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	c.Data["json"] = resp
	c.ServeJSON()
}

func (c *ApiController) ResponseError(error string, data ...interface{}) {
	resp := Response{Status: "error", Msg: error}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	c.Data["json"] = resp
	c.ServeJSON()
}

// ResponseErrorWithStatus writes the standard error envelope with an explicit
// HTTP status code. Plain ResponseError emits HTTP 200, which is wrong for auth
// failures; the OpenAI-compatible /v1 handlers use this so a missing or invalid
// Bearer token returns 401, matching /v1/models. Beego's Output.Body honors a
// status set via SetStatus, so the body shape is unchanged.
func (c *ApiController) ResponseErrorWithStatus(status int, error string, data ...interface{}) {
	c.Ctx.Output.SetStatus(status)
	c.ResponseError(error, data...)
}

// apiError carries an HTTP status alongside the message so the OpenAI-compatible
// handlers map auth / billing / validation failures to the right code instead of
// Beego's default 200 (ServeJSON never sets a status). The shared auth+routing
// policy (authResolveProvider and the resolveProvider* functions it composes)
// returns exactly one category per failure, so the status is unambiguous:
//
//	authError    401  unknown / invalid key, bad JWT, IAM lookup failure
//	billingError 402  valid key, insufficient / starter-only balance
//	modelError   400  valid key, model not in the routing table
//	serverError  500  provider misconfig or balance lookup transport failure
//
// Fail-secure: an untyped error reaching statusOf defaults to 401 (deny), never
// 200 (grant).
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func authError(format string, a ...interface{}) error {
	return &apiError{http.StatusUnauthorized, fmt.Sprintf(format, a...)}
}

func billingError(format string, a ...interface{}) error {
	return &apiError{http.StatusPaymentRequired, fmt.Sprintf(format, a...)}
}

func modelError(format string, a ...interface{}) error {
	return &apiError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

func serverError(format string, a ...interface{}) error {
	return &apiError{http.StatusInternalServerError, fmt.Sprintf(format, a...)}
}

// statusOf returns the HTTP status carried by an apiError, or 401 for an untyped
// error reaching an auth-gated handler (fail-secure: deny, never grant).
func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status
	}
	return http.StatusUnauthorized
}

// wrapAuth tags an untyped error as a 401 auth failure, but leaves an already
// typed apiError untouched — so the widget / provider-key branches of
// authResolveProvider fail closed as 401 while the IAM / JWT branches keep the
// precise 400 / 402 / 500 status produced deeper in the routing policy.
func wrapAuth(err error) error {
	if err == nil {
		return nil
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return err
	}
	return &apiError{http.StatusUnauthorized, err.Error()}
}

// ResponseAuthError renders an error from the auth / routing path with its
// carried HTTP status (401 / 402 / 400 / 500). It never emits 200, so an invalid
// key, an empty balance, or a bad model is unambiguous to OpenAI-compatible
// clients. This is the ONE renderer for that surface (chat, embeddings, rerank).
func (c *ApiController) ResponseAuthError(err error) {
	c.ResponseErrorWithStatus(statusOf(err), err.Error())
}

// ResponseUnauthorized renders an authentication denial (no/invalid session or
// credential) as a real HTTP 401 — never Beego's default 200. Same body shape.
func (c *ApiController) ResponseUnauthorized(error string, data ...interface{}) {
	c.ResponseErrorWithStatus(http.StatusUnauthorized, error, data...)
}

// ResponseForbidden renders an authorization denial (authenticated but not
// permitted) as a real HTTP 403 — never Beego's default 200. Same body shape.
func (c *ApiController) ResponseForbidden(error string, data ...interface{}) {
	c.ResponseErrorWithStatus(http.StatusForbidden, error, data...)
}

func (c *ApiController) T(error string) string {
	return i18n.Translate(c.GetAcceptLanguage(), error)
}

func (c *ApiController) ResponseAudio(audioData []byte, contentType string, filename string) {
	if contentType == "" {
		contentType = "audio/mp3"
	}
	if filename == "" {
		filename = "audio.mp3"
	}

	c.Ctx.Output.Header("Content-Type", contentType)
	c.Ctx.Output.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	err := c.Ctx.Output.Body(audioData)
	if err != nil {
		responseError(c.Ctx, err.Error())
	}
}

func (c *ApiController) GetAcceptLanguage() string {
	language := c.Ctx.Request.Header.Get("Accept-Language")
	if len(language) > 2 {
		language = language[0:2]
	}
	return conf.GetLanguage(language)
}

func (c *ApiController) RequireSignedIn() (string, bool) {
	userId := c.GetSessionUsername()
	if userId == "" {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return "", false
	}
	return userId, true
}

func (c *ApiController) RequireSignedInUser() (*iam.User, bool) {
	user := c.GetSessionUser()
	if user == nil {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return nil, false
	}
	return user, true
}

func (c *ApiController) CheckSignedIn() (string, bool) {
	userId := c.GetSessionUsername()
	if userId == "" {
		return "", false
	}
	return userId, true
}

func (c *ApiController) RequireAdmin() bool {
	disablePreviewMode, _ := beego.AppConfig.Bool("disablePreviewMode")
	if !disablePreviewMode {
		return true
	}

	if !c.IsAdmin() {
		c.ResponseForbidden(c.T("auth:this operation requires admin privilege"))
		return false
	}

	return true
}

// RequireGlobalAdmin is the controller-level self-guard for platform-sensitive
// endpoints (provider-admin, upstream-key/topology config). It mirrors the authz
// filter's globalAdminEndpoints gate EXACTLY — same principal (session or VERIFIED
// Bearer JWT via c.principalUser) and same policy (util.IsGlobalAdmin) — so it is
// belt-AND-suspenders: even if the filter is ever bypassed (e.g. a path-normalization
// disagreement), the controller still refuses. Fail-closed: no principal → 401,
// authenticated non-global-admin → 403. Unlike RequireAdmin it is NOT relaxed by
// preview mode and checks GLOBAL (platform) admin, not org admin — these routes
// govern the primary provider that backs the whole model catalog.
func (c *ApiController) RequireGlobalAdmin() bool {
	user := c.principalUser()
	if user == nil {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return false
	}
	if !util.IsGlobalAdmin(user) {
		c.ResponseForbidden(c.T("auth:this operation requires global admin privilege"))
		return false
	}
	return true
}

func (c *ApiController) IsPreviewMode() bool {
	disablePreviewMode, _ := beego.AppConfig.Bool("disablePreviewMode")
	return !disablePreviewMode
}

func (c *ApiController) IsAdmin() bool {
	user := c.GetSessionUser()
	return util.IsAdmin(user)
}

func DenyRequest(ctx *context.Context) {
	ctx.Output.SetStatus(http.StatusForbidden)
	responseError(ctx, "auth:Unauthorized operation")
}

func responseError(ctx *context.Context, error string, data ...interface{}) {
	// Get language from Accept-Language header
	language := ctx.Request.Header.Get("Accept-Language")
	if len(language) > 2 {
		language = language[0:2]
	}
	language = conf.GetLanguage(language)

	// Translate error message if it contains namespace prefix
	translatedError := error
	if strings.Contains(error, ":") {
		translatedError = i18n.Translate(language, error)
	}

	resp := Response{Status: "error", Msg: translatedError}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}

	err := ctx.Output.JSON(resp, true, false)
	if err != nil {
		panic(err)
	}
}

func isIpAddress(host string) bool {
	// Attempt to split the host and port, ignoring the error
	hostWithoutPort, _, err := net.SplitHostPort(host)
	if err != nil {
		// If an error occurs, it might be because there's no port
		// In that case, use the original host string
		hostWithoutPort = host
	}

	// Attempt to parse the host as an IP address (both IPv4 and IPv6)
	ip := net.ParseIP(hostWithoutPort)
	// if host is not nil is an IP address else is not an IP address
	return ip != nil
}

func getOriginFromHost(host string) string {
	protocol := "https://"
	if !strings.Contains(host, ".") {
		// "localhost:14000"
		protocol = "http://"
	} else if isIpAddress(host) {
		// "192.168.0.10"
		protocol = "http://"
	}

	return fmt.Sprintf("%s%s", protocol, host)
}

func removeHtmlTags(s string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return re.ReplaceAllString(s, "")
}

func getContentHash(content string) string {
	hasher := sha256.New()
	hasher.Write([]byte(content))

	res := hex.EncodeToString(hasher.Sum(nil))
	res = res[:8]
	return res
}

func (c *ApiController) getClientIp() string {
	res := strings.Replace(util.GetIPFromRequest(c.Ctx.Request), ": ", "", -1)
	return res
}

func (c *ApiController) getUserAgent() string {
	res := c.Ctx.Request.UserAgent()
	return res
}

func (c *ApiController) IsCurrentUser(usernameInput string) bool {
	username := c.GetSessionUsername()
	if username == "" && c.getAnonymousUsername() == usernameInput {
		username = c.getAnonymousUsername()
	}

	if !c.IsAdmin() && username != usernameInput {
		c.ResponseForbidden(c.T("auth:Unauthorized operation"))
		return false
	}
	return true
}
