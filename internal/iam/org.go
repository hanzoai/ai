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

package iam

// The org a request acts in and the org it spends from are TWO answers, not one.
// They coincide for every ordinary caller and diverge for exactly one: a super
// admin operating inside a customer's tenant. Keeping them as two functions is
// what stops that divergence from being re-derived (differently) at each of the
// dozen sites that need one or the other.
//
// Both are pure: same inputs, same answer, no clock, no config, no I/O. They live
// here, beside OrgRef and Claims, because this is the one package both the
// controllers and the router filters already import — the alternative is a copy
// per package, and a money predicate with two implementations has two answers.

// EffectiveOrg reports the org a credential ACTS IN: its home org, unless the
// caller asked for another AND the signed `orgs` claim names that one.
//
// orgs is the membership set IAM signed into the token — the ONLY admissible
// proof. A raw X-Org-Id is a claim by the client about itself; honoring one that
// the signature does not back is a cross-tenant read. A credential carrying no
// such claim (a cookie session, an hk-/sk-/hz- key, a token minted before the
// claim shipped) passes nil here, nil names no org, and the caller stays home.
//
// The comparison is byte-exact — no trim, no case fold. "acme" and "ACME" are
// DISTINCT orgs, and folding them here would let a request address a ledger the
// membership row does not name. This matches cloud's isMember byte for byte, so
// the edge and the ledger cannot disagree about who is a member.
//
// Refusal is SILENT and returns exactly what "no switch requested" returns: a
// non-member learns nothing about which orgs exist or which it failed to enter.
func EffectiveOrg(home string, orgs []OrgRef, requested string) string {
	if requested == "" || requested == home {
		return home
	}
	for _, o := range orgs {
		if o.Org == requested {
			return requested
		}
	}
	return home
}

// LedgerOrg reports the org a credential SPENDS FROM — the wallet the balance
// gate reads, the budget reservation holds, and the usage debit lands on.
//
// TWO branches, deliberately not one expression:
//
//   - masquerade: a super admin may ACT in any tenant (platform ops, support),
//     but it spends its OWN home ledger. An admin working on a customer's behalf
//     must never drain the customer's wallet — the customer did not authorize
//     the spend and cannot see who made it.
//   - everyone else: the org they are acting in, which EffectiveOrg already
//     proved they belong to. This is the whole point of the switcher — the org
//     selected is the org that pays.
//
// masquerade is passed in rather than derived because the super-admin predicate
// (membership in the reserved admin org) lives in util, which imports this
// package; taking the answer as an argument keeps this a pure function of values.
func LedgerOrg(effective, home string, masquerade bool) string {
	if masquerade {
		return home
	}
	return effective
}
