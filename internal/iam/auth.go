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

package iam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
)

// OAuthOption configures an OAuth request.
type OAuthOption func(*oauthOptions)

type oauthOptions struct {
	httpClient *http.Client
}

// WithHTTPClient sets a custom http client for the OAuth exchange.
func WithHTTPClient(httpClient *http.Client) OAuthOption {
	return func(opts *oauthOptions) { opts.httpClient = httpClient }
}

// GetOAuthToken exchanges an authorization code for a token against the IAM
// server's OAuth endpoints.
func (c *Client) GetOAuthToken(code, state string, opts ...OAuthOption) (*oauth2.Token, error) {
	options := &oauthOptions{}
	for _, opt := range opts {
		opt(options)
	}

	endpoint := c.endpoint()
	config := oauth2.Config{
		ClientID:     c.ClientId,
		ClientSecret: c.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   fmt.Sprintf("%s/v1/iam/oauth/authorize", endpoint),
			TokenURL:  fmt.Sprintf("%s/v1/iam/oauth/access_token", endpoint),
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}

	ctx := context.Background()
	if options.httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, options.httpClient)
	}

	token, err := config.Exchange(ctx, code)
	if err != nil {
		return token, err
	}
	if strings.HasPrefix(token.AccessToken, "error:") {
		return nil, errors.New(strings.TrimPrefix(token.AccessToken, "error: "))
	}
	return token, nil
}

// GetOAuthToken exchanges a code using the configured (or env-derived) client.
func GetOAuthToken(code, state string, opts ...OAuthOption) (*oauth2.Token, error) {
	return ensureClient().GetOAuthToken(code, state, opts...)
}
