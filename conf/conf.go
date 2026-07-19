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

package conf

import (
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
)

type WebConfig struct {
	AuthConfig struct {
		ServerUrl        string `json:"serverUrl"`
		ClientId         string `json:"clientId"`
		AppName          string `json:"appName"`
		OrganizationName string `json:"organizationName"`
		RedirectPath     string `json:"redirectPath"`
	} `json:"authConfig"`
	EnableExtraPages   bool     `json:"enableExtraPages"`
	ShortcutPageItems  []string `json:"shortcutPageItems"`
	UsageEndpoints     []string `json:"usageEndpoints"`
	IframeUrl          string   `json:"iframeUrl"`
	ForceLanguage      string   `json:"forceLanguage"`
	DefaultLanguage    string   `json:"defaultLanguage"`
	StaticBaseUrl      string   `json:"staticBaseUrl"`
	HtmlTitle          string   `json:"htmlTitle"`
	FaviconUrl         string   `json:"faviconUrl"`
	LogoUrl            string   `json:"logoUrl"`
	NavbarHtml         string   `json:"navbarHtml"`
	FooterHtml         string   `json:"footerHtml"`
	AppUrl             string   `json:"appUrl"`
	ShowGithubCorner   bool     `json:"showGithubCorner"`
	IsDemoMode         bool     `json:"isDemoMode"`
	DisablePreviewMode bool     `json:"disablePreviewMode"`
	ThemeDefault       struct {
		ThemeType    string `json:"themeType"`
		ColorPrimary string `json:"colorPrimary"`
		BorderRadius int    `json:"borderRadius"`
		IsCompact    bool   `json:"isCompact"`
	} `json:"themeDefault"`
	AvatarErrorUrl string `json:"avatarErrorUrl"`
}

func ReadGlobalConfigTokens() []string {
	dbName := AppConfig.String("dbName")
	if strings.Count(dbName, "_") < 2 {
		return nil
	}

	path := "C:/cloud_data/config.txt"
	if !FileExist(path) {
		return nil
	}

	text := ReadStringFromPath(path)
	tokens := strings.Split(text, "\n")
	return tokens
}

func GetConfigString(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}

	tokens := ReadGlobalConfigTokens()
	if len(tokens) > 0 {
		if key == "forceLanguage" {
			return tokens[6]
		} else if key == "defaultLanguage" {
			return tokens[7]
		} else if key == "htmlTitle" {
			return tokens[8]
		} else if key == "faviconUrl" {
			return tokens[9]
		} else if key == "logoUrl" {
			return tokens[10]
		} else if key == "footerHtml" {
			return tokens[11]
		}
	}

	res := AppConfig.String(key)
	if res == "" {
		if key == "staticBaseUrl" {
			res = "https://cdn.hanzo.ai"
		} else if key == "logConfig" {
			res = "{\"filename\": \"logs/cloud.log\", \"maxdays\":99999, \"perm\":\"0770\"}"
		} else if key == "avatarErrorUrl" {
			res = "https://cdn.hanzo.ai/gravatar/error.png"
		}
	}

	if key == "staticBaseUrl" {
		if strings.HasSuffix(GetConfigString("IAM_URL"), ".hanzo.ai") && res == "https://cdn.hanzo.ai" {
			res = "https://cdn.hanzo.ai"
		}
	}

	if key == "frontendBaseDir" && os.Getenv("RUNNING_IN_DOCKER") == "true" {
		res = ""
	}

	return res
}

func GetConfigBool(key string) bool {
	value := GetConfigString(key)
	if value == "true" {
		return true
	} else {
		return false
	}
}

func GetConfigInt(key string) int {
	value := GetConfigString(key)
	num, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return num
}

func GetConfigDataSourceName() string {
	dataSourceName := GetConfigString("dataSourceName")

	runningInDocker := os.Getenv("RUNNING_IN_DOCKER")
	if runningInDocker == "true" {
		// https://stackoverflow.com/questions/48546124/what-is-linux-equivalent-of-host-docker-internal
		if runtime.GOOS == "linux" {
			dataSourceName = strings.ReplaceAll(dataSourceName, "localhost", "172.17.0.1")
		} else {
			dataSourceName = strings.ReplaceAll(dataSourceName, "localhost", "host.docker.internal")
		}
	}

	return dataSourceName
}

func GetLanguage(language string) string {
	if language == "" || language == "*" {
		return "en"
	}

	if len(language) != 2 || language == "nu" {
		return "en"
	} else {
		return language
	}
}

func IsDemoMode() bool {
	return strings.ToLower(GetConfigString("isDemoMode")) == "true"
}

// DisablePreviewMode resolves the disablePreviewMode toggle. It is the ONE place
// the preview-mode value is read, so it can never be resolved two ways. Preview
// mode is the UNAUTHENTICATED, development-only relaxation; disabling it is what
// production wants.
//
// Resolution order (env beats file so ops can flip it on the DEPLOYED cloud CR
// with NO rebuild — the reversible, durable lever):
//
//  1. DISABLE_PREVIEW_MODE env — "true" (case-insensitive) => preview OFF.
//  2. disablePreviewMode app.conf key (standalone/local dev).
//
// A security kill-switch gets its own explicit UPPER_SNAKE env name rather than
// riding the generic GetConfigString passthrough (whose env key would be the
// camelCase config name) — one unambiguous lever for one decision.
func DisablePreviewMode() bool {
	if v, ok := os.LookupEnv("DISABLE_PREVIEW_MODE"); ok {
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	b, _ := AppConfig.Bool("disablePreviewMode")
	return b
}

// IsPreviewMode reports whether the server is in preview mode: the
// unauthenticated dev relaxation. It is the semantic every caller wants; it and
// DisablePreviewMode are the ONLY readers of the toggle. Callers must use these
// and never read the raw config key, so the env lever governs every call site.
func IsPreviewMode() bool {
	return !DisablePreviewMode()
}

func GetStringArray(key string) []string {
	strValue := GetConfigString(key)
	if strValue == "" {
		return []string{}
	}

	var strArray []string
	if err := json.Unmarshal([]byte(strValue), &strArray); err == nil {
		return strArray
	}

	return strings.Split(strValue, ",")
}

func GetWebConfig() *WebConfig {
	config := &WebConfig{}

	config.AuthConfig.ServerUrl = GetConfigString("IAM_URL")
	config.AuthConfig.ClientId = GetConfigString("IAM_CLIENT_ID")
	config.AuthConfig.AppName = GetConfigString("IAM_APP_NAME")
	config.AuthConfig.OrganizationName = GetConfigString("IAM_ORG")
	config.AuthConfig.RedirectPath = GetConfigString("IAM_REDIRECT_PATH")

	config.EnableExtraPages = GetConfigBool("enableExtraPages")
	config.ShortcutPageItems = GetStringArray("shortcutPageItems")
	config.UsageEndpoints = GetStringArray("usageEndpoints")
	config.IframeUrl = GetConfigString("iframeUrl")
	config.ForceLanguage = GetConfigString("forceLanguage")
	config.DefaultLanguage = GetLanguage(GetConfigString("defaultLanguage"))
	config.StaticBaseUrl = GetConfigString("staticBaseUrl")
	config.HtmlTitle = GetConfigString("htmlTitle")
	config.FaviconUrl = GetConfigString("faviconUrl")
	config.LogoUrl = GetConfigString("logoUrl")
	config.NavbarHtml = GetConfigString("navbarHtml")
	config.FooterHtml = GetConfigString("footerHtml")
	config.AppUrl = GetConfigString("appUrl")
	config.ShowGithubCorner = GetConfigBool("showGithubCorner")
	config.IsDemoMode = GetConfigBool("isDemoMode")
	config.DisablePreviewMode = GetConfigBool("disablePreviewMode")

	config.ThemeDefault.ThemeType = GetConfigString("defaultThemeType")
	config.ThemeDefault.ColorPrimary = GetConfigString("defaultColorPrimary")
	config.ThemeDefault.BorderRadius = GetConfigInt("defaultBorderRadius")
	config.ThemeDefault.IsCompact = GetConfigBool("defaultIsCompact")

	config.AvatarErrorUrl = GetConfigString("avatarErrorUrl")

	return config
}
