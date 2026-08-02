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
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// iamTokenCookieName carries the caller's VERIFIED IAM access token (RS256 JWT)
// to the browser as an HttpOnly, Secure, SameSite=Lax first-party cookie. It is
// the self-healing identity credential: the beego session that backs the cookie
// login is the process-local in-memory store, so it is lost on pod restart / GC /
// a request that lands without it — after which get-account used to synthesize an
// anonymous `u-<hash>` user (owner=the real tenant, isAdmin=false), the bug that
// made a signed-in admin resolve to a guest. With this cookie present the
// stateless JWT validator (see bearerTokenFromRequest -> principalUser ->
// object.ParseAndValidateJWT) re-derives the canonical identity, so a real
// authenticated subject is NEVER downgraded to a guest.
const iamTokenCookieName = "hanzo_iam_token"

// accountAction is get-account's identity decision, decoupled from the beego
// controller plumbing so the policy is unit-testable in isolation.
type accountAction int

const (
	// serveIdentity: a resolved (canonical) session identity is present — serve it.
	serveIdentity accountAction = iota
	// serveAnonymous: no identity, but the surface permits an anonymous public
	// user (public domain / preview mode) — synthesize one.
	serveAnonymous
	// failUnauthenticated: no identity on a protected surface — fail LOUD (401),
	// never fabricate a `u-<hash>` record for a would-be authenticated subject.
	failUnauthenticated
)

// resolveAccountAction is the get-account identity policy: a resolved session
// user (INCLUDING one self-healed from a verified IAM credential) is ALWAYS
// served its canonical identity; the anonymous fallback is allowed ONLY on
// public/preview surfaces; every other unauthenticated request fails loud rather
// than impersonating the tenant with a synthesized guest.
func resolveAccountAction(sessionUser *iam.User, anonymousAllowed bool) accountAction {
	if sessionUser != nil {
		return serveIdentity
	}
	if anonymousAllowed {
		return serveAnonymous
	}
	return failUnauthenticated
}

// sessionNeedsSelfHeal reports whether get-account should attempt to re-derive
// the canonical identity from a verified credential: true when there is no
// session identity at all, OR when the session holds only an anonymous guest (an
// earlier anonymousSignin bound a u-<hash> record that must not shadow a real
// signed-in subject). Decoupled from the controller so the precedence rule —
// a verified credential beats a guest session — is unit-testable in isolation.
func sessionNeedsSelfHeal(sessionUser *iam.User) bool {
	return sessionUser == nil || util.IsAnonymousUser(sessionUser)
}

// setIamTokenCookie persists the verified IAM access token as the self-healing
// first-party cookie (see iamTokenCookieName). Host-only + Secure + HttpOnly +
// SameSite=Lax, scoped to the token's own lifetime; the validator re-checks the
// JWT `exp` on read, so an expired cookie simply fails closed to re-auth.
func (c *ApiController) setIamTokenCookie(accessToken string, expiry time.Time) {
	maxAge := 0 // 0 => session cookie (cleared on browser close) when no expiry
	if !expiry.IsZero() {
		if secs := int(time.Until(expiry).Seconds()); secs > 0 {
			maxAge = secs
		}
	}
	http.SetCookie(c.Ctx.ResponseWriter, &http.Cookie{
		Name:     iamTokenCookieName,
		Value:    accessToken,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// InitAuthConfig establishes the IAM signing cert this process validates every
// bearer token against, eagerly. It is retained for callers that WANT to resolve
// up front — a CLI, a test — and returns the failure rather than ending the
// process.
//
// It is deliberately NOT called from an init(). It was, and the cert was fetched
// over the network before any subsystem mounted, so a momentary IAM outage was a
// hard outage of everything in this process with no path back: it panicked, the
// pod crashlooped, and it did not recover on its own once IAM returned. That shape
// reached production twice (see object.AuthReady for both).
//
// Resolution now happens on first use and retries, and a request that cannot be
// authenticated is refused with 503 at the door rather than served without
// authentication. The security property is unchanged; only the cost of an IAM blip
// is — the requests during it, instead of the process.
func InitAuthConfig() error { return object.AuthReady() }

// Signin
// @Title Signin
// @Tag Account API
// @Description sign in
// @Param code  query string true "code of account"
// @Param state query string true "state of account"
// @Success 200 {object} iam.Claims The Response object
// @router /signin [post]
func (c *ApiController) Signin() {
	code := c.Input().Get("code")
	state := c.Input().Get("state")

	// White-label: resolve the IAM client from the request Host so a lux console
	// (console.lux.cloud) exchanges its code as `lux-cloud` against lux.id, a zoo
	// console as `zoo-cloud`, etc. The default (hanzo) resolves to the exact values
	// InitAuthConfig baked, so the hanzo exchange is byte-unchanged. Exchanging a
	// lux code as `hanzo-cloud` is what IAM rejected with "token is for wrong
	// application" -- resolving per-brand is the fix.
	brand := resolveBrandIAM(c.Ctx.Request.Host)
	authClient, err := brandAuthClient(brand)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	token, err := authClient.GetOAuthToken(code, state)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	claims, err := authClient.ParseJwtToken(token.AccessToken)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	if strings.Count(claims.Type, "-") <= 1 {
		if !util.IsAdmin(&claims.User) {
			claims.Type = "chat-user"
		}
	}

	err = c.addInitialChatAndMessage(&claims.User)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	claims.AccessToken = token.AccessToken
	c.SetSessionClaims(claims)
	// Persist the verified access token so identity survives an in-memory session
	// loss: a later get-account whose beego session is gone self-heals from this
	// cookie to the canonical identity instead of falling back to an anonymous
	// u-<hash> user. See iamTokenCookieName + GetAccount.
	c.setIamTokenCookie(token.AccessToken, token.Expiry)
	userId := claims.User.Owner + "/" + claims.User.Name
	c.Ctx.Input.SetParam("recordUserId", userId)

	// Record session ID
	sessionId := c.Ctx.Input.CruSession.SessionID()
	if sessionId != "" && userId != "" {
		session := &object.Session{
			Owner:     claims.User.Owner,
			Name:      claims.User.Name,
			SessionId: []string{sessionId},
		}

		object.AddSession(session)
	}

	c.ResponseOk(claims)
}

// Signout
// @Title Signout
// @Tag Account API
// @Description sign out
// @Success 200 {object} controllers.Response The Response object
// @router /signout [post]
func (c *ApiController) Signout() {
	// Sign-out is idempotent: with no session user (already signed out, or an
	// expired/cleared session) just drop the claims and return OK instead of
	// dereferencing a nil user (which panicked → HTTP 500).
	user := c.GetSessionUser()
	if user != nil {
		_, err := object.DeleteSessionId(util.GetIdFromOwnerAndName(user.Owner, user.Name), c.Ctx.Input.CruSession.SessionID())
		if err != nil {
			c.ResponseError(err.Error())
			return
		}
	}

	c.SetSessionClaims(nil)

	c.ResponseOk()
}

func (c *ApiController) addInitialChat(organization string, userName string, storeName string) (*object.Chat, error) {
	var store *object.Store
	var err error

	if storeName != "" {
		store, err = object.GetStore(util.GetId("admin", storeName))
		if err != nil {
			return nil, err
		}
		if store == nil {
			return nil, fmt.Errorf("%s", fmt.Sprintf(c.T("account:The store: %s is not found"), storeName))
		}
	} else {
		store, err = object.GetDefaultStore("admin")
		if err != nil {
			return nil, err
		}
		if store == nil {
			return nil, fmt.Errorf("%s", c.T("account:The default store is not found"))
		}
	}

	currentTime := util.GetCurrentTime()
	chat := &object.Chat{
		Owner:         "admin",
		Name:          fmt.Sprintf("chat_%s", util.GetRandomName()),
		CreatedTime:   currentTime,
		UpdatedTime:   currentTime,
		Organization:  organization,
		DisplayName:   fmt.Sprintf("New Chat - %d", 1),
		Store:         store.Name,
		ModelProvider: store.ModelProvider,
		Category:      "Default Category",
		Type:          "AI",
		User:          userName,
		User1:         "",
		User2:         "",
		Users:         []string{},
		ClientIp:      c.getClientIp(),
		UserAgent:     c.getUserAgent(),
		MessageCount:  0,
		NeedTitle:     true,
	}

	if store.Welcome != "Hello" {
		chat.DisplayName = fmt.Sprintf("新会话 - %d", 1)
		chat.Category = "默认分类"
	}

	chat.ClientIpDesc = util.GetDescFromIP(chat.ClientIp)
	chat.UserAgentDesc = util.GetDescFromUserAgent(chat.UserAgent)

	_, err = object.AddChat(chat)
	if err != nil {
		return nil, err
	}

	return chat, nil
}

func (c *ApiController) addInitialChatAndMessage(user *iam.User) error {
	chats, err := object.GetChats("admin", "", user.Name)
	if err != nil {
		return err
	}

	if len(chats) != 0 {
		return nil
	}

	organizationName := user.Owner
	userName := user.Name

	chat, err := c.addInitialChat(organizationName, userName, "")
	if err != nil {
		return err
	}

	store, err := object.GetStore(util.GetId("admin", chat.Store))
	if err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("%s", fmt.Sprintf(c.T("account:The store: %s is not found"), chat.Store))
	}

	userMessage := &object.Message{
		Owner:        "admin",
		Name:         fmt.Sprintf("message_%s", util.GetRandomName()),
		CreatedTime:  util.AdjustTimeFromSecToMilli(chat.CreatedTime, -100),
		Organization: chat.Organization,
		Store:        chat.Store,
		User:         userName,
		Chat:         chat.Name,
		ReplyTo:      "",
		Author:       userName,
		Text:         store.Welcome,
		IsHidden:     true,
		VectorScores: []object.VectorScore{},
	}
	_, err = object.AddMessage(userMessage)
	if err != nil {
		return err
	}

	answerMessage := &object.Message{
		Owner:        "admin",
		Name:         fmt.Sprintf("message_%s", util.GetRandomName()),
		CreatedTime:  util.GetCurrentTimeEx(chat.CreatedTime),
		Organization: chat.Organization,
		Store:        chat.Store,
		User:         userName,
		Chat:         chat.Name,
		ReplyTo:      "Welcome",
		Author:       "AI",
		Text:         "",
		VectorScores: []object.VectorScore{},
	}
	_, err = object.AddMessage(answerMessage)
	return err
}

func (c *ApiController) anonymousSignin() {
	username := c.getAnonymousUsername()

	org := c.GetOrg()
	user := iam.User{
		Owner:           org,
		Name:            username,
		CreatedTime:     util.GetCurrentTime(),
		Id:              username,
		Type:            "anonymous-user",
		DisplayName:     "User",
		Avatar:          "https://cdn.hanzo.ai/img/hanzo-cloud-user.png",
		AvatarType:      "",
		PermanentAvatar: "",
		Email:           "",
		EmailVerified:   false,
		Phone:           "",
		CountryCode:     "",
		Region:          "",
		Location:        "",
		Education:       "",
		IsAdmin:         false,
		CreatedIp:       "",
	}

	err := c.addInitialChatAndMessage(&user)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	c.ResponseOk(user)
}

func (c *ApiController) getAnonymousUsername() string {
	clientIp := c.getClientIp()
	userAgent := c.getUserAgent()
	hash := getContentHash(fmt.Sprintf("%s|%s", clientIp, userAgent))
	return fmt.Sprintf("u-%s", hash)
}

func (c *ApiController) isPublicDomain() bool {
	configPublicDomain := conf.GetConfigString("publicDomain")
	if configPublicDomain == "" {
		return false
	}

	if strings.Contains(configPublicDomain, ",") {
		configPublicDomains := strings.Split(configPublicDomain, ",")
		for _, domain := range configPublicDomains {
			if c.Ctx.Request.Host == domain {
				return true
			}
		}
	} else {
		if c.Ctx.Request.Host == configPublicDomain {
			return true
		}
	}

	return false
}

func (c *ApiController) isSafePassword() (bool, error) {
	claims := c.GetSessionClaims()
	if claims == nil {
		return true, nil
	}

	if len(claims.User.Id) != 11 || !strings.HasPrefix(claims.User.Id, "270") {
		return true, nil
	}

	// Use the user data from claims which has been updated with fresh data from IAM in GetAccount()
	if claims.User.Password == "#NeedToModify#" {
		return false, nil
	} else {
		return true, nil
	}
}

// GetAccount
// @Title GetAccount
// @Tag Account API
// @Description get account
// @Success 200 {object} iam.Claims The Response object
// @router /get-account [get]
func (c *ApiController) GetAccount() {
	// Route through the env-first accessor (conf.DisablePreviewMode) so the
	// DISABLE_PREVIEW_MODE lever governs GetAccount too — matching the authz filter
	// and RequireAdmin. Reading beego.AppConfig directly bypassed that lever.
	disablePreviewMode := conf.DisablePreviewMode()
	err := util.AppendWebConfigCookie(c.Ctx)
	if err != nil {
		log.Error("AppendWebConfigCookie: %v", err)
	}

	// Self-heal the session identity from a verified IAM credential (the
	// hanzo_iam_token cookie or an Authorization Bearer JWT) BEFORE deciding
	// identity. The cookie login is backed by the process-local beego session, so
	// two failure modes downgrade a real subject to an anonymous u-<hash> record:
	//   (1) the session was dropped (pod restart / GC) — GetSessionUser() == nil;
	//   (2) an earlier anonymousSignin already bound a guest INTO the session, so
	//       GetSessionUser() returns that guest and principalUser (session-first)
	//       would keep serving it.
	// In BOTH cases, resolve the principal DIRECTLY from the credential (via
	// credentialUser, which bypasses the session) and, when it re-derives a
	// canonical non-anonymous identity, OVERRIDE the session with it — a signed-in
	// subject is never served a guest. A genuine anonymous caller (no credential)
	// keeps its guest session and flows to the anonymous branch below.
	if sessionNeedsSelfHeal(c.GetSessionUser()) {
		if p := c.credentialUser(); p != nil && !util.IsAnonymousUser(p) {
			c.SetSessionClaims(&iam.Claims{User: *p})
		}
	}

	// Anonymous is permitted ONLY on public/preview surfaces. On a protected
	// surface an unresolved identity fails LOUD (401) instead of fabricating a
	// guest — matching the old RequireSignedIn behavior, now with self-heal ahead.
	switch resolveAccountAction(c.GetSessionUser(), c.isPublicDomain() || !disablePreviewMode) {
	case serveAnonymous:
		c.anonymousSignin()
		return
	case failUnauthenticated:
		c.ResponseUnauthorized(c.T("auth:Please sign in first"))
		return
	}

	claims := c.GetSessionClaims()

	// Fetch fresh user data from IAM in real-time for non-anonymous users.
	// Resolve by the SESSION's own owner (white-label: a lux session refreshes
	// lux/z, not hanzo/z) — see refreshSessionUser. hanzo is byte-unchanged.
	if claims.User.Type != "anonymous-user" {
		user, err := refreshSessionUser(c.Ctx.Request.Host, &claims.User)
		if err != nil {
			c.ResponseError(err.Error())
			return
		}

		if user != nil {
			// Update the session with fresh user data from IAM
			// Only update the User field, preserving all other claims fields (AccessToken, Type, IsAdmin, etc.)
			claims.User = *user
			c.SetSessionClaims(claims)
		}
	}

	isSafePassword, err := c.isSafePassword()
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Strip every credential field before returning the account to the browser:
	// password hash/salt/type, MFA seed + recovery codes, and all access/refresh
	// tokens. Access keys are revealed only via an explicit authorized endpoint,
	// never this default read. The claims here are a per-call copy, so the
	// server-side session set above is unaffected.
	object.RedactClaimsSecrets(claims)

	// Preserve the "must change password" signal as a non-secret sentinel.
	if !isSafePassword {
		claims.User.Password = "#NeedToModify#"
	}

	// Surface the cloud's AUTHORITATIVE super-admin verdict to the browser.
	// iam.User has no isSuperAdmin field, so wrap the claims (their fields stay
	// promoted to the top level — owner, isAdmin, email, … are unchanged) and add
	// the computed bool alongside. The console gate (isSuperAdminAccount) reads
	// account.isSuperAdmin off this SAME object. Stamped AFTER redaction (it's a
	// non-secret bool, and redaction never touches it).
	c.ResponseOk(accountResponse{
		Claims:       claims,
		IsSuperAdmin: util.IsSuperAdmin(&claims.User),
	})
}

// accountResponse is the /get-account payload: the full claims (every field
// promoted to the top level of the JSON object, so existing consumers still read
// owner/name/isAdmin/email/… off the same object unchanged) plus the cloud's
// authoritative isSuperAdmin verdict (util.IsSuperAdmin: membership in the
// reserved `admin` org — the ONE super-admin rule, matching IAM and the console).
type accountResponse struct {
	*iam.Claims
	IsSuperAdmin bool `json:"isSuperAdmin"`
}

// preferencesKey is the single IAM-user property under which all cross-product,
// cross-device user customizations are stored as a JSON object. One home for
// every product's settings — each product owns its own top-level key(s).
const preferencesKey = "hanzo.preferences"

// UpdatePreferences persists user customizations (favorites, layout, etc.) onto
// the signed-in user's IAM account so they follow the user across every product
// and every device/login.
//
// SELF-SCOPED BY DESIGN: the target user is taken from the session, never from
// the request body — a caller can only ever change their own preferences. The
// posted JSON object is SHALLOW-MERGED into the existing preferences by
// top-level key, so one product (or device) writing its keys never clobbers
// another's. Persisted column-scoped (`properties` only) so no other user field
// is touched. Returns the merged preferences object.
//
// @Title UpdatePreferences
// @Tag Account API
// @Description persist the signed-in user's cross-product preferences
// @Success 200 {object} object the merged preferences
// @router /update-preferences [post]
func (c *ApiController) UpdatePreferences() {
	_, ok := c.RequireSignedIn()
	if !ok {
		return
	}

	claims := c.GetSessionClaims()
	if claims == nil || claims.User.Type == "anonymous-user" {
		c.ResponseError("auth:please sign in first")
		return
	}

	incoming := map[string]interface{}{}
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &incoming); err != nil {
		c.ResponseError(fmt.Sprintf("invalid preferences body: %v", err))
		return
	}

	// Always resolve the user from IAM by the SESSION identity, never the body —
	// scoped to the session's own owner (white-label: lux/z, not hanzo/z).
	user, err := refreshSessionUser(c.Ctx.Request.Host, &claims.User)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if user == nil {
		c.ResponseError("auth:user not found")
		return
	}

	// Shallow-merge incoming top-level keys into the existing preferences object.
	prefs := map[string]interface{}{}
	if user.Properties != nil {
		if raw := user.Properties[preferencesKey]; raw != "" {
			_ = json.Unmarshal([]byte(raw), &prefs)
		}
	}
	for k, v := range incoming {
		prefs[k] = v
	}

	merged, err := json.Marshal(prefs)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if user.Properties == nil {
		user.Properties = map[string]string{}
	}
	user.Properties[preferencesKey] = string(merged)

	// Column-scoped update: only `properties` is written.
	if _, err := iam.UpdateUserForColumns(user, []string{"properties"}); err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Refresh the session copy so a subsequent get-account is consistent.
	claims.User = *user
	c.SetSessionClaims(claims)

	c.ResponseOk(prefs)
}
