// Copyright 2024 The Casibase Authors. All Rights Reserved.
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
	"encoding/json"
	"fmt"
	"strings"
)

const analyzeTaskPrompt = `请对以下教学设计文本进行深度分析，根据提供的评价量表对每个二级评价项进行评分（0-100分制）和详细分析。

评价量表：
%s

教学设计文本：
%s

请严格按照以下JSON格式返回分析结果，不要包含任何其他内容，只返回合法的JSON：
{
  "title": "从文档中提取的课题/单元名称",
  "designer": "从文档中提取的设计/实施者姓名或团队",
  "stage": "从文档中提取的学段（如：小学、初中、高中）",
  "participants": "从文档中提取的参与者描述",
  "grade": "从文档中提取的年级",
  "instructor": "从文档中提取的指导教师姓名",
  "subject": "从文档中提取的学科",
  "school": "从文档中提取的学校名称",
  "otherSubjects": "从文档中提取的其他相关领域或学科（逗号分隔）",
  "textbook": "从文档中提取的主要教材信息",
  "score": 所有二级评价项得分的平均值（保留一位小数的数字，不是字符串）,
  "categories": [
    {
      "name": "一级评价项名称",
      "score": 该类别下所有二级评价项得分的平均值（保留两位小数的数字）,
      "items": [
        {
          "name": "二级评价项名称",
          "score": 该项得分（0-100的整数）,
          "advantage": "优点分析（详细说明教学设计在该项的优势和亮点）",
          "disadvantage": "不足分析（详细说明教学设计在该项存在的问题和不足）",
          "suggestion": "改进建议（提供具体可操作的改进措施和建议）"
        }
      ]
    }
  ]
}`

func AnalyzeTask(task *Task, lang string) (*TaskResult, error) {
	if task.Text == "" {
		return nil, fmt.Errorf("task evaluation rubric (text) is empty")
	}
	if task.DocumentText == "" {
		return nil, fmt.Errorf("task document text is empty, please upload a document first")
	}

	question := fmt.Sprintf(analyzeTaskPrompt, task.Text, task.DocumentText)

	answer, _, err := GetAnswer(task.Provider, question, lang)
	if err != nil {
		return nil, fmt.Errorf("failed to get analysis from AI model: %v", err)
	}

	answer = strings.TrimSpace(answer)
	// Strip markdown code block if present
	if strings.HasPrefix(answer, "```json") {
		answer = strings.TrimPrefix(answer, "```json")
		answer = strings.TrimSuffix(answer, "```")
		answer = strings.TrimSpace(answer)
	} else if strings.HasPrefix(answer, "```") {
		answer = strings.TrimPrefix(answer, "```")
		answer = strings.TrimSuffix(answer, "```")
		answer = strings.TrimSpace(answer)
	}

	var result TaskResult
	if err = json.Unmarshal([]byte(answer), &result); err != nil {
		return nil, fmt.Errorf("failed to parse AI analysis result as JSON: %v\nRaw answer: %s", err, answer)
	}

	return &result, nil
}
