// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"bytes"

	"github.com/zap-proto/zip"
)

// How a test in this package gets a controller. One way.
//
// A controller IS its request — the resolved principal, the org, the writer it
// answers through — so a test needs a real context to hold one, and zip builds that
// over a synthetic fasthttp request. The thing under test therefore sees exactly
// what production hands it.
//
// It replaced an httptest request, an httptest recorder, a router context, a Reset
// to marry them and an Init to register the controller against a name — five steps
// whose only purpose was to hand a handler somewhere to read from and write to.
// Both live on the context now.

// visit builds a controller for one request.
func visit(method, path string) *ApiController {
	return &ApiController{
		Ctx: zip.New(zip.Config{DisableStartupMessage: true}).TestCtx(method, path),
	}
}

// sent is the body the controller wrote.
func sent(c *ApiController) string { return string(c.Fiber().Response().Body()) }

// answered is the status the controller set, or 200 when it set none.
func answered(c *ApiController) int { return c.Fiber().Response().StatusCode() }

// stream is a destination for the SSE relays, which take the writer they write to
// rather than reaching for one. Read it with String once the relay has returned.
type stream struct {
	buf bytes.Buffer
	w   *bufio.Writer
}

func toStream() *stream {
	s := &stream{}
	s.w = bufio.NewWriter(&s.buf)
	return s
}

// String flushes and returns everything the relay wrote.
func (s *stream) String() string {
	_ = s.w.Flush()
	return s.buf.String()
}
