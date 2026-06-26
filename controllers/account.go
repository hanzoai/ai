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
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/beego/beego"
	"github.com/beego/beego/logs"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	iam "github.com/hanzoai/iam"
)

func init() {
	InitAuthConfig()
}

func InitAuthConfig() {
	iamEndpoint := conf.GetConfigString("IAM_URL")
	clientId := conf.GetConfigString("IAM_CLIENT_ID")
	clientSecret := conf.GetConfigString("IAM_CLIENT_SECRET")
	iamOrganization := conf.GetConfigString("IAM_ORG")
	iamApplication := conf.GetConfigString("IAM_APP_NAME")

	if iamEndpoint == "" {
		return
	}

	iam.InitConfig(iamEndpoint, clientId, clientSecret, "", iamOrganization, iamApplication)
	application, err := iam.GetApplication(iamApplication)
	if err != nil {
		fmt.Printf("[WARN] Failed to get IAM application %q: %v (auth features disabled)\n", iamApplication, err)
		return
	}
	if application == nil {
		fmt.Printf("[WARN] IAM application %q does not exist (auth features disabled)\n", iamApplication)
		return
	}

	cert, err := iam.GetCert(application.Cert)
	if err != nil {
		fmt.Printf("[WARN] Failed to get cert %q for application %q: %v (auth features disabled)\n", application.Cert, iamApplication, err)
		return
	}
	if cert == nil {
		fmt.Printf("[WARN] Cert %q for application %q does not exist (auth features disabled)\n", application.Cert, iamApplication)
		return
	}

	iam.InitConfig(iamEndpoint, clientId, clientSecret, cert.Certificate, iamOrganization, iamApplication)
}

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

	token, err := iam.GetOAuthToken(code, state)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	claims, err := iam.ParseJwtToken(token.AccessToken)
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

	effectiveOrg := c.GetEffectiveOrg()
	user := iam.User{
		Owner:           effectiveOrg,
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
	disablePreviewMode, _ := beego.AppConfig.Bool("disablePreviewMode")
	err := util.AppendWebConfigCookie(c.Ctx)
	if err != nil {
		logs.Error("AppendWebConfigCookie: %v", err)
	}

	if !c.isPublicDomain() && disablePreviewMode {
		_, ok := c.RequireSignedIn()
		if !ok {
			return
		}
	} else {
		_, ok := c.CheckSignedIn()
		if !ok {
			c.anonymousSignin()
			return
		}
	}

	claims := c.GetSessionClaims()

	// Fetch fresh user data from IAM in real-time for non-anonymous users
	if claims.User.Type != "anonymous-user" {
		user, err := iam.GetUser(claims.User.Name)
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

	if !isSafePassword {
		claims.User.Password = "#NeedToModify#"
	}

	c.ResponseOk(claims)
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

	// Always resolve the user from IAM by the SESSION identity, never the body.
	user, err := iam.GetUser(claims.User.Name)
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
