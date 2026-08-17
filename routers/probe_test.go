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

package routers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/zap-proto/zip"
)

// How a test in this package calls a filter. One way.
//
// A filter takes the request's context and answers on it, so a test needs one —
// and zip builds it over a synthetic fasthttp request, which is what a live
// request arrives on. The thing under test therefore sees exactly what production
// hands it.
//
// It replaced an httptest request plus an httptest recorder plus a Reset that
// married them, with the answer read off the recorder afterwards. The request and
// the response live on the same context now, so there is one object and the
// assertions read from where the filter wrote.

// probe is a request, and — once it has been run — what came back.
type probe struct {
	*zip.Ctx
	code    int
	head    http.Header
	wrote   string
	ran     bool
	reached context.Context
}

// ask builds a request for method and target — a path, plus a query string if any.
func ask(method, target string) probe {
	p := probe{Ctx: zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10}).TestCtx(method, target)}
	// Whole, not just the path: TestCtx sets the path, so a query string would arrive
	// as part of it and no parameter would be readable. See visit in the controllers
	// probe for the same note.
	p.Fiber().Request().SetRequestURI(target)
	return p
}

// with sets a request header and returns the probe, so a request reads as one
// expression.
func (p probe) with(name, value string) probe {
	p.Fiber().Request().Header.Set(name, value)
	return p
}

// secure marks the request as having arrived over TLS, which is what the socket
// records on a direct https connection.
func (p probe) secure() probe {
	p.Fiber().Request().URI().SetScheme("https")
	return p
}

// body sets the request body.
func (p probe) body(b []byte) probe {
	p.Fiber().Request().SetBody(b)
	return p
}

// through runs the request through f, in an app.
//
// A filter is MIDDLEWARE: it ends in Continue(), and Continue means "the next
// handler in the chain" — which a detached context does not have, so calling one
// directly panics inside fiber's Next. zip's own doc says as much: TestCtx is for
// calling ONE handler, and a chain belongs to an app. Running it here also puts the
// filter in the position it actually occupies, ahead of a handler.
//
// The terminal handler answers 200 and writes nothing, so a status is the FILTER's
// whenever it refused and 200 whenever it let the request through — which is exactly
// the distinction every one of these tests is making.
func (p probe) through(f zip.Handler) probe {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	app.Use(zip.H(f))
	// The terminal handler records the CONTEXT VALUE, because some filters answer by
	// leaving something on it rather than by writing — the tenant attribution is the
	// whole example. The value, not the Ctx: fiber pools its Ctx and releases it when
	// the request ends, so a Ctx read afterwards is a recycled object with none of
	// this request on it. Read it with left().
	var reached context.Context
	app.All("/*", func(c *zip.Ctx) error {
		reached = c.Context()
		return nil
	})

	req := httptest.NewRequest(p.Method(), p.Path(), bytes.NewReader(p.Fiber().Request().Body()))
	p.Fiber().Request().Header.VisitAll(func(k, v []byte) {
		req.Header.Set(string(k), string(v))
	})
	if p.Fiber().Request().URI().Scheme() != nil && string(p.Fiber().Request().URI().Scheme()) == "https" {
		req.Header.Set("X-Forwarded-Proto", "https")
	}

	resp, err := app.Fiber().Test(req)
	if err != nil {
		// Never a silent zero. A status of 0 reads as "the filter answered nothing"
		// and sent three assertions looking for a bug in the filter that was in here.
		panic("probe: running the filter failed: " + err.Error())
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	p.code, p.head, p.wrote, p.ran = resp.StatusCode, resp.Header, string(body), true
	p.reached = reached
	return p
}

// left is the context the request carried INTO the handler — what a filter that
// answers by annotating rather than by writing actually changed. Nil when the filter
// refused, which is itself the answer.
func (p probe) left() context.Context { return p.reached }

// status is what the request was answered with: the filter's refusal, or 200 when it
// continued to the terminal handler.
func (p probe) status() int {
	if p.ran {
		return p.code
	}
	return p.Fiber().Response().StatusCode()
}

// replied is a header on the response.
func (p probe) replied(name string) string {
	if p.ran {
		return p.head.Get(name)
	}
	return string(p.Fiber().Response().Header.Peek(name))
}

// said is the body written.
func (p probe) said() string {
	if p.ran {
		return p.wrote
	}
	return string(p.Fiber().Response().Body())
}

// built is the app the document tests read from — registered the way the runtime
// registers it, so the document describes what is actually served.
func built() *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	Register(app)
	return app
}
