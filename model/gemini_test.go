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
	"github.com/hanzoai/ai/internal/gemini"
	"github.com/hanzoai/ai/proxy"
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
	client := gemini.New(apiKey, proxy.ProxyHttpClient)

	fmt.Println("Available Gemini Models:")
	fmt.Println("========================")

	pageToken := ""
	count := 1
	for {
		page, err := client.ListModels(ctx, 50, pageToken)
		if err != nil {
			t.Fatalf("Error listing models: %v", err)
		}

		for _, model := range page.Models {
			fmt.Printf("[%d] %s\n", count, model.Name)
			count++
		}

		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
}

func TestGeminiContents(t *testing.T) {
	contents := geminiContents("now", []*RawMessage{
		{Text: "before", Author: "user"},
		{Text: "reply", Author: "model"},
	})

	if len(contents) != 3 {
		t.Fatalf("got %d contents, want 3", len(contents))
	}
	want := []struct{ text, role string }{
		{"before", "user"}, {"reply", "model"}, {"now", gemini.RoleUser},
	}
	for i, w := range want {
		if got := contents[i].Parts[0].Text; got != w.text {
			t.Errorf("contents[%d].text = %q, want %q", i, got, w.text)
		}
		if got := contents[i].Role; got != w.role {
			t.Errorf("contents[%d].role = %q, want %q", i, got, w.role)
		}
	}
}
