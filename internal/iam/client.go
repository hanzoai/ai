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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// AuthConfig is the connection config for a Client: the IAM server endpoint and
// the app credentials/context requests are made under.
type AuthConfig struct {
	Endpoint         string
	ClientId         string
	ClientSecret     string
	Certificate      string
	OrganizationName string
	ApplicationName  string
}

// Client talks to a Hanzo IAM server over its /v1/iam/ JSON REST API and
// verifies its tokens via published JWKS. It is the one place the endpoint and
// app credentials live.
type Client struct {
	AuthConfig
	CustomHeaders map[string]string
}

// Status is the IAM envelope's status field.
//
// The server has emitted it BOTH as a string ("ok") and — since iam v1.33.x — as a
// JSON number (200). A plain `string` field fails the ENTIRE decode on the numeric
// form, and the one caller that matters treats a decode failure as "IAM unreachable"
// and continues with auth features switched off. The pod still passes its probes, so
// nothing crashes and nothing reverts: the binary just serves 401 to every
// authenticated call. One field's wire drift silently disarms authentication.
//
// Accepting both shapes removes that failure mode at the only place it can occur.
type Status string

// UnmarshalJSON accepts the string form, the numeric form, or null, and normalizes to
// the string the callers compare against. A 2xx number IS the success code, so it
// normalizes to "ok"; any other number keeps its digits so a real error still reads as
// one rather than being laundered into success. Unknown shapes are an error, not a
// guess — silently accepting them is what caused this.
func (s *Status) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = Status(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("iam: status is neither string nor number: %s", b)
	}
	code, err := n.Int64()
	if err != nil {
		return fmt.Errorf("iam: status %q is not an integer: %w", n, err)
	}
	if code >= 200 && code < 300 {
		*s = "ok"
		return nil
	}
	*s = Status(n.String())
	return nil
}

// Response is the IAM JSON envelope: {status, msg, data, data2}.
type Response struct {
	Status Status      `json:"status"`
	Msg    string      `json:"msg"`
	Data   interface{} `json:"data"`
	Data2  interface{} `json:"data2"`
}

// HttpClient is the minimal http doer a Client uses; *http.Client satisfies it.
type HttpClient interface {
	Do(*http.Request) (*http.Response, error)
}

var (
	client       HttpClient = &http.Client{}
	globalClient *Client
)

// SetHttpClient overrides the shared http client (tests).
func SetHttpClient(httpClient HttpClient) { client = httpClient }

// InitConfig sets the package-global Client used by the package-level helpers
// (GetUser, ParseJwtToken, …). ai's account bootstrap calls this with the
// deployed IAM endpoint and app credentials.
func InitConfig(endpoint, clientId, clientSecret, certificate, organizationName, applicationName string) {
	globalClient = NewClient(endpoint, clientId, clientSecret, certificate, organizationName, applicationName)
}

// NewClient builds a Client for an explicit endpoint + app context.
func NewClient(endpoint, clientId, clientSecret, certificate, organizationName, applicationName string) *Client {
	return NewClientWithConf(&AuthConfig{
		Endpoint:         endpoint,
		ClientId:         clientId,
		ClientSecret:     clientSecret,
		Certificate:      certificate,
		OrganizationName: organizationName,
		ApplicationName:  applicationName,
	})
}

// NewClientWithConf builds a Client from an AuthConfig.
func NewClientWithConf(config *AuthConfig) *Client {
	return &Client{AuthConfig: *config, CustomHeaders: make(map[string]string)}
}

// resolveEndpoint returns an IAM base URL: the explicit value if set, else the
// first non-empty IAM_ENDPOINT / IAM_ISSUER env var, else the https://hanzo.id
// default. This is what lets a package-level helper reach IAM even when
// InitConfig was never called.
func resolveEndpoint(explicit string) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit
	}
	for _, k := range []string{"IAM_ENDPOINT", "IAM_ISSUER"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return "https://hanzo.id"
}

// ensureClient returns the configured global Client, or lazily builds one from
// the environment so the read helpers never nil-panic.
func ensureClient() *Client {
	if globalClient != nil {
		return globalClient
	}
	return NewClient(resolveEndpoint(""), "", "", "", "", "")
}

// endpoint returns the client's endpoint, resolved from env when unset.
func (c *Client) endpoint() string { return resolveEndpoint(c.Endpoint) }

// GetUrl builds a /v1/iam/<action> URL with the given query params. Hanzo IAM
// serves its JSON API under /v1/iam/ only (the Casdoor /api/ prefix is retired).
func (c *Client) GetUrl(action string, queryMap map[string]string) string {
	query := ""
	for k, v := range queryMap {
		query += fmt.Sprintf("%s=%s&", url.QueryEscape(k), url.QueryEscape(v))
	}
	query = strings.TrimRight(query, "&")
	return fmt.Sprintf("%s/v1/iam/%s?%s", c.endpoint(), action, query)
}

// GetId returns "<org>/<name>".
func (c *Client) GetId(name string) string { return c.OrganizationName + "/" + name }

// DoGetResponse GETs url and returns the decoded IAM envelope, erroring on a
// non-"ok" status.
func (c *Client) DoGetResponse(url string) (*Response, error) {
	respBytes, err := c.doGetBytesRawWithoutCheck(url)
	if err != nil {
		return nil, err
	}
	var response Response
	if err = json.Unmarshal(respBytes, &response); err != nil {
		return nil, err
	}
	if response.Status != "ok" {
		return nil, errors.New(response.Msg)
	}
	return &response, nil
}

// DoGetBytes GETs url and returns the envelope's data field re-marshaled to JSON
// bytes, ready to unmarshal into a typed value.
func (c *Client) DoGetBytes(url string) ([]byte, error) {
	response, err := c.DoGetResponse(url)
	if err != nil {
		return nil, err
	}
	return json.Marshal(response.Data)
}

// DoPost posts to /v1/iam/<action>. isForm/isFile select multipart file upload,
// multipart form fields, or a raw text/plain body.
func (c *Client) DoPost(action string, queryMap map[string]string, postBytes []byte, isForm, isFile bool) (*Response, error) {
	postURL := c.GetUrl(action, queryMap)

	var (
		err         error
		contentType string
		body        io.Reader
	)
	switch {
	case isForm && isFile:
		contentType, body, err = createFormFile(map[string][]byte{"file": postBytes})
		if err != nil {
			return nil, err
		}
	case isForm:
		var params map[string]string
		if err = json.Unmarshal(postBytes, &params); err != nil {
			return nil, err
		}
		contentType, body, err = createForm(params)
		if err != nil {
			return nil, err
		}
	default:
		contentType = "text/plain;charset=UTF-8"
		body = bytes.NewReader(postBytes)
	}

	respBytes, err := c.doPostBytesRaw(postURL, contentType, body)
	if err != nil {
		return nil, err
	}
	var response Response
	if err = json.Unmarshal(respBytes, &response); err != nil {
		return nil, err
	}
	if response.Status != "ok" {
		return nil, errors.New(response.Msg)
	}
	return &response, nil
}

func (c *Client) doPostBytesRaw(postURL, contentType string, body io.Reader) ([]byte, error) {
	if contentType == "" {
		contentType = "text/plain;charset=UTF-8"
	}
	req, err := http.NewRequest(http.MethodPost, postURL, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.ClientId, c.ClientSecret)
	req.Header.Set("Content-Type", contentType)
	for k, v := range c.CustomHeaders {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusForbidden {
		return nil, fmt.Errorf("iam: status %d: %s", resp.StatusCode, string(respBytes))
	}
	return respBytes, nil
}

func (c *Client) doGetBytesRawWithoutCheck(getURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, getURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.ClientId, c.ClientSecret)
	for k, v := range c.CustomHeaders {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func createFormFile(formData map[string][]byte) (string, io.Reader, error) {
	body := new(bytes.Buffer)
	w := multipart.NewWriter(body)
	for k, v := range formData {
		pw, err := w.CreateFormFile(k, "file")
		if err != nil {
			return "", nil, err
		}
		if _, err = pw.Write(v); err != nil {
			return "", nil, err
		}
	}
	if err := w.Close(); err != nil {
		return "", nil, err
	}
	return w.FormDataContentType(), body, nil
}

func createForm(formData map[string]string) (string, io.Reader, error) {
	body := new(bytes.Buffer)
	w := multipart.NewWriter(body)
	for k, v := range formData {
		if err := w.WriteField(k, v); err != nil {
			return "", nil, err
		}
	}
	if err := w.Close(); err != nil {
		return "", nil, err
	}
	return w.FormDataContentType(), body, nil
}
