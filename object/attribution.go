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
// ledger stamp. Every field is optional; an empty field emits/stores nothing.
type GenAIAttribution struct {
	// Org is the caller's VERIFIED tenant org (GetOrg — the principal's real org, not
	// the raw X-Org-Id header). recordTrace stamps it onto the gen_ai span's
	// gen_ai.hanzo.org_id ONLY as a fallback when a producer left the record's
	// org/owner empty, so real tenant traffic always carries its org even on a code
	// path that forgot to set it. The o11y llmobs views apply a mandatory org filter
	// that silently drops empty-org spans — this closes that hole.
	Org string
	// Project is the caller's org SUB-SCOPE (X-Project-Id); "" is the default project.
	Project string
	// Session is the client-supplied session/conversation id (X-Session-Id). It turns
	// the o11y sessions view on for this org (emitted as session.id + gen_ai.conversation.id).
	Session string
	// Environment is the caller's logical environment label (X-Environment, e.g.
	// "staging"/"production"). Stamped onto the span's deployment.environment so
	// Observe stops defaulting to "default". "" emits nothing.
	Environment string
	// APIKeyHash is a SHA-256 hex ref of the caller credential — NEVER the plaintext key.
	APIKeyHash string
}

// empty reports whether a carries nothing worth threading.
func (a GenAIAttribution) empty() bool {
	return a.Org == "" && a.Project == "" && a.Session == "" && a.Environment == "" && a.APIKeyHash == ""
}

// WithGenAIAttribution returns ctx carrying a. When a is empty it returns ctx
// unchanged (no allocation, no value to read back).
func WithGenAIAttribution(ctx context.Context, a GenAIAttribution) context.Context {
	if ctx == nil {
		return ctx
	}
	if a.empty() {
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
