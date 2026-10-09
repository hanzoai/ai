// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"context"
	"strings"

	"github.com/valyala/fasthttp"
)

// Caller is the customer a program in this process makes a model call for: a
// sibling app's embeddings, rerank or completion bought over the host's plane. Org
// is whom the call is for; Person ("<org>/<name>") and Project where it names them.
//
// Only code in this process states one (ai.For): it rides the request as a value the
// network cannot write, so nothing a request carries can claim it. The program that
// states it meters the call itself and charges the customer for it, so the call is
// attributed to Org and debits no wallet here (PaysCaller): the customer is charged
// once, by the program, and the deployment's wallet not at all.
type Caller struct {
	Org     string
	Person  string
	Project string
}

type callerKey struct{}

// SetCaller states c on a request this process is about to serve.
func SetCaller(fctx *fasthttp.RequestCtx, c Caller) {
	fctx.SetUserValue(callerKey{}, c)
}

// RequestCaller is the caller stated on fctx, if a program in this process stated one
// with an org.
func RequestCaller(fctx *fasthttp.RequestCtx) (Caller, bool) {
	c, ok := fctx.UserValue(callerKey{}).(Caller)
	return c, ok && strings.TrimSpace(c.Org) != ""
}

// WithCaller is ctx carrying c.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerOf is the caller ctx carries, if any.
func CallerOf(ctx context.Context) (Caller, bool) {
	if ctx == nil {
		return Caller{}, false
	}
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}
