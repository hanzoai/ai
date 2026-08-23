// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/txt"
)

func (c *ApiController) ResponseErrorStream(message *object.Message, errorText string) {
	var err error
	if message != nil {
		if !message.IsAlerted {
			err = message.SendErrorEmail(errorText, c.GetAcceptLanguage())
			if err != nil {
				errorText = fmt.Sprintf("%s\n%s", errorText, err.Error())
			}
		}

		if message.ErrorText != errorText || !message.IsAlerted || err != nil {
			message.ErrorText = errorText
			message.IsAlerted = true
			_, err = object.UpdateMessage(message.GetId(), message, false)
			if err != nil {
				errorText = fmt.Sprintf("%s\n%s", errorText, err.Error())
			}
		}
	}

	event := fmt.Sprintf("event: myerror\ndata: %s\n\n", errorText)
	err = c.Bytes(c.Fiber().Response().StatusCode(), []byte(event))
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
}

func refineQuestionTextViaParsingUrlContent(question string, lang string) (string, error) {
	re := regexp.MustCompile(`href="([^"]+)"`)
	urls := re.FindStringSubmatch(question)
	if len(urls) == 0 {
		return question, nil
	}

	href := urls[1]
	ext := filepath.Ext(href)
	content, err := txt.GetParsedTextFromUrl(href, ext, lang)
	if err != nil {
		return "", err
	}

	aTag := regexp.MustCompile(`<a\s+[^>]*href=["']([^"']+)["'][^>]*>.*?</a>`)
	res := aTag.ReplaceAllString(question, content)
	return res, nil
}

func ConvertMessageDataToJSON(data string) ([]byte, error) {
	jsonData := map[string]string{"text": data}
	jsonBytes, err := json.Marshal(jsonData)
	if err != nil {
		return nil, err
	}
	return jsonBytes, nil
}
