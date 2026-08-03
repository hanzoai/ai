// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2024 The OpenAgent Authors. All Rights Reserved.
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

package object

import (
	"fmt"
	"testing"

	"github.com/hanzoai/ai/proxy"
	"github.com/hanzoai/ai/util"
)

// TestTranslateArticle drives the real translation path: it reads an article
// row, asks the configured model to translate each English block, and writes the
// result back. It therefore needs BOTH a configured database and a live model
// endpoint, so it skips rather than fails when either is absent — with no
// database it read nothing and dereferenced a nil article, and a panic in a test
// takes the whole package binary down with it, hiding every other result in it.
func TestTranslateArticle(t *testing.T) {
	InitConfig()
	proxy.InitHttpClient()
	article, err := getArticle("admin", "pml")
	if err != nil {
		t.Skipf("no article store configured: %v", err)
	}
	if article == nil {
		t.Skip("article admin/pml is not present; this test needs a seeded database")
	}
	glossary := util.StructToJsonNoIndent(article.Glossary)
	for i, block := range article.Content {
		if block.Text != "" || block.TextEn == "" {
			continue
		}
		question := fmt.Sprintf("Translate the following text to Chinese, the words related to this glossary: %s should not be translated. Only respond with the translated text:\n%s", glossary, block.TextEn)
		var answer string
		answer, _, err = GetAnswer("", question, "en")
		if err != nil {
			t.Skipf("no model endpoint configured: %v", err)
		}
		block.Text = answer
		fmt.Printf("[%d/%d] block type: %s, text EN: %s, text: %s\n", i+1, len(article.Content), block.Type, block.TextEn, block.Text)
		if _, err = UpdateArticle(article.GetId(), article); err != nil {
			t.Fatalf("write back translated block %d: %v", i+1, err)
		}
	}
}
