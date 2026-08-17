// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// Native ZAP handlers for the account/auth route-group (strangler migration of
// controllers/account.go). Pure ZAP handlers — no controller, no session,
// no cookie, no http.ResponseWriter. Identity is the bearer `auth` seam
// (zapAccountPrincipal), exactly like zapChatHandler. The same routes stay live
// on routers.App, which also backs the gateway fallback.
//
// The four handlers, and the HTTP endpoint each answers for (behavior preserved
// 1:1, minus the browser-only session/cookie plumbing that has no meaning on the
// stateless ZAP wire). The paths are the ones routers/resources.go generates —
// the flat /v1/signin, /v1/get-account and /v1/update-preferences this list used
// to name have not existed since the resources became nouns:
//   POST  /v1/ai/signin      -> zapSigninHandler            (method "account.signin")
//   POST  /v1/ai/signout     -> zapSignoutHandler           (method "account.signout")
//   GET   /v1/ai/account     -> zapGetAccountHandler        (method "account.get")
//   PATCH /v1/ai/preferences -> zapUpdatePreferencesHandler (method "account.preferences")
//
// Only the METHOD names are registered. No gateway prefix is: these four sit in
// the /v1/ai namespace the resource table generates, where a path-only matcher
// cannot be trusted to pick the right handler.
//
// REGISTRATION: this file self-registers from its own init() via the shared
// registerCloud / registerGatewayPath seam (the ONE registry, zap_registry.go).
// This group defines NONE of the registry primitives — it only registers into
// them — so it never contends with another group's file. Handlers use the canonical native handler signature
// func(ctx, auth string, body []byte) (*zap.Message, error).
//
// The account endpoints return the {status,msg,data,data2} envelope (unlike
// the OpenAI-compat chat surface, which returns a raw payload), so these handlers
// marshal the SAME Response envelope their HTTP counterparts do — a client reading
// /v1/get-account gets a byte-identical body shape.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"

	"github.com/luxfi/zap"
)

// zapBrandHost is the brand-resolution host on the ZAP wire. The native handler
// signature carries no request Host, so brand resolution falls through to the
// default (hanzo): resolveBrandIAM("") returns exactly the values InitAuthConfig
// baked. A white-label single-tenant deployment pins its identity via env
// (IAM_CLIENT_ID/IAM_ORG), which resolveBrandIAM honors for the default brand, so
// those deployments still resolve correctly.
const zapBrandHost = ""

func init() { registerZapAccountAuth() }

// registerZapAccountAuth wires this group's routes into the shared dispatch
// registry. Invoked from init() so the group self-registers from its own file;
// InitZapHandlers stays the single Node.Handle bootstrap and gains no per-group
// arm.
func registerZapAccountAuth() {
	registerCloud("account.signin", zapSigninHandler)
	registerCloud("account.signout", zapSignoutHandler)
	registerCloud("account.get", zapGetAccountHandler)
	registerCloud("account.preferences", zapUpdatePreferencesHandler)

}

// ── envelope helpers ────────────────────────────────────────────────────────
//
// The account surface speaks the {status,msg,data,data2} envelope, so
// these mirror ResponseOk / ResponseError into the ZAP body. Status codes match
// the HTTP handlers: ResponseUnauthorized -> 401; ResponseError -> 200 (the router's
// default, with the error carried in the body's status:"error").

func zapAccountOk(httpStatus uint32, data interface{}) (*zap.Message, error) {
	body, _ := json.Marshal(Response{Status: "ok", Data: data})
	return object.BuildCloudResponse(httpStatus, body, "")
}

func zapAccountErr(httpStatus uint32, msg string) (*zap.Message, error) {
	body, _ := json.Marshal(Response{Status: "error", Msg: msg})
	return object.BuildCloudResponse(httpStatus, body, msg)
}

// ── identity seam ───────────────────────────────────────────────────────────

// zapAccountPrincipal resolves the request principal STRICTLY from the bearer
// `auth` string — the standalone (no-controller, no-session) twin of
// principalUser. JWTs go through object.ParseAndValidateJWT (signature +
// iss/aud, never raw iam.ParseJwtToken); pk-/sk- IAM keys through
// getUserByAccessKey. This is the ONE identity source for the authed account
// routes; the body is never trusted for identity.
func zapAccountPrincipal(auth string) (*iam.User, error) {
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return nil, fmt.Errorf("authentication required")
	}
	if isJwtToken(token) {
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil {
			return nil, err
		}
		return &claims.User, nil
	}
	if isIAMApiKey(token) {
		user, err := getUserByAccessKey(token)
		if err != nil {
			return nil, fmt.Errorf("invalid API key: %w", err)
		}
		if user != nil {
			return user, nil
		}
	}
	return nil, fmt.Errorf("unsupported auth type")
}

// ── POST /v1/signin ─────────────────────────────────────────────────────────
//
// The OAuth code→token exchange that MINTS identity, so it needs no incoming
// bearer (auth is ignored). Body carries the {code,state} the browser path took
// from query params, plus an optional {host} so a white-label console still
// exchanges its code against the right brand IAM (resolveBrandIAM); absent host
// => the hanzo default, byte-unchanged. On the stateless ZAP wire there is no
// the router session to bind or cookie to set — the caller keeps the returned
// accessToken. The initial chat/message seeding is preserved verbatim.
func zapSigninHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	var req struct {
		Code  string `json:"code"`
		State string `json:"state"`
		Host  string `json:"host"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return zapAccountErr(400, "invalid request: "+err.Error())
	}
	if req.Code == "" {
		return zapAccountErr(400, "code required")
	}

	host := req.Host
	if host == "" {
		host = zapBrandHost
	}
	brand := resolveBrandIAM(host)
	authClient, err := brandAuthClient(brand)
	if err != nil {
		return zapAccountErr(500, err.Error())
	}

	token, err := authClient.GetOAuthToken(req.Code, req.State)
	if err != nil {
		return zapAccountErr(401, err.Error())
	}

	claims, err := authClient.ParseJwtToken(token.AccessToken)
	if err != nil {
		return zapAccountErr(401, err.Error())
	}

	// Same downgrade rule as the router path: a bare/loosely-typed principal that
	// is not an admin is scoped to chat-user.
	if strings.Count(claims.Type, "-") <= 1 {
		if !util.IsAdmin(&claims.User) {
			claims.Type = "chat-user"
		}
	}

	if err := zapAddInitialChatAndMessage(&claims.User); err != nil {
		return zapAccountErr(500, err.Error())
	}

	claims.AccessToken = token.AccessToken
	return zapAccountOk(200, claims)
}

// ── POST /v1/signout ────────────────────────────────────────────────────────
//
// Idempotent, matching the router path (which no-ops when there is no session
// user). The ZAP wire is sessionless — there is no server-side the router session id
// to delete — so sign-out is purely the client discarding its bearer. An
// absent/invalid token still returns OK (never a 500), exactly like the hardened
// controller handler.
func zapSignoutHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	return zapAccountOk(200, nil)
}

// ── GET /v1/get-account ─────────────────────────────────────────────────────
//
// Resolves the principal from the verified credential (the ZAP equivalent of
// get-account's self-heal, which re-derives identity from the credential rather
// than a possibly-stale session). No credential => fail LOUD 401, never a
// synthesized anonymous guest: the anonymous fallback is a browser/public-domain
// affordance keyed on client IP + user-agent, neither of which exists on this
// wire. A resolved subject is refreshed from IAM, has its secrets redacted, and
// is returned as the SAME accountResponse (claims + authoritative isSuperAdmin)
// the HTTP path serves.
func zapGetAccountHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	user, err := zapAccountPrincipal(auth)
	if err != nil {
		return zapAccountErr(401, "Please sign in first")
	}

	// Fetch fresh user data from IAM for non-anonymous users, scoped to the
	// principal's own owner (host defaults to hanzo on this wire). Fail-soft: a
	// nil refresh keeps the credential identity rather than erroring.
	if !util.IsAnonymousUser(user) {
		if fresh, ferr := refreshSessionUser(zapBrandHost, user); ferr != nil {
			return zapAccountErr(500, ferr.Error())
		} else if fresh != nil {
			user = fresh
		}
	}

	claims := &iam.Claims{User: *user}

	// Preserve the "must change password" sentinel (see isSafePassword).
	needsPasswordChange := zapNeedsPasswordChange(user)

	// Strip every credential field before returning the account.
	object.RedactClaimsSecrets(claims)
	if needsPasswordChange {
		claims.User.Password = "#NeedToModify#"
	}

	return zapAccountOk(200, accountResponse{
		Claims:       claims,
		IsSuperAdmin: util.IsSuperAdmin(&claims.User),
	})
}

// zapNeedsPasswordChange mirrors ApiController.isSafePassword: a legacy account
// (11-char Id starting "270") whose password is still the placeholder must be
// flagged. Any other account is safe.
func zapNeedsPasswordChange(user *iam.User) bool {
	if len(user.Id) != 11 || !strings.HasPrefix(user.Id, "270") {
		return false
	}
	return user.Password == "#NeedToModify#"
}

// ── POST /v1/update-preferences ─────────────────────────────────────────────
//
// Self-scoped by design: the target user is the resolved PRINCIPAL, never the
// body. Anonymous principals are rejected. The posted JSON object is
// shallow-merged into the existing hanzo.preferences by top-level key and
// persisted column-scoped (`properties` only). Returns the merged object.
func zapUpdatePreferencesHandler(ctx context.Context, auth string, body []byte) (*zap.Message, error) {
	user, err := zapAccountPrincipal(auth)
	if err != nil {
		return zapAccountErr(401, "Please sign in first")
	}
	if util.IsAnonymousUser(user) {
		return zapAccountErr(401, "auth:please sign in first")
	}

	incoming := map[string]interface{}{}
	if err := json.Unmarshal(body, &incoming); err != nil {
		return zapAccountErr(400, fmt.Sprintf("invalid preferences body: %v", err))
	}

	// Resolve the user from IAM by the principal identity (never the body).
	fresh, err := refreshSessionUser(zapBrandHost, user)
	if err != nil {
		return zapAccountErr(500, err.Error())
	}
	if fresh != nil {
		user = fresh
	}
	if user == nil {
		return zapAccountErr(401, "auth:user not found")
	}

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
		return zapAccountErr(500, err.Error())
	}
	if user.Properties == nil {
		user.Properties = map[string]string{}
	}
	user.Properties[preferencesKey] = string(merged)

	if _, err := iam.UpdateUserForColumns(user, []string{"properties"}); err != nil {
		return zapAccountErr(500, err.Error())
	}

	return zapAccountOk(200, prefs)
}

// ── initial-chat seeding (standalone twins of the controller methods) ───────
//
// The the router addInitialChat* methods reach into c.getClientIp/getUserAgent for
// the seeded chat's telemetry; on the ZAP wire those request-scoped values do
// not exist, so the seeded chat carries empty client metadata. Everything else
// is identical to account.go so a native sign-in leaves the same first
// chat+welcome message as the browser sign-in.

func zapAddInitialChat(organization string, userName string, storeName string) (*object.Chat, error) {
	var store *object.Store
	var err error

	if storeName != "" {
		store, err = object.GetStore(util.GetId("admin", storeName))
		if err != nil {
			return nil, err
		}
		if store == nil {
			return nil, fmt.Errorf("the store: %s is not found", storeName)
		}
	} else {
		store, err = object.GetDefaultStore("admin")
		if err != nil {
			return nil, err
		}
		if store == nil {
			return nil, fmt.Errorf("the default store is not found")
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
		ClientIp:      "",
		UserAgent:     "",
		MessageCount:  0,
		NeedTitle:     true,
	}

	if store.Welcome != "Hello" {
		chat.DisplayName = fmt.Sprintf("新会话 - %d", 1)
		chat.Category = "默认分类"
	}

	chat.ClientIpDesc = util.GetDescFromIP(chat.ClientIp)
	chat.UserAgentDesc = util.GetDescFromUserAgent(chat.UserAgent)

	if _, err = object.AddChat(chat); err != nil {
		return nil, err
	}

	return chat, nil
}

func zapAddInitialChatAndMessage(user *iam.User) error {
	chats, err := object.GetChats("admin", "", user.Name)
	if err != nil {
		return err
	}
	if len(chats) != 0 {
		return nil
	}

	chat, err := zapAddInitialChat(user.Owner, user.Name, "")
	if err != nil {
		return err
	}

	store, err := object.GetStore(util.GetId("admin", chat.Store))
	if err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("the store: %s is not found", chat.Store)
	}

	userMessage := &object.Message{
		Owner:        "admin",
		Name:         fmt.Sprintf("message_%s", util.GetRandomName()),
		CreatedTime:  util.AdjustTimeFromSecToMilli(chat.CreatedTime, -100),
		Organization: chat.Organization,
		Store:        chat.Store,
		User:         user.Name,
		Chat:         chat.Name,
		ReplyTo:      "",
		Author:       user.Name,
		Text:         store.Welcome,
		IsHidden:     true,
		VectorScores: []object.VectorScore{},
	}
	if _, err = object.AddMessage(userMessage); err != nil {
		return err
	}

	answerMessage := &object.Message{
		Owner:        "admin",
		Name:         fmt.Sprintf("message_%s", util.GetRandomName()),
		CreatedTime:  util.GetCurrentTimeEx(chat.CreatedTime),
		Organization: chat.Organization,
		Store:        chat.Store,
		User:         user.Name,
		Chat:         chat.Name,
		ReplyTo:      "Welcome",
		Author:       "AI",
		Text:         "",
		VectorScores: []object.VectorScore{},
	}
	_, err = object.AddMessage(answerMessage)
	return err
}
