// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Output writes the response status, headers and body. Response compression
// is handled at the edge (the cloud/gateway serving layer), so Output writes
// bodies uncompressed.
type Output struct {
	Context *Context
	Status  int
}

// Reset rebinds the Output to a context and clears the pending status.
func (output *Output) Reset(ctx *Context) {
	output.Context = ctx
	output.Status = 0
}

// Header sets a response header.
func (output *Output) Header(key, val string) {
	output.Context.ResponseWriter.Header().Set(key, val)
}

// SetStatus records the status to send with the next Body write. It does not
// write the header immediately.
func (output *Output) SetStatus(status int) {
	output.Status = status
}

// Body writes content as the response body. It sets Content-Length, sends a
// status set earlier via SetStatus (then clears it so a second write cannot
// emit a duplicate status line), and writes the bytes.
func (output *Output) Body(content []byte) error {
	output.Header("Content-Length", strconv.Itoa(len(content)))
	if output.Status != 0 {
		output.Context.ResponseWriter.WriteHeader(output.Status)
		output.Status = 0
	} else {
		output.Context.ResponseWriter.Started = true
	}
	_, err := output.Context.ResponseWriter.Write(content)
	return err
}

// JSON writes data as a JSON body. hasIndent pretty-prints; encoding escapes
// non-ASCII runes to \uXXXX. A marshal error becomes a 500.
func (output *Output) JSON(data interface{}, hasIndent bool, encoding bool) error {
	output.Header("Content-Type", "application/json; charset=utf-8")
	var content []byte
	var err error
	if hasIndent {
		content, err = json.MarshalIndent(data, "", "  ")
	} else {
		content, err = json.Marshal(data)
	}
	if err != nil {
		http.Error(output.Context.ResponseWriter, err.Error(), http.StatusInternalServerError)
		return err
	}
	if encoding {
		content = []byte(escapeNonASCII(string(content)))
	}
	return output.Body(content)
}

// Cookie adds a Set-Cookie response header. The optional args are positional,
// in order: max-age (int seconds; negative deletes), path (string, default
// "/"), domain (string), secure (bool) and http-only (bool).
func (output *Output) Cookie(name, value string, others ...interface{}) {
	ck := &http.Cookie{Name: name, Value: value, Path: "/"}
	if len(others) > 0 {
		switch v := others[0].(type) {
		case int:
			ck.MaxAge = v
		case int32:
			ck.MaxAge = int(v)
		case int64:
			ck.MaxAge = int(v)
		}
	}
	if len(others) > 1 {
		if v, ok := others[1].(string); ok && v != "" {
			ck.Path = v
		}
	}
	if len(others) > 2 {
		if v, ok := others[2].(string); ok && v != "" {
			ck.Domain = v
		}
	}
	if len(others) > 3 {
		if v, ok := others[3].(bool); ok {
			ck.Secure = v
		} else if others[3] != nil {
			ck.Secure = true
		}
	}
	if len(others) > 4 {
		if v, ok := others[4].(bool); ok {
			ck.HttpOnly = v
		}
	}
	if s := ck.String(); s != "" {
		output.Context.ResponseWriter.Header().Add("Set-Cookie", s)
	}
}

// escapeNonASCII replaces every rune above the ASCII range with its \uXXXX
// escape, leaving ASCII bytes untouched.
func escapeNonASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 {
			b.WriteRune(r)
			continue
		}
		b.WriteString("\\u")
		h := strconv.FormatInt(int64(r), 16)
		for i := len(h); i < 4; i++ {
			b.WriteByte('0')
		}
		b.WriteString(h)
	}
	return b.String()
}
