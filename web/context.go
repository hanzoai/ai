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

import "net/http"

// Context carries one request's reader and writer: the Input for request
// data, the Output for the response, and the raw Request plus the wrapped
// ResponseWriter.
type Context struct {
	Input          *Input
	Output         *Output
	Request        *http.Request
	ResponseWriter *Response
}

// NewContext returns a Context with an initialized Input and Output.
func NewContext() *Context {
	return &Context{Input: NewInput(), Output: &Output{}}
}

// Reset rebinds the Context to a request/response pair and clears the
// per-request state on the Input, Output and ResponseWriter.
func (ctx *Context) Reset(rw http.ResponseWriter, r *http.Request) {
	ctx.Request = r
	if ctx.ResponseWriter == nil {
		ctx.ResponseWriter = &Response{}
	}
	ctx.ResponseWriter.reset(rw)
	ctx.Input.Reset(ctx)
	ctx.Output.Reset(ctx)
}

// Redirect writes an HTTP redirect to localurl with the given status.
func (ctx *Context) Redirect(status int, localurl string) {
	http.Redirect(ctx.ResponseWriter, ctx.Request, localurl, status)
}
