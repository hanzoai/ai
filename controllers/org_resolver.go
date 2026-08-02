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

package controllers

import (
	"strings"

	"github.com/hanzoai/account"
	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// credentialUser resolves the request principal STRICTLY from its verified
// bearer credential (Authorization: Bearer JWT, or the hanzo_iam_token cookie
// fallback), BYPASSING the beego session. Unlike principalUser it never consults
// GetSessionUser, so get-account can re-derive the canonical identity even when
// the process-local session already holds a stale anonymous guest that would
// otherwise shadow it. Returns nil when no valid JWT credential is present.
func (c *ApiController) credentialUser() *iam.User {
	token := bearerTokenFromRequest(c.Ctx.Request)
	if token == "" {
		return nil
	}
	if isJwtToken(token) {
		claims, err := object.ParseAndValidateJWT(token)
		if err != nil {
			return nil
		}
		return &claims.User
	}
	// Non-JWT Bearer: an hk- IAM API key. Resolve it to its owning user via IAM so
	// GetOrg yields the caller's real tenant (routing, pricing, usage reads, record
	// attribution) instead of the IAM_ORG default. Without this, an hk- chat call
	// resolved orgId="" and relied solely on the separately-resolved authUser.Owner
	// for tenant attribution to zen — a split that 402'd whenever that fallback was
	// absent. Fail-secure: an unknown key (IAM data:null) yields nil, never a
	// spoofed org.
	if isIAMApiKey(token) {
		if user, err := getUserByAccessKey(token); err == nil && user != nil {
			return user
		}
	}
	return nil
}

// principalUser resolves the request principal: the session user (cookie auth)
// if present, else the VERIFIED Bearer JWT user (see credentialUser). Returns
// nil for an unauthenticated or provider/widget-key request. This is the one
// identity source the org resolver trusts — never a raw client header.
func (c *ApiController) principalUser() *iam.User {
	if u := c.GetSessionUser(); u != nil {
		return u
	}
	return c.credentialUser()
}

// principalIsOwnBrand reports whether the request principal was minted by THIS
// deployment's OWN brand IAM. A cookie session is own-brand: sessions are minted
// by this deployment's own hostname-resolved sign-in, so a lux/zoo/pars session
// only exists on a lux/zoo/pars host — never on this brand's. A Bearer principal
// is own-brand ONLY when its verified token issuer == expectedJWTIssuer(); a
// sibling white-label brand's token (trusted for SIGN-IN via trustedJWTIssuers,
// e.g. a lux.id bearer presented to api.hanzo.ai) is NOT own-brand.
//
// It mirrors principalUser's session-first order, so the SAME credential decides
// both identity and brand. Callers gate cross-tenant / all-customer disclosure on
// this AND util.IsSuperAdmin, decoupling that grant from the broader multi-brand
// sign-in trust set — see resolveCloudUsageScope.
func (c *ApiController) principalIsOwnBrand() bool {
	if c.GetSessionUser() != nil {
		return true
	}
	token := bearerTokenFromRequest(c.Ctx.Request)
	if token == "" || !isJwtToken(token) {
		return false
	}
	return object.TokenIsOwnBrand(token)
}

// principalOrgs returns the SIGNED `orgs` membership set of the request's bearer
// JWT: the orgs IAM states this subject may act in. It is the ONLY admissible
// proof of membership — see iam.EffectiveOrg.
//
// nil for every credential that carries no such claim: a cookie session, an hk-
// IAM key, a widget (hz_) or provider (sk-) key, and any token minted before the
// claim shipped. nil names no org, so all of those stay in their home org exactly
// as they did before the claim existed.
//
// Called ONLY on the switch path (a request that asked for an org other than its
// own), so the dominant no-switch request never pays for this parse.
func (c *ApiController) principalOrgs() []account.OrgRef {
	token := bearerTokenFromRequest(c.Ctx.Request)
	if token == "" || !isJwtToken(token) {
		return nil
	}
	claims, err := object.ParseAndValidateJWT(token)
	if err != nil {
		return nil
	}
	return claims.Orgs
}

// GetOrg resolves the organization a request ACTS IN — data scoping, routing,
// pricing, usage reads, record attribution — from the VERIFIED request
// principal, never a raw client header.
//
// X-Org-Id is honored when it names the principal's own org, an org the SIGNED
// `orgs` claim says the principal belongs to (the org switcher), or any org at
// all for a super admin (cross-tenant platform access). A non-member's request
// is silently answered with its home org: a spoofed header can never widen
// scope, and the refusal discloses nothing.
//
// This is the SCOPE answer, not the MONEY answer. What a request SPENDS FROM is
// billingOrg, which differs for exactly one principal — a super admin acting
// inside a customer tenant scopes to the customer and spends its own ledger.
func (c *ApiController) GetOrg() string {
	requested := strings.TrimSpace(c.Ctx.Input.Header("X-Org-Id"))

	user := c.principalUser()
	if user != nil && user.Owner != "" {
		if requested == "" || requested == user.Owner {
			return user.Owner
		}
		if util.IsSuperAdmin(user) {
			return requested
		}
		return account.EffectiveOrg(user.Owner, c.principalOrgs(), requested)
	}

	// No verified principal: never trust a client-supplied org header.
	return conf.GetConfigString("IAM_ORG")
}

// billingOrg resolves the org `user` SPENDS FROM on this request: the ledger the
// balance gate reads, the budget reservation holds, and the usage debit lands on.
// It is iam.LedgerOrg over this request's own credential.
//
// It is a function OF THE BILLED USER, not of the request alone. A provider (sk-)
// or widget (hz_) key bills the org that MINTED it, which is not the request's
// JWT principal; those credentials carry no membership claim, so principalOrgs
// returns nil for them and they always resolve to their own owner.
//
// Returns "" only for a nil user — a request with nobody to bill, which every
// caller already refuses rather than serving free.
func (c *ApiController) billingOrg(user *iam.User) string {
	if user == nil {
		return ""
	}
	requested := strings.TrimSpace(c.Ctx.Input.Header("X-Org-Id"))
	if requested == "" || requested == user.Owner {
		// Dominant path: no switch asked for. Byte-identical to keying on
		// user.Owner, and it never touches the claim.
		return user.Owner
	}
	return account.LedgerOrg(
		account.EffectiveOrg(user.Owner, c.principalOrgs(), requested),
		user.Owner,
		util.IsSuperAdmin(user),
	)
}
