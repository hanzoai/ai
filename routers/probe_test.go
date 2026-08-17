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

// probe is a request to call a filter with.
type probe struct{ *zip.Ctx }

// ask builds a request for method and path.
func ask(method, path string) probe {
	return probe{zip.New(zip.Config{DisableStartupMessage: true}).TestCtx(method, path)}
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

// status is the status the filter answered, or 200 when it answered nothing —
// which is what a filter that continued looks like.
func (p probe) status() int { return p.Fiber().Response().StatusCode() }

// replied is a header the filter set on the response.
func (p probe) replied(name string) string {
	return string(p.Fiber().Response().Header.Peek(name))
}

// said is the body the filter wrote.
func (p probe) said() string { return string(p.Fiber().Response().Body()) }
