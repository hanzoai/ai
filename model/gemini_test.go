// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

//go:build !skipCi

package model

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/proxy"
	"google.golang.org/genai"
)

func TestListGeminiModels(t *testing.T) {
	// The key was blanked in source — correctly, it does not belong here — but
	// the test stayed, so it could only ever panic with "api key is required".
	// Read it from the environment and skip without one, the same shape
	// TestGenerateImageDOAI_Live already uses:
	//
	//	GEMINI_API_KEY=… go test ./model/ -run TestListGeminiModels -v
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set — skipping live Gemini model listing")
	}

	if err := conf.LoadAppConfig("ini", "../conf/app.conf"); err != nil {
		t.Fatalf("load config: %v", err)
	}

	proxy.InitHttpClient()

	ctx := context.Background()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: proxy.ProxyHttpClient,
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("Available Gemini Models:")
	fmt.Println("========================")

	pageToken := ""
	count := 1
	for {
		listOpts := &genai.ListModelsConfig{
			PageSize:  50,
			PageToken: pageToken,
		}

		resp, err := client.Models.List(ctx, listOpts)
		if err != nil {
			t.Fatalf("Error listing models: %v", err)
		}

		for _, model := range resp.Items {
			fmt.Printf("[%d] %s\n", count, model.Name)
			count++
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
}
