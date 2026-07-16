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

package object

import "strings"

// An Account is the ONE thing that holds a balance and gets charged. It belongs
// to exactly one owner — a Person, an Org, or a Project — and (Org, Subject) IS
// the money's address: Org names the ledger that holds it, Subject the key within
// that ledger. A deposit credits that address, the gate reads it, a usage debit
// spends it. One address, one answer, every caller.
//
// WHO PAYS IS A PROPERTY OF THE CREDENTIAL — Payer is the only function that
// answers it, and it reads nothing but the credential. There is no environment
// variable, no allowlist, and no precedence rule in this file. The two global
// lists this replaces (PERSONAL_BILLING_ORGS / ORG_BILLING_ORGS) encoded one
// boolean per org in two places with a precedence between them, and one lied when
// unset — so the grant and the gate could consult "the same" rule and disagree.
// They did: the grant landed on the org account while the gate read the person's,
// and a funded org 402'd its own members.

// Kind names what owns an Account. The constructor that made an Account fixes its
// kind; the constants stay unexported because no caller branches on kind today —
// they read Subject and Org, which is the whole contract with the ledger.
type Kind string

const (
	person  Kind = "person"
	org     Kind = "org"
	project Kind = "project"
)

// SignupOrg is the org every self-serve signup lands in — IAM's
// DefaultOrganization (iam/object/session.go: DefaultOrganization = "hanzo"). It
// is NOT a tenant: it is the platform's own org, and it is the home of everyone
// who has not been placed in a real one, because IAM's social/OAuth signup path
// assigns Owner = application.Organization with no tenant logic at all
// (iam/controllers/auth.go). Its members are strangers to each other, so each
// pays from their OWN account — a shared org is not a shared wallet.
//
// This is a constant of the product, not a deployment knob: the same value is
// hardcoded in IAM, and nothing in the fleet configures it (verified across every
// Deployment and StatefulSet — the vars that used to override it were unset in
// production, which is exactly how the lying default fired).
//
// It is the last fact about billing that does not come from the credential, and it
// now survives for ONE reason: tokens minted before IAM shipped the
// `billing_account` claim name no payer, and Payer's fallback still has to answer
// for them. IAM states this same rule authoritatively for every token minted since
// (iam/object/billing_account.go), so this constant is a bridge, not a floor — when
// the last pre-claim token has expired, it deletes with the fallback. See Payer.
const SignupOrg = "hanzo"

// Account is one account money is recorded against. Its fields are unexported so
// every Account is valid by construction — this is money; an Account whose owner
// disagrees with its key is not a value we allow to exist.
type Account struct {
	kind Kind
	org  string
	name string
}

// Org returns the account owned by an org: one pooled balance for the whole
// tenant, keyed by the org slug. This is the account an admin grant credits.
func Org(slug string) Account {
	slug = fold(slug)
	if slug == "" {
		return Account{}
	}
	return Account{kind: org, org: slug}
}

// Person returns the account owned by one person within an org — a balance no
// other member of that org can spend.
func Person(slug, name string) Account {
	slug, name = fold(slug), fold(name)
	if slug == "" || name == "" {
		return Account{}
	}
	return Account{kind: person, org: slug, name: name}
}

// Project returns the account owned by a project within an org.
//
// A project needs no org of its own to be billed: the project IS the owner, and
// the org is only the ledger its money lives in. Nothing in Payer special-cases
// it — it is one more owner kind, exactly like a Person.
//
// KNOWN LIMIT, stated rather than papered over: a Project and a Person are
// distinct VALUES here but share one ledger key space ("<org>/<name>"), so a
// project and a person with the same name in the same org would address the SAME
// wallet. Nothing constructs a Project account yet (no credential can name one —
// see Payer), so nothing collides today; giving projects their own key prefix is
// a ledger change and must land with the credential that names them, not before.
func Project(slug, name string) Account {
	slug, name = fold(slug), fold(name)
	if slug == "" || name == "" {
		return Account{}
	}
	return Account{kind: project, org: slug, name: name}
}

// Kind reports what owns this account.
func (a Account) Kind() Kind { return a.kind }

// Org reports the ledger holding this account — the X-Org-Id namespace, and the
// per-org file the balance lives in.
func (a Account) Org() string { return a.org }

// Subject is the account's key within its ledger: what a deposit credits as
// DestinationId, the gate reads as ?user=, and a usage debit spends as SourceId.
// An org account is the bare slug; a person's or project's is "<org>/<name>".
//
// Always folded, because the read paths lowercase and the write paths store
// verbatim: an un-lowercased subject would record usage that never nets against
// the balance — a silent leak.
func (a Account) Subject() string {
	if a.org == "" {
		return ""
	}
	if a.name == "" {
		return a.org
	}
	return a.org + "/" + a.name
}

// Zero reports an unresolved account — no owner could be named. A caller must not
// bill it: a request that cannot be attributed is refused, never billed free.
func (a Account) Zero() bool { return a.org == "" }

// String renders an Account as the `billing_account` claim IAM signs into every
// token: `<kind>:<subject>` — "org:acme", "person:hanzo/alice",
// "project:acme/website". A zero Account renders "" (unattributable names nobody).
//
// This is one half of a WIRE CONTRACT with iam/object/billing_account.go, which
// builds the same string from the grant context. The two repos share no code, so
// the grammar is the only thing holding them together: kind is one of person|org|
// project, subject is the Account's own Subject(). ParseAccount is the inverse —
// String ∘ ParseAccount is the identity on every account IAM can mint.
func (a Account) String() string {
	if a.Zero() {
		return ""
	}
	return string(a.kind) + ":" + a.Subject()
}

// ParseAccount reads a `billing_account` claim back into the Account it names. It
// is a PARSE, not a decision: it never invents an owner, and anything it cannot
// read — an empty claim, an unknown kind, a missing subject, a person or project
// with no name — returns the zero Account, which Payer treats as "the credential
// named nobody" and falls back rather than billing a guess.
//
// Every component funnels through the same constructors the rest of this file
// uses (Org/Person/Project), so a parsed Account is valid and folded by
// construction — it can never address a wallet a constructed one could not.
func ParseAccount(claim string) Account {
	kind, subject, ok := strings.Cut(strings.TrimSpace(claim), ":")
	if !ok {
		return Account{}
	}
	slug, name, hasName := strings.Cut(subject, "/")
	switch Kind(fold(kind)) {
	case org:
		if hasName {
			return Account{} // an org account is the bare slug — a subject with a name is not one
		}
		return Org(slug)
	case person:
		if !hasName {
			return Account{}
		}
		return Person(slug, name)
	case project:
		if !hasName {
			return Account{}
		}
		return Project(slug, name)
	}
	return Account{} // an unknown kind names nothing we are willing to bill
}

// Credential is what a request presents, reduced to the only facts about it that
// are both VALIDATED and relevant to money. It is the ONLY input to Payer.
//
// Owner and Name are the IAM `owner` and `name` claims — minted by the gateway
// from a verified JWT and stripped from client input, so they are trustworthy.
//
// Account is the IAM `billing_account` claim: the credential NAMING its own payer,
// signed at the identity boundary (iam/object/billing_account.go). It is the whole
// answer when present — Payer parses it and stops guessing. It reaches us two ways,
// both server-minted and neither forgeable: the gateway validates the claim, strips
// any client-supplied copy, and mints X-Billing-Account-Id from it
// (gateway/iamauth), and cloud's own identity boundary mints the same header from
// the same claim for the in-cluster path.
//
// Machine is the LEGACY fallback signal, and it is not trustworthy: callers derive
// it from User.Type == "application", a field IAM's UpdateUser carries in its
// NON-admin column list — IAM's own code says "User.Type=='application' ALONE is
// forgeable" (iam/object/client_credentials.go), which is why IAM's auth requires
// four correlated fields (IsClientCredentialsClaim). A signup-org member who set
// their own Type could be billed as a machine and reach the org pool. That is
// exactly what the Account claim removes: IAM now resolves machine-ness from the
// client_credentials GRANT SHAPE and states the answer here, so Machine only ever
// decides a token minted BEFORE the claim shipped.
type Credential struct {
	Owner   string
	Name    string
	Account string
	Machine bool
}

// IsMachine reports whether an IAM User.Type names a service credential rather than
// a person. It is the ONE place that predicate lives, so the day it becomes
// trustworthy it changes here and nowhere else.
//
// It is NOT trustworthy today — see Credential.Machine. IAM's own auth refuses to
// trust this field alone and requires four correlated fields
// (object.IsClientCredentialsClaim: type=="application" AND name==app.Name AND
// provider=="" AND signinMethod==""), because "type" rides IAM's non-admin
// UpdateUser column list and a user can set their own. Billing should resolve
// this at the auth boundary, where those four fields exist, and pass the answer
// in; this function is the seam that makes that a one-line change.
func IsMachine(userType string) bool {
	return strings.EqualFold(strings.TrimSpace(userType), "application")
}

// Payer returns the Account that pays for a credential. It is the ONE function
// that answers "who pays": the grant, the gate, the usage debit and the console
// read all call it, so they cannot drift. It reads no configuration.
//
// The rule:
//
//	the account the credential NAMES → that account   (IAM said so, over its signature)
//	nothing named (a pre-claim token) → the legacy fallback below
//
// THE CREDENTIAL NAMES ITS PAYER. IAM mints `billing_account` at the identity
// boundary from the REAL grant context and signs it (iam/object/billing_account.go);
// the gateway validates it, strips any client copy, and mints X-Billing-Account-Id
// from it. So the pooled/personal distinction — real, and money depends on it — is
// now IN the credential, and this function reads it instead of inferring it. That
// is the whole point: an inference can be wrong, and this one was. It ran on
// User.Type=="application", which IAM's UpdateUser lets a user set, so a member of
// the shared signup org could name themselves a machine and spend the org pool.
// A signed claim cannot be forged by the caller it describes.
//
// THE FALLBACK IS FOR OLD TOKENS, NOT FOR DOUBT. A token minted before the claim
// shipped carries no account, and refusing it would 402 every live session. So an
// unnamed credential resolves the old way — and the old way is exactly what the
// claim now states for a new one, so the two agree for every principal that is not
// forging. When the last pre-claim token has expired, the fallback (and SignupOrg
// with it) deletes, and the rule is one line: the account the credential names.
//
// The claim is only honored WITHIN the caller's own org — a claim naming another
// tenant's ledger is discarded, not billed. IAM never mints one (the claim and
// `owner` come from the same signed token), so this can only fire on a mis-wired
// caller pairing a foreign claim with a local owner; it costs one comparison to
// make that a fallback instead of a cross-tenant debit.
func Payer(c Credential) Account {
	owner := fold(c.Owner)
	if owner == "" {
		return Account{} // unattributable — the caller refuses; it never bills free
	}
	if named := ParseAccount(c.Account); !named.Zero() && named.Org() == owner {
		return named
	}
	// Legacy fallback: a pre-claim token named nobody. Resolve who pays from the
	// credential's shape, exactly as before the claim existed.
	if c.Machine {
		return Org(owner)
	}
	if owner == SignupOrg {
		if name := fold(c.Name); name != "" {
			return Person(owner, name)
		}
	}
	return Org(owner)
}

// PayerOf is Payer for callers holding the identity as an "<org>/<name>" key
// rather than two fields — usage records, ZAP params, searchAuth.UserID. If org
// is empty it is taken from the key's prefix. It is a parse, not a second rule:
// it funnels into Payer, so it can never answer differently.
func PayerOf(org, key string) Account {
	name := ""
	if i := strings.IndexByte(key, '/'); i >= 0 {
		if strings.TrimSpace(org) == "" {
			org = key[:i]
		}
		name = key[i+1:]
	} else if strings.TrimSpace(org) == "" {
		org = key
	}
	return Payer(Credential{Owner: org, Name: name})
}

// fold normalizes an identity component to its canonical, comparable form.
func fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
