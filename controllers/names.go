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

// names.go — one request, read one way.
//
// encoding/json matches a struct field's name without regard to case — by Unicode
// simple folding, the rule bytes.EqualFold applies, so "Stream" and "ſtream" are both
// "stream" — and keeps the last of two keys that match. Every vendor reads the exact
// name. The relay sends the caller's body as written, and this handler decides the
// hold, the stream and the price from its own decode of the same body, so a key that
// differs from a field only in case makes one request two: "stream":true beside
// "Stream":false is billed as a buffered answer while the vendor streams, and a small
// "Messages" beside a large "messages" prices the hold on the one the vendor never
// reads.
//
// So a key that folds onto a field this handler decodes, or onto a sibling key, and
// is not that field's exact name, is refused by name — in the body and in every
// object below it the handler decodes into a struct. Objects decoded into a map keep
// their keys exactly, and a tool's JSON schema may name "Name" beside "name", so
// nothing below those is looked at.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"unicode"

	openai "github.com/hanzoai/go-openai"
)

// structured are the objects the handler decodes into structs, by where they sit in the
// body, each with its fields' names keyed by their fold.
var structured = map[string]map[string]string{
	"":                                 fieldNames(chatRequest{}),
	"stream_options":                   fieldNames(openai.StreamOptions{}),
	"messages[]":                       fieldNames(openai.ChatCompletionMessage{}),
	"messages[].content[]":             fieldNames(openai.ChatMessagePart{}),
	"messages[].content[].image_url":   fieldNames(openai.ChatMessageImageURL{}),
	"messages[].tool_calls[]":          fieldNames(openai.ToolCall{}),
	"messages[].tool_calls[].function": fieldNames(openai.FunctionCall{}),
	"tools[]":                          fieldNames(openai.Tool{}),
	"tools[].function":                 fieldNames(openai.FunctionDefinition{}),
}

// fieldNames is every name encoding/json would match for v's fields, keyed by fold:
// the json tag, and those of embedded structs. An untagged field maps to "", which no
// key equals, so every spelling of its Go name is refused.
func fieldNames(v any) map[string]string {
	out := map[string]string{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if f.Anonymous && tag == "" && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if name == "-" || !f.IsExported() {
				continue
			}
			if name == "" {
				// An untagged field is matched by its Go name, which no vendor reads:
				// every spelling of it is one the handler would read alone.
				out[fold(f.Name)] = ""
				continue
			}
			out[fold(name)] = name
		}
	}
	walk(reflect.TypeOf(v))
	return out
}

// fold maps s to one spelling per bytes.EqualFold class: each rune becomes the least
// rune of its simple-folding orbit.
func fold(s string) string {
	var b strings.Builder
	for _, r := range s {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		b.WriteRune(least)
	}
	return b.String()
}

// casefolded names the first key in raw that this handler would read as a field and a
// vendor would not, or "" when every key is exact. A body that is not JSON is left to
// the decoder, which has already refused it.
func casefolded(raw []byte) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	bad, _ := walkNames(dec, "")
	return bad
}

// walkNames reads one value from dec, sitting at path, and returns the first
// offending key within it. A value at no path the handler decodes into a struct is
// skipped whole, at the decoder's own speed: the walk runs before the credential is
// read, so it must cost no more than the decode it guards.
func walkNames(dec *json.Decoder, path string) (string, error) {
	if !under(path) {
		var skip json.RawMessage
		return "", dec.Decode(&skip)
	}
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	switch tok {
	case json.Delim('{'):
		fields, checked := structured[path]
		seen := map[string]string{}
		for dec.More() {
			t, err := dec.Token()
			if err != nil {
				return "", err
			}
			key, _ := t.(string)
			below := "-"
			if checked {
				f := fold(key)
				if exact, ok := fields[f]; ok && exact != key {
					return key, nil
				}
				if prev, ok := seen[f]; ok && prev != key {
					return key, nil
				}
				seen[f] = key
				below = strings.TrimPrefix(path+"."+key, ".")
			}
			if bad, err := walkNames(dec, below); bad != "" || err != nil {
				return bad, err
			}
		}
		_, err = dec.Token()
		return "", err
	case json.Delim('['):
		element := path + "[]"
		for dec.More() {
			if bad, err := walkNames(dec, element); bad != "" || err != nil {
				return bad, err
			}
		}
		_, err = dec.Token()
		return "", err
	}
	return "", nil
}

// under reports a path that is, or leads to, an object the handler decodes into a
// struct.
func under(path string) bool {
	for p := range structured {
		if p == path || strings.HasPrefix(p, path+".") || strings.HasPrefix(p, path+"[]") {
			return true
		}
	}
	return false
}
