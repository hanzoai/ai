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

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

// cloudRespStatus reads the status + error text back out of a native cloud
// response message (the same shape BuildCloudResponse writes).
func cloudRespStatus(t *testing.T, msg *zap.Message) (uint32, []byte, string) {
	t.Helper()
	if msg == nil {
		t.Fatal("nil response message")
	}
	root := msg.Root()
	return root.Uint32(object.CloudRespStatus), root.Bytes(object.CloudRespBody), root.Text(object.CloudRespError)
}

// TestZapEmbeddingsRerankRegistered proves the group self-registers its native
// method names and gateway path prefixes via init() — the strangler wiring
// Integrate flips handleCloudService / handleGatewayHTTPRequest onto.
func TestZapEmbeddingsRerankRegistered(t *testing.T) {
	if _, ok := lookupCloudHandler("embeddings"); !ok {
		t.Fatal("cloud method \"embeddings\" not registered")
	}
	if _, ok := lookupCloudHandler("rerank"); !ok {
		t.Fatal("cloud method \"rerank\" not registered")
	}
	if _, ok := lookupGatewayHandler("/v1/embeddings"); !ok {
		t.Fatal("gateway path /v1/embeddings not registered")
	}
	if _, ok := lookupGatewayHandler("/v1/rerank/anything"); !ok {
		t.Fatal("gateway prefix /v1/rerank not matched")
	}
}

// TestZapEmbeddingsAuthRejected asserts the ONE auth seam: an empty credential
// is 401 before any body work, matching the beego path's credential-first rule.
func TestZapEmbeddingsAuthRejected(t *testing.T) {
	msg, err := zapEmbeddingsHandler(context.Background(), "", []byte(`{"model":"m","input":"x"}`))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if status, _, _ := cloudRespStatus(t, msg); status != 401 {
		t.Fatalf("empty auth: got status %d want 401", status)
	}

	msg, _ = zapRerankHandler(context.Background(), "", []byte(`{"model":"m","query":"q","documents":["d"]}`))
	if status, _, _ := cloudRespStatus(t, msg); status != 401 {
		t.Fatalf("rerank empty auth: got status %d want 401", status)
	}
}

// TestZapEmbeddingsValidation asserts the required-field contract (400s) is
// preserved verbatim from ApiController.Embeddings / .Rerank. A bogus bearer is
// enough because these checks run before provider resolution.
func TestZapEmbeddingsValidation(t *testing.T) {
	cases := []struct {
		name string
		fn   func() (*zap.Message, error)
	}{
		{"embeddings-bad-json", func() (*zap.Message, error) {
			return zapEmbeddingsHandler(context.Background(), "Bearer x", []byte(`{`))
		}},
		{"embeddings-no-model", func() (*zap.Message, error) {
			return zapEmbeddingsHandler(context.Background(), "Bearer x", []byte(`{"input":"hi"}`))
		}},
		{"rerank-no-model", func() (*zap.Message, error) {
			return zapRerankHandler(context.Background(), "Bearer x", []byte(`{"query":"q","documents":["d"]}`))
		}},
		{"rerank-no-query", func() (*zap.Message, error) {
			return zapRerankHandler(context.Background(), "Bearer x", []byte(`{"model":"m","documents":["d"]}`))
		}},
		{"rerank-empty-docs", func() (*zap.Message, error) {
			return zapRerankHandler(context.Background(), "Bearer x", []byte(`{"model":"m","query":"q","documents":[]}`))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, err := c.fn()
			if err != nil {
				t.Fatalf("handler err: %v", err)
			}
			if status, _, _ := cloudRespStatus(t, msg); status != 400 {
				t.Fatalf("got status %d want 400", status)
			}
		})
	}
}

// TestZapProxyJSONHappyPath drives the shared terminal (zapProxyJSON) against a
// stubbed upstream provider: it must proxy the body, return the upstream status
// verbatim, and pass the response bytes through unchanged. authUser is nil so
// the metering path is exercised as a no-op (no DB), isolating proxy+encode.
func TestZapProxyJSONHappyPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assert the model rewrite / passthrough reached us as JSON.
		var got map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("upstream decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer upstream.Close()

	provider := &object.Provider{
		Name:        "stub",
		Type:        "OpenAI",
		ProviderUrl: upstream.URL,
	}

	msg, err := zapProxyJSON(context.Background(), provider, "embeddings",
		[]byte(`{"model":"text-embedding-3-small","input":"hello"}`),
		"text-embedding-3-small", nil, false, time.Now().UTC())
	if err != nil {
		t.Fatalf("zapProxyJSON err: %v", err)
	}
	status, body, errText := cloudRespStatus(t, msg)
	if status != 200 {
		t.Fatalf("got status %d want 200 (err=%q)", status, errText)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp["object"] != "list" {
		t.Fatalf("upstream body not passed through verbatim: %s", body)
	}
}
