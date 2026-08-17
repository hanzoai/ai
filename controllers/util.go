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

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/i18n"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/zap-proto/zip"
)

type Response struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Code   string      `json:"code,omitempty"` // machine name of the failure; clients switch on this, never on Msg
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
	c.JSON(http.StatusOK, resp)
}

// ResponseError writes the envelope with HTTP 200: the admin contract, where the
// envelope's own Status field carries the failure and the transport says nothing.
// The React admin reads that field, so this is the shape it expects.
func (c *ApiController) ResponseError(error string, data ...interface{}) {
	c.ResponseErrorWithStatus(http.StatusOK, error, data...)
}

// ResponseErrorWithStatus writes the envelope with an explicit HTTP status, which
// is what the OpenAI-compatible /v1 handlers need: a missing or invalid Bearer
// token is a 401 on the wire, not a 200 carrying a sad sentence.
//
// The status is an ARGUMENT of the write, and has to be. This used to set it on
// the response and then call ResponseError to write the body — but a write takes
// the status too, so the second one silently replaced the first with 200 and every
// refusal in the service answered OK. A denial that arrives as a success is worse
// than an outage: the client reads 200 and believes it.
func (c *ApiController) ResponseErrorWithStatus(status int, error string, data ...interface{}) {
	resp := Response{Status: "error", Msg: error}
	switch len(data) {
	case 2:
		resp.Data2 = data[1]
		fallthrough
	case 1:
		resp.Data = data[0]
	}
	c.JSON(status, resp)
}

// apiError carries an HTTP status alongside the message so the OpenAI-compatible
// handlers map auth / billing / validation failures to the right code instead of
// The router's default 200 (ServeJSON never sets a status). The shared auth+routing
// policy (authResolveProvider and the resolveProvider* functions it composes)
// returns exactly one category per failure, so the status is unambiguous:
//
//	authError    401  unknown / invalid key, bad JWT, IAM lookup failure
//	billingError 402  valid key, insufficient / starter-only balance
//	forbiddenError 403  valid principal, org selection they may not bill
//	modelError   400  valid key, model not in the routing table
//	supplyError  503  the caller is fine; WE cannot buy the inference right now
//	serverError  500  provider misconfig or balance lookup transport failure
//
// busyError (429) also travels this way but is not an auth outcome: it is raised
// AFTER the policy has admitted the caller, by the capacity check that stands
// between a valid request and work the service cannot currently do.
//
// Fail-secure: an untyped error reaching statusOf defaults to 401 (deny), never
// 200 (grant).
type apiError struct {
	status int
	msg    string
	// code names the failure for a program. A message is written for a person and
	// gets rewritten; a status answers several different questions at once (503 is
	// both "our upstream refused" and "we could not read your balance"). Only this
	// is safe to branch on, which is why the shapes that need telling apart carry
	// one and the rest carry nothing rather than something invented.
	code string
}

func (e *apiError) Error() string { return e.msg }

func authError(format string, a ...interface{}) error {
	return &apiError{status: http.StatusUnauthorized, msg: fmt.Sprintf(format, a...)}
}

func billingError(format string, a ...interface{}) error {
	return &apiError{status: http.StatusPaymentRequired, msg: fmt.Sprintf(format, a...), code: object.CodeInsufficientBalance}
}

// codeSupply names the refusal where WE cannot buy: our account with a vendor is
// spent, or our own cash breaker is holding. The caller's wallet is untouched.
//
// It is a separate code from CodeInsufficientBalance rather than a separate status
// alone, because the status is what a person sees and the code is what a program
// acts on. A client that switches on insufficient_balance offers a top-up — which
// is the right move for a caller who owes money and exactly the wrong one for a
// caller who owes nothing and is being told to pay our bill.
const codeSupply = "supply_unavailable"

// supplyError is 503: the request is well formed, the credential is valid, the
// balance is funded, and we still cannot serve it because OUR side cannot pay.
//
// Not 402. 402 says the caller owes money, which is false here, and a client that
// acts on it goes and tops up a balance that is already funded — the expensive
// half of this being wrong. Not 500 either: nothing is broken, the condition
// clears on its own or when somebody tops an account up, and 503 is the status
// that says "try again" rather than "file a bug".
func supplyError(format string, a ...interface{}) error {
	return &apiError{status: http.StatusServiceUnavailable, msg: fmt.Sprintf(format, a...), code: codeSupply}
}

// forbiddenError is 403: the credential is VALID and the caller is known — they
// simply may not act in the org they asked for. Distinct from authError (401,
// "who are you") and billingError (402, "you owe money"), because answering an
// unauthorized org selection with either of those tells the caller something
// untrue about their own credential. Unauthorized and nonexistent orgs share
// this one status so the ask cannot enumerate orgs.
func forbiddenError(format string, a ...interface{}) error {
	return &apiError{status: http.StatusForbidden, msg: fmt.Sprintf(format, a...)}
}

func modelError(format string, a ...interface{}) error {
	return &apiError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, a...)}
}

func serverError(format string, a ...interface{}) error {
	return &apiError{status: http.StatusInternalServerError, msg: fmt.Sprintf(format, a...)}
}

// busyError is 429: the credential is valid, the request is well formed, and
// there is simply no capacity for it right now. Distinct from billingError (402,
// "you owe money") because the caller owes nothing, and from serverError (500)
// because nothing is broken — the condition is transient and, for a caller at its
// own share, one the caller itself can clear. The message names which of those
// two it is (speech_admission.go).
func busyError(format string, a ...interface{}) error {
	return &apiError{status: http.StatusTooManyRequests, msg: fmt.Sprintf(format, a...)}
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

// codeOf returns the machine name a failure carries, or "" for one that has none.
func codeOf(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.code
	}
	return ""
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
	return &apiError{status: http.StatusUnauthorized, msg: err.Error()}
}

// ResponseAuthError renders an error from the auth / routing path with its
// carried HTTP status (401 / 402 / 400 / 500). It never emits 200, so an invalid
// key, an empty balance, or a bad model is unambiguous to OpenAI-compatible
// clients. This is the ONE renderer for that surface (chat, embeddings, rerank).
func (c *ApiController) ResponseAuthError(err error) {
	c.ResponseFailure(err)
}

// ResponseFailure renders a typed failure with the status AND the machine name it
// carries. ResponseError writes neither: it answers 200 with a message, so a
// completion that no provider could serve reached clients as a success whose body
// had no choices in it.
func (c *ApiController) ResponseFailure(err error) {
	c.JSON(statusOf(err), Response{Status: "error", Msg: err.Error(), Code: codeOf(err)})
}

// ResponseUnauthorized renders an authentication denial (no/invalid session or
// credential) as a real HTTP 401 — never The router's default 200. Same body shape.
func (c *ApiController) ResponseUnauthorized(error string, data ...interface{}) {
	c.ResponseErrorWithStatus(http.StatusUnauthorized, error, data...)
}

// ResponseForbidden renders an authorization denial (authenticated but not
// permitted) as a real HTTP 403 — never The router's default 200. Same body shape.
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

	c.SetHeader("Content-Type", contentType)
	c.SetHeader("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	err := c.Bytes(http.StatusOK, audioData)
	if err != nil {
		responseError(c.Ctx, http.StatusInternalServerError, err.Error())
	}
}

func (c *ApiController) GetAcceptLanguage() string {
	language := c.Header("Accept-Language")
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

// RequirePrincipal resolves the request principal from EITHER the browser session
// cookie OR a verified Bearer JWT (c.principalUser), returning a real 401 when
// neither is present. It is the Bearer-aware sibling of RequireSignedInUser: an
// endpoint that must serve BOTH the console (session cookie) and the token-bearing
// surfaces (app / chat / billing, which carry an IAM Bearer, not the console
// cookie) with the SAME resolved identity uses this. The Bearer branch is
// signature- AND issuer/audience-validated via object.ParseAndValidateJWT (never
// raw iam.ParseJwtToken), so a forged token cannot pose as anyone.
//
// The returned user is the SOLE authority for any downstream org/role scope
// decision — the caller MUST derive scope from THIS user (its Owner, its role via
// util.IsSuperAdmin), NEVER from a request header or query param. That is what
// keeps a Bearer-reachable, org-scoped read tenant-safe.
func (c *ApiController) RequirePrincipal() (*iam.User, bool) {
	user := c.principalUser()
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
	// conf.IsPreviewMode() is env-first (DISABLE_PREVIEW_MODE), so flipping the
	// lever on the deployed CR makes this enforce real admin with no rebuild.
	if conf.IsPreviewMode() {
		return true
	}

	if !c.IsAdmin() {
		c.ResponseForbidden(c.T("auth:this operation requires admin privilege"))
		return false
	}

	return true
}

// RequireSuperAdmin is the controller-level self-guard for platform-sensitive
// endpoints (provider-admin, upstream-key/topology config). It mirrors the authz
// filter's superAdminEndpoints gate EXACTLY — same principal (session or VERIFIED
// Bearer JWT via c.principalUser) and same policy (util.IsSuperAdmin) — so it is
// belt-AND-suspenders: even if the filter is ever bypassed (e.g. a path-normalization
// disagreement), the controller still refuses. Fail-closed: no principal → 401,
// authenticated non-super-admin → 403. Unlike RequireAdmin it is NOT relaxed by
// preview mode and checks GLOBAL (platform) admin, not org admin — these routes
// govern the primary provider that backs the whole model catalog.
func (c *ApiController) RequireSuperAdmin() bool {
	user := c.principalUser()
	if user == nil {
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return false
	}
	if !util.IsSuperAdmin(user) {
		c.ResponseForbidden(c.T("auth:this operation requires super admin privilege"))
		return false
	}
	return true
}

func (c *ApiController) IsPreviewMode() bool {
	return conf.IsPreviewMode()
}

func (c *ApiController) IsAdmin() bool {
	user := c.GetSessionUser()
	return util.IsAdmin(user)
}

func DenyRequest(ctx *zip.Ctx) {
	responseError(ctx, http.StatusForbidden, "auth:Unauthorized operation")
}

// responseError writes the translated envelope with an explicit status. The status
// used to arrive by being set on the response and read back here — which works, and
// is the same side channel that made every refusal in this file answer 200 when a
// write that carries its own status came along. Say it at the call.
func responseError(ctx *zip.Ctx, status int, error string, data ...interface{}) {
	// Get language from Accept-Language header
	language := ctx.Header("Accept-Language")
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

	if err := ctx.JSON(status, resp); err != nil {
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
	res := strings.Replace(util.GetIPInfo(c.Fiber().IP()), ": ", "", -1)
	return res
}

func (c *ApiController) getUserAgent() string {
	res := c.Header("User-Agent")
	return res
}

// isName reports whether s is a NAME: the half of an id (`owner/name`) that names a
// row inside its owner, and the half of a billing subject that names a payer inside
// its org. Neither half may be empty and neither may carry the separator.
//
// On the chat plane that makes it a predicate about money. AddTransactionForMessage
// builds the subject it debits by joining a row's Owner to its User — but only when
// the User carries no "/", so "victim/bob" is passed through whole and names another
// tenant's wallet directly. An empty User leaves the subject "admin/", which is the
// admin ORG's own pool wallet: Hanzo's platform account, not any customer's.
func isName(s string) bool {
	return s != "" && !strings.Contains(s, "/")
}

// IsCurrentUser reports whether this request may act as usernameInput, and refuses
// the request when it may not.
//
// It answers for NAMES, on both sides, and only for names. Empty used to pass: an
// unauthenticated request has no session username, so the comparison below read
// "" != "" and every anonymous caller was the current user of the empty user. The
// chat plane then wrote that onto a row whose answer bills `admin/` — the admin org's
// own pool wallet. A usernameInput carrying "/" is not a user either but a whole
// billing subject, and the admin branch below would wave it through.
func (c *ApiController) IsCurrentUser(usernameInput string) bool {
	username := c.GetSessionUsername()
	if username == "" && c.getAnonymousUsername() == usernameInput {
		username = c.getAnonymousUsername()
	}

	if !isName(username) || !isName(usernameInput) {
		c.ResponseForbidden(c.T("auth:Unauthorized operation"))
		return false
	}

	if !c.IsAdmin() && username != usernameInput {
		c.ResponseForbidden(c.T("auth:Unauthorized operation"))
		return false
	}
	return true
}
