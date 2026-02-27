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

package object

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/beego/beego/logs"
)

// kmsClient fetches secrets from Hanzo KMS.
//
// Authentication modes (checked in order):
//  1. Service Token: set KMS_SERVICE_TOKEN (format: "st.{id}.{secret}")
//     OR set HANZO_API_KEY as the unified service token.
//  2. Universal Auth: set KMS_CLIENT_ID + KMS_CLIENT_SECRET (machine identity)
//
// Environment variables:
//   - KMS_ENDPOINT:      Base URL (default: http://kms.hanzo.svc)
//     External: https://kms-api.hanzo.ai (no /api prefix)
//   - KMS_SERVICE_TOKEN:  Service token for direct auth
//   - HANZO_API_KEY:      Unified service token (fallback for KMS_SERVICE_TOKEN)
//   - KMS_CLIENT_ID:      Universal Auth client ID
//   - KMS_CLIENT_SECRET:  Universal Auth client secret
//   - KMS_PROJECT_ID:     Default project ID for system (admin-owned) secrets
//   - KMS_ENVIRONMENT:    Environment slug (default: prod)
//
// Multi-tenant model:
//   - Admin-owned providers use KMS_PROJECT_ID (system secrets)
//   - Org-owned providers store "kms-project:{projectId}" in ConfigText
//   - Convention: store "kms://SECRET_NAME" in provider.ClientSecret
type kmsClient struct {
	endpoint    string
	environment string
	projectID   string // default project for admin-owned secrets
	httpClient  *http.Client

	// Auth: exactly one of these is set
	serviceToken string // st.{id}.{secret} — used directly in Authorization header
	clientID     string // Universal Auth client ID
	clientSecret string // Universal Auth client secret

	// Universal Auth token cache
	accessToken    string
	tokenExpiresAt time.Time
	tokenMu        sync.Mutex
}

var (
	kms     *kmsClient
	kmsOnce sync.Once

	// Secret value cache: key = "projectID/secretName"
	kmsSecrets = make(map[string]*kmsSecretEntry)
	kmsSecMu   sync.RWMutex
	kmsSecTTL  = 5 * time.Minute
)

type kmsSecretEntry struct {
	value     string
	fetchedAt time.Time
}

// initKMS initializes the KMS client from environment variables.
func initKMS() {
	kmsOnce.Do(func() {
		serviceToken := os.Getenv("KMS_SERVICE_TOKEN")
		if serviceToken == "" {
			serviceToken = os.Getenv("HANZO_API_KEY")
		}
		clientID := os.Getenv("KMS_CLIENT_ID")
		clientSecret := os.Getenv("KMS_CLIENT_SECRET")

		if serviceToken == "" && clientID == "" {
			logs.Info("KMS not configured (no KMS_SERVICE_TOKEN or KMS_CLIENT_ID) — using DB secrets")
			return
		}

		endpoint := os.Getenv("KMS_ENDPOINT")
		if endpoint == "" {
			endpoint = "http://kms.hanzo.svc"
		}
		endpoint = strings.TrimRight(endpoint, "/")

		projectID := os.Getenv("KMS_PROJECT_ID")
		environment := os.Getenv("KMS_ENVIRONMENT")
		if environment == "" {
			environment = "prod"
		}

		kms = &kmsClient{
			endpoint:     endpoint,
			environment:  environment,
			projectID:    projectID,
			serviceToken: serviceToken,
			clientID:     clientID,
			clientSecret: clientSecret,
			httpClient: &http.Client{
				Timeout: 10 * time.Second,
			},
		}

		authMode := "service-token"
		if serviceToken == "" {
			authMode = "universal-auth"
		}
		logs.Info("KMS client initialized: endpoint=%s project=%s env=%s auth=%s",
			endpoint, projectID, environment, authMode)
	})
}

// ── Universal Auth token management ─────────────────────────────────────────

type universalAuthResponse struct {
	AccessToken       string `json:"accessToken"`
	ExpiresIn         int    `json:"expiresIn"`
	AccessTokenMaxTTL int    `json:"accessTokenMaxTTL"`
	TokenType         string `json:"tokenType"`
}

// getAuthToken returns the token to use in the Authorization header.
// For service tokens, returns the token directly.
// For Universal Auth, manages the token lifecycle (login + refresh).
func (c *kmsClient) getAuthToken() (string, error) {
	if c.serviceToken != "" {
		return c.serviceToken, nil
	}

	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	// Return cached token if still valid (with 30s buffer)
	if c.accessToken != "" && time.Now().Add(30*time.Second).Before(c.tokenExpiresAt) {
		return c.accessToken, nil
	}

	// Login via Universal Auth
	body, err := json.Marshal(map[string]string{
		"clientId":     c.clientID,
		"clientSecret": c.clientSecret,
	})
	if err != nil {
		return "", fmt.Errorf("kms: failed to marshal login request: %w", err)
	}

	url := c.endpoint + "/api/v1/auth/universal-auth/login"
	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("kms: universal auth login failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("kms: failed to read login response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("kms: universal auth login returned %d: %s", resp.StatusCode, string(respBody))
	}

	var authResp universalAuthResponse
	if err := json.Unmarshal(respBody, &authResp); err != nil {
		return "", fmt.Errorf("kms: failed to parse login response: %w", err)
	}

	c.accessToken = authResp.AccessToken
	c.tokenExpiresAt = time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

	logs.Info("KMS: universal auth token acquired, expires in %ds", authResp.ExpiresIn)
	return c.accessToken, nil
}

// ── Secret fetching ─────────────────────────────────────────────────────────

// kmsSecretResponse is the JSON envelope from KMS V4 GET /api/v4/secrets/:name
type kmsSecretResponse struct {
	Secret struct {
		SecretKey   string `json:"secretKey"`
		SecretValue string `json:"secretValue"`
	} `json:"secret"`
}

// getSecret fetches a secret value by name from KMS, scoped to a project.
// Results are cached for kmsSecTTL (5 minutes) keyed by project+name.
func (c *kmsClient) getSecret(name string, projectID string) (string, error) {
	cacheKey := projectID + "/" + name

	kmsSecMu.RLock()
	entry, ok := kmsSecrets[cacheKey]
	kmsSecMu.RUnlock()

	if ok && time.Since(entry.fetchedAt) < kmsSecTTL {
		return entry.value, nil
	}

	token, err := c.getAuthToken()
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/api/v4/secrets/%s?projectId=%s&environment=%s",
		c.endpoint, name, projectID, c.environment)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("kms: failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("kms: request failed for secret %q: %w", name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("kms: failed to read response for secret %q: %w", name, err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("kms: secret %q (project=%s) returned status %d: %s",
			name, projectID, resp.StatusCode, string(body))
	}

	var kmsResp kmsSecretResponse
	if err := json.Unmarshal(body, &kmsResp); err != nil {
		return "", fmt.Errorf("kms: failed to parse response for secret %q: %w", name, err)
	}

	value := kmsResp.Secret.SecretValue

	kmsSecMu.Lock()
	kmsSecrets[cacheKey] = &kmsSecretEntry{value: value, fetchedAt: time.Now()}
	kmsSecMu.Unlock()

	return value, nil
}

// ── Public API ──────────────────────────────────────────────────────────────

// ResolveProviderSecret resolves KMS-backed secret fields for a provider.
// If KMS is configured and provider fields start with "kms://", each secret
// is fetched from KMS. Otherwise, DB values are used as-is.
//
// Supported provider fields:
//   - ClientSecret
//   - UserKey
//   - SignKey
//
// Convention: store "kms://SECRET_NAME" in these fields in the database.
// At runtime, they are resolved to actual secret values.
//
// Multi-tenant scoping:
//   - Admin-owned providers use the default KMS_PROJECT_ID
//   - Org-owned providers can set "kms-project:{projectId}" in ConfigText
//     to scope secrets to the org's own KMS project
func ResolveProviderSecret(provider *Provider) error {
	initKMS()

	if kms == nil || provider == nil {
		return nil // KMS disabled, use DB value as-is
	}

	hasKmsRef := strings.HasPrefix(provider.ClientSecret, "kms://") ||
		strings.HasPrefix(provider.UserKey, "kms://") ||
		strings.HasPrefix(provider.SignKey, "kms://")
	if !hasKmsRef {
		return nil // Not a KMS reference
	}

	// Determine project ID: org-specific or system default.
	// Org-owned providers can store "kms-project:{id}" in ConfigText
	// to scope secrets to the org's KMS project.
	projectID := kms.projectID
	if provider.ConfigText != "" {
		for _, line := range strings.Split(provider.ConfigText, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "kms-project:") {
				projectID = strings.TrimPrefix(line, "kms-project:")
				break
			}
		}
	}

	if projectID == "" {
		return fmt.Errorf("kms: no project ID for provider %q (set KMS_PROJECT_ID or provider ConfigText 'kms-project:{id}')", provider.Name)
	}

	resolveField := func(fieldName string, currentValue string) (string, error) {
		if !strings.HasPrefix(currentValue, "kms://") {
			return currentValue, nil
		}

		secretName := strings.TrimPrefix(currentValue, "kms://")
		if secretName == "" {
			return "", fmt.Errorf("kms: empty secret reference for provider %q field %s", provider.Name, fieldName)
		}

		value, err := kms.getSecret(secretName, projectID)
		if err != nil {
			return "", fmt.Errorf("failed to resolve KMS secret for provider %q field %s: %w", provider.Name, fieldName, err)
		}

		return value, nil
	}

	clientSecret, err := resolveField("clientSecret", provider.ClientSecret)
	if err != nil {
		return err
	}
	userKey, err := resolveField("userKey", provider.UserKey)
	if err != nil {
		return err
	}
	signKey, err := resolveField("signKey", provider.SignKey)
	if err != nil {
		return err
	}

	provider.ClientSecret = clientSecret
	provider.UserKey = userKey
	provider.SignKey = signKey
	return nil
}

// GetKMSSecret fetches a secret by name from KMS using the default system project.
// This is a convenience function for non-provider secrets.
func GetKMSSecret(name string) (string, error) {
	initKMS()

	if kms == nil {
		return "", fmt.Errorf("kms: not configured")
	}

	if kms.projectID == "" {
		return "", fmt.Errorf("kms: KMS_PROJECT_ID not set")
	}

	return kms.getSecret(name, kms.projectID)
}

// GetOrgKMSSecret fetches a secret scoped to an organization's KMS project.
func GetOrgKMSSecret(name string, orgProjectID string) (string, error) {
	initKMS()

	if kms == nil {
		return "", fmt.Errorf("kms: not configured")
	}

	if orgProjectID == "" {
		return "", fmt.Errorf("kms: org project ID is empty")
	}

	return kms.getSecret(name, orgProjectID)
}
