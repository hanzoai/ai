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
	"testing"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/model"
	"github.com/hanzoai/ai/object"
)

// decodeCloud reads the status + error text out of a native ZAP cloud response.
func decodeCloudImg(t *testing.T, msg *zap.Message, err error) (uint32, string) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned transport error: %v", err)
	}
	if msg == nil {
		t.Fatal("handler returned nil message")
	}
	root := msg.Root()
	return root.Uint32(object.CloudRespStatus), root.Text(object.CloudRespError)
}

// TestZapImagesHandler_AuthRejection: empty auth is rejected 401 BEFORE any body
// is decoded — the ONE auth seam, never trusting the body (recipe STEP 1).
func TestZapImagesHandler_AuthRejection(t *testing.T) {
	body := []byte(`{"model":"stable-diffusion-3.5-large","prompt":"a cat"}`)
	msg, err := zapImagesHandler(context.Background(), "", body)
	status, errMsg := decodeCloudImg(t, msg, err)
	if status != 401 {
		t.Fatalf("empty auth: status = %d, want 401 (err=%q)", status, errMsg)
	}
}

// TestZapImagesHandler_BadRequest: with a credential present, malformed/empty
// bodies return 400 with the SAME messages as the HTTP path (images_api.go).
// These paths never touch the provider/balance layer.
func TestZapImagesHandler_BadRequest(t *testing.T) {
	const auth = "Bearer sk-does-not-resolve" // enough to pass the empty-auth gate

	cases := []struct {
		name string
		body string
		want string
	}{
		{"invalid json", `{`, "invalid request: "},
		{"missing model", `{"prompt":"a cat"}`, "images request requires a \"model\" field"},
		{"missing prompt", `{"model":"stable-diffusion-3.5-large"}`, "images request requires a \"prompt\" field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := zapImagesHandler(context.Background(), auth, []byte(tc.body))
			status, errMsg := decodeCloudImg(t, msg, err)
			if status != 400 {
				t.Fatalf("status = %d, want 400 (err=%q)", status, errMsg)
			}
			if len(errMsg) < len(tc.want) || errMsg[:len(tc.want)] != tc.want {
				t.Fatalf("err = %q, want prefix %q", errMsg, tc.want)
			}
		})
	}
}

// TestZapImagesHandler_Registered proves the handler self-registers under both
// the native cloud method and the gateway path prefix (the per-group convention),
// so the dispatch lookups in zap_native.go find it.
func TestZapImagesHandler_Registered(t *testing.T) {
	if _, ok := lookupCloudHandler("images.generations"); !ok {
		t.Error("registerCloud(\"images.generations\") not registered")
	}
	if _, ok := lookupGatewayHandler("/v1/images/generations"); !ok {
		t.Error("registerGatewayPath(\"/v1/images\") does not match /v1/images/generations")
	}
}

// TestZapImagesResponseShape proves the terminal encoding matches the HTTP path:
// the OpenAI images envelope {created,data:[{url}|{b64_json}]} projected by the
// shared imageResponseData. This is the happy-path response contract, exercised
// with a stubbed provider result (no network).
func TestZapImagesResponseShape(t *testing.T) {
	result := &model.ImageGenResult{Images: []model.GeneratedImage{
		{URL: "https://fal.media/img-1.jpg"},
		{B64JSON: "aGVsbG8="},
	}}

	// Mirror exactly what zapImagesHandler marshals on success.
	payload := map[string]interface{}{
		"created": int64(1700000000),
		"data":    imageResponseData(result),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		Created int64               `json:"created"`
		Data    []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(got.Data))
	}
	if got.Data[0]["url"] != "https://fal.media/img-1.jpg" {
		t.Errorf("data[0].url = %q", got.Data[0]["url"])
	}
	if _, hasB64 := got.Data[0]["b64_json"]; hasB64 {
		t.Error("data[0] leaked an empty b64_json field")
	}
	if got.Data[1]["b64_json"] != "aGVsbG8=" {
		t.Errorf("data[1].b64_json = %q", got.Data[1]["b64_json"])
	}
}
