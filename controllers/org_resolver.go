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

// principalUser is who is calling, resolved from the credential the request
// presents — Authorization: Bearer, or the hanzo_iam_token cookie. nil for a request
// carrying nothing this process can verify, and nil for a provider or publishable
// key, neither of which names a person.
//
// ONE READ. This used to be two functions: a principalUser that asked for a session
// first, and this one that deliberately BYPASSED the session because a stale
// anonymous guest in the process-local store could shadow the real identity. There is
// no such store any more — a session IS the verified token — so both were reading the
// same credential and the order between them could not mean anything.
func (c *ApiController) principalUser() *iam.User {
	token := bearerToken(c.Header("Authorization"), c.Fiber().Cookies(iamTokenCookieName))
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
	// Non-JWT Bearer: an sk- IAM API key. Resolve it to its owning user via IAM so
	// GetOrg yields the caller's real tenant (routing, pricing, usage reads, record
	// attribution) instead of the IAM_ORG default. Without this, a keyed chat call
	// resolved orgId="" and relied solely on the separately-resolved authUser.Owner
	// for tenant attribution to zen — a split that 402'd whenever that fallback was
	// absent. Fail-secure: an unknown key (IAM data:null) yields nil, never a
	// spoofed org.
	if isIAMApiKey(token) {
		if user, err := getUserByAccessKey(token); err == nil && user != nil {
			return user
		}
	}
	// A run key names the org that pays for the run (run.go). It is resolved here
	// for exactly the reason the sk- branch above is: authResolveProvider settles
	// the PROVIDER and the payer, but GetOrg asks THIS function who the tenant is,
	// and a tenant it cannot name falls back to the IAM_ORG default — which is
	// empty, so the call reaches zen with no tenant and 402s "a billable tenant is
	// required". Authenticating and then failing to bill is the same split that
	// bug was, arriving by a different door.
	//
	// It resolves to a machine and never to a person: there is no user record
	// behind a run key, so nothing here can be logged in as, and Type
	// "application" is what makes the billing subject the ORG account.
	if isRunKey(token) {
		if r, ok := resolveRun(token); ok {
			return &iam.User{Owner: r.Org, Type: "application", Name: "run"}
		}
	}
	return nil
}

// principalIsOwnBrand reports whether the request principal was minted by THIS
// deployment's OWN brand IAM. A cookie session is own-brand: sessions are minted
// by this deployment's own hostname-resolved sign-in, so a lux/zoo/pars session
// only exists on a lux/zoo/pars host — never on this brand's. A Bearer principal
// is own-brand ONLY when its verified token issuer == expectedJWTIssuer(); a
// sibling white-label brand's token (trusted for SIGN-IN via trustedJWTIssuers,
// e.g. a lux.id bearer presented to api.hanzo.ai) is NOT own-brand.
//
// THE ISSUER IS THE ONLY THING THAT SAYS WHICH BRAND MINTED A CREDENTIAL, so it is
// the only thing asked. There used to be a session branch answering yes by
// construction — a cookie session could only exist on its own brand's host, so
// holding one proved the brand. That stopped being true when a session became the
// verified token itself: the same read now sees a sibling brand's bearer, and
// answering yes for it hands a lux/zoo/pars admin THIS brand's all-customer view.
// The cookie is covered either way, since the token it carries was minted here and
// says so in its issuer.
//
// Callers gate cross-tenant / all-customer disclosure on this AND util.IsSuperAdmin,
// which decouples that grant from the broader multi-brand sign-in trust set — see
// resolveCloudUsageScope.
func (c *ApiController) principalIsOwnBrand() bool {
	token := bearerToken(c.Header("Authorization"), c.Fiber().Cookies(iamTokenCookieName))
	if token == "" || !isJwtToken(token) {
		return false
	}
	return object.TokenIsOwnBrand(token)
}

// principalOrgs returns the SIGNED `orgs` membership set of the request's bearer
// JWT: the orgs IAM states this subject may act in. It is the ONLY admissible
// proof of membership — see account.EffectiveOrg.
//
// nil for every credential that carries no such claim: a cookie session, an sk-
// IAM key, a provider (sk-) or publishable (pk-) key, and any token minted before
// the claim shipped. nil names no org, so all of those stay in their home org
// exactly as they did before the claim existed.
//
// Called ONLY on the switch path (a request that asked for an org other than its
// own), so the dominant no-switch request never pays for this parse.
func (c *ApiController) principalOrgs() []account.OrgRef {
	token := bearerToken(c.Header("Authorization"), c.Fiber().Cookies(iamTokenCookieName))
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
	requested := strings.TrimSpace(c.Header("X-Org-Id"))

	user := c.principalUser()
	if user != nil && user.Owner != "" {
		if requested == "" || requested == user.Owner {
			return user.Owner
		}
		if util.IsSuperAdmin(user) {
			return requested
		}
		// SCOPE, not money: a refused selection answers with the principal's own
		// org. Falling back here NARROWS what the request can see, so it is
		// genuinely fail-closed — unlike the same fallback on the billing path,
		// which would charge a different account. account.EffectiveOrg refuses
		// now, and this is the one caller for which home is the right answer.
		org, err := account.EffectiveOrg(user.Owner, c.principalOrgs(), requested)
		if err != nil {
			return user.Owner
		}
		return org
	}

	// A PUBLISHABLE KEY ANSWERS SCOPE AND NOTHING ELSE. It names a tenant without
	// naming a person, so it belongs to this question and not to principalUser: that
	// function's answer is what RequireSuperAdmin, RequirePrincipal, the connections
	// surface and the session self-heal all act on, and a key that ships in the
	// source of a page must not reach any of them. Naming the TENANT and refusing to
	// name a PERSON is the difference between the two key halves, and it is kept by
	// answering here.
	//
	// A LIVE SESSION OUTRANKS IT. The key is a header and a session is a cookie, so
	// one browser can carry both, and the search doors read the session first — a
	// request that resolved to two different orgs depending on which door it entered
	// would be scoped by one and billed by the other.
	//
	// It is PINNED to the org that holds the key: X-Org-Id is not consulted and no
	// membership claim can widen it. And it is DEFINITIVE, including when it fails —
	// a caller presenting a pk- has named a tenant, so if IAM cannot confirm which,
	// the honest answer is none. Falling through to the deployment default would
	// scope a customer's key to the platform's own org on an IAM hiccup.
	if token := bearerToken(c.Header("Authorization"), c.Fiber().Cookies(iamTokenCookieName)); isPublishableKey(token) {
		if u := c.GetSessionUser(); u != nil && u.Owner != "" {
			return u.Owner
		}
		org, err := publishableOrg(token)
		if err != nil {
			return ""
		}
		return org
	}

	// No verified principal: never trust a client-supplied org header.
	return conf.GetConfigString("IAM_ORG")
}

// billingOrg resolves the org `user` SPENDS FROM on this request: the ledger the
// balance gate reads, the budget reservation holds, and the usage debit lands on.
// It is account.LedgerOrg over this request's own credential.
//
// It is a function OF THE BILLED USER, not of the request alone. A provider (sk-)
// or publishable (pk-) key bills the org that MINTED it, which is not the request's
// JWT principal; those credentials carry no membership claim, so principalOrgs
// returns nil for them and they always resolve to their own owner.
//
// Returns "" only for a nil user — a request with nobody to bill, which every
// caller already refuses rather than serving free.
func (c *ApiController) billingOrg(user *iam.User) string {
	if user == nil {
		return ""
	}
	// THE PUBLIC LANE NEVER SWITCHES, and this is the one place that has to say so.
	//
	// A visitor presents no credential and the lane authenticates none, but the
	// membership set below is read straight off the request — so a signed-in reader
	// asking an anonymous question could name one of THEIR orgs and move an anonymous
	// call onto a real tenant's books and a real tenant's ceiling. The reserved org
	// holds no membership and there is no wallet a visitor could be switched to, so
	// the lane's own account is the only answer.
	if user.Owner == publicOrg {
		return publicOrg
	}
	requested := strings.TrimSpace(c.Header("X-Org-Id"))
	if requested == "" || requested == user.Owner {
		// Dominant path: no switch asked for. Byte-identical to keying on
		// user.Owner, and it never touches the claim.
		return user.Owner
	}
	// A credential that carries NO membership set — sk- IAM key, sk- provider key,
	// pk- publishable key, no credential at all — cannot switch by construction. Its
	// ask is not a bid for someone else's wallet; there is no other wallet it
	// could name. Billing its own owner is the only possible answer and the one
	// it has always given, so an unrelated X-Org-Id header must not start
	// rejecting these requests.
	orgs := c.principalOrgs()
	if len(orgs) == 0 {
		return user.Owner
	}

	// MONEY, and the case that matters: this principal DOES carry a signed
	// membership set and asked for an org outside it. Refuse — never redirect the
	// bill to their personal wallet. "" is the existing "nobody to bill" answer
	// that every caller already rejects rather than serving free, so a refusal
	// cannot be spent, only refused.
	effective, err := account.EffectiveOrg(user.Owner, orgs, requested)
	if err != nil {
		return ""
	}
	return account.LedgerOrg(effective, user.Owner, util.IsSuperAdmin(user))
}
