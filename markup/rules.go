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

package markup

import (
	"cmp"
	"encoding/json"
)

// RulesFor is what may be lifted from the answer to a chat request body served by
// the upstream model: the tools the request offered, and nothing (false) when it
// offered none, forbade calling them, named a function the legacy way, asked to be
// served unchanged (strict), or carries markup of the model's family in a message.
func RulesFor(body []byte, upstream string, strict bool) (Rules, bool) {
	if strict || !Reads(upstream) {
		return Rules{}, false
	}
	var r struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Functions  []json.RawMessage `json:"functions"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
		Parallel   *bool             `json:"parallel_tool_calls"`
		Messages   []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Functions) > 0 || string(r.ToolChoice) == `"none"` {
		return Rules{}, false
	}
	rules := Rules{Offered: map[string]bool{}, Model: upstream, One: r.Parallel != nil && !*r.Parallel}
	for _, t := range r.Tools {
		if t.Function.Name != "" {
			rules.Offered[t.Function.Name] = true
		}
	}
	if len(rules.Offered) == 0 {
		return Rules{}, false
	}
	type named struct {
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	var forced struct {
		Type     string  `json:"type"`
		Tools    []named `json:"tools"` // the Responses spelling of the allowed set
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Allowed struct {
			Tools []named `json:"tools"`
		} `json:"allowed_tools"`
	}
	if json.Unmarshal(r.ToolChoice, &forced) == nil {
		rules.Only = forced.Function.Name
		if forced.Type == "allowed_tools" {
			// Only the tools the caller allowed may be called, whatever else it offered.
			allowed := map[string]bool{}
			for _, t := range append(forced.Allowed.Tools, forced.Tools...) {
				allowed[cmp.Or(t.Function.Name, t.Name)] = true
			}
			for name := range rules.Offered {
				if !allowed[name] {
					delete(rules.Offered, name)
				}
			}
			if len(rules.Offered) == 0 {
				return Rules{}, false
			}
		}
	}
	for _, m := range r.Messages {
		for _, text := range textsOf(m.Content) {
			if Quotes(upstream, text) {
				return Rules{}, false
			}
		}
	}
	return rules, true
}

// textsOf is the text a message's content holds: the string, or each text part.
func textsOf(content json.RawMessage) []string {
	var one string
	if json.Unmarshal(content, &one) == nil {
		return []string{one}
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(content, &parts)
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.Text
	}
	return out
}

