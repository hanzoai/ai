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
// It is also the LAST fact about billing that does not come from the credential,
// and it exists only because IAM cannot yet say who pays. See Payer.
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

// Credential is what a request presents, reduced to the only facts about it that
// are both VALIDATED and relevant to money. It is the ONLY input to Payer.
//
// Owner and Name are the IAM `owner` and `name` claims — minted by the gateway
// from a verified JWT and stripped from client input, so they are trustworthy.
// Machine says the credential acts FOR its org rather than as a person (a
// client_credentials/application token, e.g. the hanzo-cloud service token).
//
// MACHINE IS NOT YET TRUSTWORTHY, and callers must know it. Every caller today
// derives it from User.Type == "application", and that field is user-writable:
// IAM's UpdateUser carries "type" in its NON-admin column list
// (iam/object/user.go), and IAM's own code says so —
// "User.Type=='application' ALONE is forgeable (AddUser/UpdateUser persist
// arbitrary Type)" (iam/object/client_credentials.go) — which is why IAM's auth
// refuses to trust it alone and requires four correlated fields
// (IsClientCredentialsClaim). Billing still trusts the one field, so a member of
// the signup org who sets their own Type can be billed as a machine and reach the
// org pool. This type does not widen that hole; it names it. The fix is to
// resolve Machine with IAM's four-field predicate at the auth boundary and hand
// the answer here — a credential fact, decided once, where the claims are.
type Credential struct {
	Owner   string
	Name    string
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
//	a machine credential       → its org's account   (it IS the org)
//	a person in the signup org → their OWN account    (strangers, not a team)
//	anyone else                → their org's account  (a real tenant pools)
//
// WHY THE SIGNUP ORG IS NAMED HERE, AND WHAT DELETES IT. Ideally this function
// reads the payer straight off the credential and names no org at all. It cannot,
// and the blocker is exact and small: IAM does not put the payer in the token.
// Every credential it mints says owner=<org slug>, name=<who> — for humans and
// machines alike — and nothing about which account those two should charge. So
// the pooled/personal distinction, which is real and which money depends on, is
// not in the credential to read.
//
// Either of these finishes the job, reduces the rule to its first two lines, and
// deletes SignupOrg:
//
//  1. IAM mints the account. The claim already exists end-to-end EXCEPT at the
//     source: gateway/iamauth declares `billing_account`, validates it, strips any
//     client copy, and mints X-Billing-Account-Id from it — and IAM never sets it,
//     so it is always empty. One producer closes this and Payer becomes: the
//     account the credential names, else its org.
//  2. Signup stops sharing an org. If each person owned their org, "a person's
//     account" and "their org's account" would be the same account, and the
//     distinction would evaporate.
//
// Until then, naming the signup org in ONE constant, in ONE function, with no env
// and no precedence, is the honest floor. What this replaces was never this fact
// — it was this fact written twice, mutably, in two lists that disagreed.
func Payer(c Credential) Account {
	owner := fold(c.Owner)
	if owner == "" {
		return Account{} // unattributable — the caller refuses; it never bills free
	}
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
