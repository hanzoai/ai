// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// standInVendor answers free with text and refuses every other model.
func standInVendor(t *testing.T, free, text string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(string(body), `"model":"`+free+`"`) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"down"}}`)
			return
		}
		fmt.Fprintf(w, `{"id":"gen-1","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`, free, liftQuote(text))
	}))
	t.Cleanup(s.Close)
	return s
}

// What reads an answer's tool calls is the markup of the model that wrote it: when a
// stand-in answers, its id picks the family, not the name the caller asked by.
func TestLiftFamilyIsTheModelThatServed(t *testing.T) {
	hermesCall := `<tool_call>{"name":"exec_command","arguments":{"cmd":"ls"}}</tool_call>`
	for name, c := range map[string]struct {
		asked, free, text string
		calls             int
	}{
		"asked reads, stand-in does not": {"zen-flash", "vendor/big:free", "ok\n" + hermesCall, 0},
		"stand-in reads, asked does not": {"enso-flash", "deepseek/deepseek-chat:free", liftSample, 1},
	} {
		t.Run(name, func(t *testing.T) {
			cooled.forget()
			vendor := standInVendor(t, c.free, c.text)
			spareFamily(t, vendor.URL, c.free)
			fam := otherFamily(t, vendor.URL)
			body := []byte(`{"model":"` + c.asked + `",` + liftTools + `,"messages":[{"role":"user","content":"go"}]}`)
			ctl := visit(http.MethodPost, "/v1/chat/completions")
			ctl.Fiber().Request().SetBody(body)
			if out := ctl.pipeToFamily(fam, "chat/completions", "openai", c.asked, body, false, 0, "acme", nil, false, nil, time.Now()); out != nil {
				t.Fatalf("not served: %+v", out)
			}
			h := liftHear(t, sent(ctl))
			if len(h.names) != c.calls || (c.calls == 0 && h.text != c.text) {
				t.Fatalf("liftHeard %+v, want %d calls", h, c.calls)
			}
		})
	}
}
