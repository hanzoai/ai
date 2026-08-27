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
	"context"
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

	// Which organization this request acts in, answered once. Resolving it
	// validates the caller's credential, and for an IAM API key it asks IAM — so a
	// handler that asks four times pays four times, and if IAM answers one of them
	// and not the next, one request is attributed to two tenants. A controller is
	// built per request (routers/route.go) and nothing that outlives the request
	// reaches through it, so this is that request's own answer for as long as the
	// caller is the same one.
	org      string
	orgKnown bool

	// And who the caller is, for the same reason. Answering parses the credential
	// and verifies its signature — twice per ask, since the issuer and audience are
	// checked from the token again — and one handler asks six times. Dropped
	// alongside the organization, by the one call that changes the answer.
	claims      *iam.Claims
	claimsKnown bool
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
	if c.claimsKnown {
		return c.claims
	}
	c.claims, c.claimsKnown = c.resolveClaims(), true
	return c.claims
}

func (c *ApiController) resolveClaims() *iam.Claims {
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
	// Who the caller is decides which organization the request acts in, so both
	// answers above stop being this request's answers here.
	c.orgKnown, c.claimsKnown = false, false

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

// reach answers how far a listing may see: one organization, or every one of
// them. The reserved org reaches every tenant, so its answer is the empty filter;
// every other caller reaches only their own org. Both surfaces use it, which is why
// it takes the resolved user rather than reading a session.
// reach answers WHICH organization a caller reads within: the empty string for the
// reserved org, which narrows to nothing and so reads them all, and the caller's own
// otherwise. It needs a caller — there is no organization without one, and the empty
// string already means every organization, so it cannot double as "nobody".
func reach(user *iam.User) string {
	if util.IsSuperAdmin(user) {
		return ""
	}
	return user.Owner
}

// snapshot is the part of a request that outlives it.
//
// A streamed body is produced by fasthttp draining the writer from its own
// goroutine, after the handler has returned and fiber has released the request
// context. Everything a stream writer needs is therefore taken while the request
// is still ours and carried in — reaching back through the controller there
// dereferences a released context, in a goroutine whose panic the router never
// sees.
type snapshot struct {
	lang string
	ip   string
	org  string
	ctx  context.Context
}

// takeSnapshot reads the request's outliving parts. Call it before handing a
// closure to SendStreamWriter, never inside one.
func (c *ApiController) takeSnapshot(user *iam.User) snapshot {
	// A snapshot exists to be read after the handler returns, and each of these
	// begins life as a view of the connection's buffer. Cloned, they are ours.
	return snapshot{
		lang: strings.Clone(c.GetAcceptLanguage()),
		ip:   strings.Clone(c.Fiber().IP()),
		org:  strings.Clone(c.billingOrg(user)),
		ctx:  context.WithoutCancel(c.Context()),
	}
}

// whose answers which person's rows a listing is for.
//
// The name arrives three ways — a user parameter, a selected user, and a
// field/value pair naming "user" — so it is resolved here rather than per branch
// and per surface. An admin may name someone; anyone else gets their own rows
// whichever way they asked, which is what the third way used to skip. reach()
// then confines the answer to the caller's own tenant, so an admin naming a name
// is an admin of THAT name's organization or of nobody.
//
// A surface with no field/value pair passes both empty.
func whose(caller *iam.User, user, selectedUser, field, value string) string {
	who := user
	if field == "user" {
		who = value
	}
	if selectedUser != "" && selectedUser != "null" {
		who = selectedUser
	}
	if !util.IsAdmin(caller) {
		who = caller.Name
	}
	return who
}

// reaches reports whether a caller may act on a row owned by owner. It is reach()
// asked about a single row rather than a whole listing: an empty reach covers
// every tenant, any other reach covers only itself.
func reaches(user *iam.User, owner string) bool {
	if user == nil {
		return false
	}
	r := reach(user)
	return r == "" || r == owner
}

// Whether the caller reaches the row just read, for the reads that name one by
// id. A row they do not reach answers the way a row that is not there answers, so
// an id says nothing about what exists outside their own organization. The filter
// these sit behind asks whether the caller administers an organization, not which
// one, so the row is asked here.
// The rows a listing named "global" may show this caller: the whole table for the
// reserved org, the caller's own organization for everybody else.
func within[T any](c *ApiController, all func() ([]*T, error), mine func(string) ([]*T, error)) ([]*T, bool) {
	caller, ok := c.RequireSignedInUser()
	if !ok {
		return nil, false
	}
	var rows []*T
	var err error
	if org := reach(caller); org == "" {
		rows, err = all()
	} else {
		rows, err = mine(org)
	}
	if err != nil {
		c.ResponseError(err.Error())
		return nil, false
	}
	return rows, true
}

func reachable(c *ApiController, owner string) bool {
	if reaches(c.GetSessionUser(), owner) {
		return true
	}
	c.ResponseOk(nil)
	return false
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

// notYourStore is what a caller bound to one store is told when they ask for
// another. Both surfaces say it; only the shape of the saying differs.
const notYourStore = "controllers:You can only access data from your assigned store"

// bound answers which store a caller may read, and whether they asked for one
// they may not.
//
// A user bound to a store through Homepage reads that store and no other: asking
// for none, or for "All", gives them theirs, and naming a different one is
// refused. A user with no binding reads whatever they asked for.
func bound(user *iam.User, requested string) (string, bool) {
	if user == nil || user.Homepage == "" {
		return requested, true
	}
	if requested == "" || requested == "All" {
		return user.Homepage, true
	}
	if requested != user.Homepage {
		return "", false
	}
	return requested, true
}

// EnforceStoreIsolation applies that rule and refuses on the request.
func (c *ApiController) EnforceStoreIsolation(requestedStoreName string) (string, bool) {
	store, ok := bound(c.GetSessionUser(), requestedStoreName)
	if !ok {
		c.ResponseError(c.T(notYourStore))
	}
	return store, ok
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
