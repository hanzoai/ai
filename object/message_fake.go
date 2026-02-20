// Copyright 2026 The Casibase Authors. All Rights Reserved.
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

import "github.com/casibase/casibase/model"

// GetAnswerFake returns a hardcoded string without calling the LLM API, for fast local debugging.
// Switch between GetAnswer and GetAnswerFake by commenting one of the two call sites (e.g. in task_analyze.go).
// Replace fakeAnswer below with your real API response (multi-line raw string is fine).
func GetAnswerFake(provider string, question string, lang string) (string, *model.ModelResult, error) {
	const fakeAnswer = `{
  "title": "示例课题",
  "designer": "张三",
  "stage": "高中",
  "participants": "全班",
  "grade": "高一",
  "instructor": "李老师",
  "subject": "数学",
  "school": "示例中学",
  "otherSubjects": "",
  "textbook": "人教版",
  "score": 85.5,
  "categories": [
    {
      "name": "教学目标",
      "score": 86,
      "items": [
        {
          "name": "目标明确",
          "score": 88,
          "advantage": "表述清晰",
          "disadvantage": "可再具体",
          "suggestion": "补充可测指标"
        }
      ]
    }
  ]
}`
	return fakeAnswer, nil, nil
}
