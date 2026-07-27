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

// These are operator SCRIPTS, not tests: they assert nothing, print results
// with fmt.Printf, panic instead of failing, and need a populated database,
// live provider credentials or local fixture files this repository does not
// contain. Run one deliberately:
//
//	go test -tags ai_scripts ./<pkg>/ -run TestName -v
//
// Behind a tag because a panicking test aborts the whole test binary, so one
// missing fixture took every real test in the package down with it. Several
// also WRITE to whatever database they find, which `go test ./...` must not.
//go:build !skipCi && ai_scripts

package object

import (
	"fmt"
	"testing"

	"github.com/hanzoai/ai/proxy"
	"github.com/hanzoai/ai/util"
)

func TestTranslateArticle(t *testing.T) {
	InitConfig()
	proxy.InitHttpClient()
	article, err := getArticle("admin", "pml")
	if err != nil {
		panic(err)
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
			panic(err)
		}
		block.Text = answer
		fmt.Printf("[%d/%d] block type: %s, text EN: %s, text: %s\n", i+1, len(article.Content), block.Type, block.TextEn, block.Text)
		_, err = UpdateArticle(article.GetId(), article)
		if err != nil {
			panic(err)
		}
	}
}
