// Copyright 2023-2025 Hanzo AI Inc.. All Rights Reserved.
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
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

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

func GetSessionUser(ctx *context.Context) *iam.User {
	s := ctx.Input.Session("user")
	if s == nil {
		return nil
	}

	claims := s.(iam.Claims)
	return &claims.User
}

func getUsername(ctx *context.Context) (username string) {
	user := GetSessionUser(ctx)
	if user != nil {
		username = util.GetIdFromOwnerAndName(user.Owner, user.Name)
	} else {
		username, _ = getUsernameByClientIdSecret(ctx)
	}
	return
}

func responseError(ctx *context.Context, error string, data ...interface{}) {
	// ctx.ResponseWriter.WriteHeader(http.StatusForbidden)

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

// responseErrorStatus writes the standard error envelope with an explicit HTTP
// status. Filters use it so an auth/authz denial is a real 401/403, not Beego's
// default 200 — a denial must never look like success to a client. The body
// shape is unchanged; only the status differs.
func responseErrorStatus(ctx *context.Context, status int, error string, data ...interface{}) {
	ctx.Output.SetStatus(status)
	responseError(ctx, error, data...)
}

// denyUnauthorized renders a 401 (no/invalid credential).
func denyUnauthorized(ctx *context.Context, error string, data ...interface{}) {
	responseErrorStatus(ctx, http.StatusUnauthorized, error, data...)
}

// denyForbidden renders a 403 (authenticated but not permitted).
func denyForbidden(ctx *context.Context, error string, data ...interface{}) {
	responseErrorStatus(ctx, http.StatusForbidden, error, data...)
}

func setSessionUser(ctx *context.Context, userId string) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(userId)
	if err != nil {
		panic(err)
	}
	claims := iam.Claims{
		User: iam.User{
			Owner:   owner,
			Name:    name,
			IsAdmin: true,
		},
	}
	err = ctx.Input.CruSession.Set("user", claims)
	if err != nil {
		panic(err)
	}

	// https://github.com/beego/beego/issues/3445#issuecomment-455411915
	ctx.Input.CruSession.SessionRelease(ctx.ResponseWriter)
}

func getUsernameByClientIdSecret(ctx *context.Context) (string, error) {
	clientId, clientSecret, ok := ctx.Request.BasicAuth()
	if !ok {
		clientId = ctx.Input.Query("clientId")
		clientSecret = ctx.Input.Query("clientSecret")
	}

	if clientId == "" || clientSecret == "" {
		return "", nil
	}

	applicationName := conf.GetConfigString("IAM_APP_NAME")
	if clientSecret != conf.GetConfigString("IAM_CLIENT_SECRET") {
		return "", fmt.Errorf("Incorrect client secret for application: %s", applicationName)
	}

	return util.GetIdFromOwnerAndName("app", applicationName), nil
}

func getUsernameByAccessToken(accessTokenInput string) (string, error) {
	applicationName := conf.GetConfigString("IAM_APP_NAME")
	clientSecret := conf.GetConfigString("IAM_CLIENT_SECRET")
	clientId := conf.GetConfigString("IAM_CLIENT_ID")
	accessToken := getMd5HexDigest(clientId + ":" + clientSecret)
	if accessTokenInput != accessToken {
		return "", fmt.Errorf("Incorrect access token for application: %s", applicationName)
	}

	return util.GetIdFromOwnerAndName("app", applicationName), nil
}

func parseBearerToken(ctx *context.Context) string {
	header := ctx.Request.Header.Get("Authorization")
	tokens := strings.Split(header, " ")
	if len(tokens) != 2 {
		return ""
	}

	prefix := tokens[0]
	if prefix != "Bearer" {
		return ""
	}

	return tokens[1]
}

func getMd5HexDigest(s string) string {
	hash := md5.Sum([]byte(s))
	res := hex.EncodeToString(hash[:])
	return res
}
