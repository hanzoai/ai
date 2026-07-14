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

package object

import "context"

// Request-scoped observability attribution carried on the Go request context.
//
// The TenantContextFilter (routers) stashes the caller's project sub-scope and a
// non-reversible ref of the caller credential here; recordTrace (controllers)
// reads it once to stamp BOTH the cloud_usage ledger row and the gen_ai span. This
// lives in `object` (the shared lower layer both routers and controllers import)
// so neither package depends on the other — one context key, one home.

type genAIAttrKey struct{}

// GenAIAttribution is the per-request attribution the gen_ai span + cloud_usage
// ledger stamp. Both fields are optional; an empty field emits/stores nothing.
type GenAIAttribution struct {
	// Project is the caller's org SUB-SCOPE (X-Project-Id); "" is the default project.
	Project string
	// APIKeyHash is a SHA-256 hex ref of the caller credential — NEVER the plaintext key.
	APIKeyHash string
}

// WithGenAIAttribution returns ctx carrying a. When a is empty it returns ctx
// unchanged (no allocation, no value to read back).
func WithGenAIAttribution(ctx context.Context, a GenAIAttribution) context.Context {
	if ctx == nil {
		return ctx
	}
	if a.Project == "" && a.APIKeyHash == "" {
		return ctx
	}
	return context.WithValue(ctx, genAIAttrKey{}, a)
}

// GenAIAttributionFromContext returns the attribution stashed on ctx, or the zero
// value when none is present.
func GenAIAttributionFromContext(ctx context.Context) GenAIAttribution {
	if ctx == nil {
		return GenAIAttribution{}
	}
	a, _ := ctx.Value(genAIAttrKey{}).(GenAIAttribution)
	return a
}
