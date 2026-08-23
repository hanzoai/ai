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
	"bufio"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/hanzoai/ai/util"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/txt"
)

// noteFailure records a failure on the message and answers the text to tell the
// client. Recording it and telling them are separate things: there is one way to
// record, and two connections a client may be listening on.
func noteFailure(message *object.Message, errorText string, lang string) string {
	if message == nil {
		return errorText
	}
	var err error
	if !message.IsAlerted {
		if err = message.SendErrorEmail(errorText, lang); err != nil {
			errorText = fmt.Sprintf("%s\n%s", errorText, err.Error())
		}
	}
	if message.ErrorText != errorText || !message.IsAlerted || err != nil {
		message.ErrorText = errorText
		message.IsAlerted = true
		if _, err = object.UpdateMessage(message.GetId(), message, false); err != nil {
			errorText = fmt.Sprintf("%s\n%s", errorText, err.Error())
		}
	}
	return errorText
}

// errorEvent is the one shape a failure takes on a chat stream.
func errorEvent(text string) string {
	return fmt.Sprintf("event: myerror\ndata: %s\n\n", text)
}

// ResponseErrorStream tells a client still holding the request's own connection.
func (c *ApiController) ResponseErrorStream(message *object.Message, errorText string) {
	text := noteFailure(message, errorText, c.GetAcceptLanguage())
	if err := c.Bytes(c.Fiber().Response().StatusCode(), []byte(errorEvent(text))); err != nil {
		c.ResponseError(err.Error())
	}
}

// streamError records a failure on the message and tells the client through the
// stream's own writer.
//
// It is the in-stream counterpart of ResponseErrorStream, and the difference is
// which connection is still there to answer on. Once a streamed body is being
// produced, fasthttp is draining the writer from its own goroutine and the
// request context has been released — so the error event goes out through w, and
// the language has to be carried in rather than read off a header.
func streamError(w *bufio.Writer, message *object.Message, errorText string, lang string) {
	_, _ = w.WriteString(errorEvent(noteFailure(message, errorText, lang)))
	_ = w.Flush()
}

func refineQuestionTextViaParsingUrlContent(question string, lang string) (string, error) {
	re := regexp.MustCompile(`href="([^"]+)"`)
	urls := re.FindStringSubmatch(question)
	if len(urls) == 0 {
		return question, nil
	}

	// The href came out of the question a person typed, so it is a request for a
	// document and not yet an address. Every reader downstream takes a scheme-less
	// string as a path on this machine's disk, and an address inside this network
	// is our own neighbours — neither is something the answer may go and read.
	href := urls[1]
	if err := util.Fetchable(href); err != nil {
		return "", err
	}
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
