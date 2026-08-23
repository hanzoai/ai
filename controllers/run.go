package controllers

// run.go admits ONE more kind of bearer at the inference edge: a token that
// stands for a RUN rather than for a person.
//
// # Why a fourth spelling exists
//
// An autonomous coding run executes model output inside a sandbox. Whatever
// credential that box holds is, for the length of the run, in the hands of the
// model — so it must not be an identity. Every other bearer this edge accepts is
// one: a hanzo.id JWT names a user, an sk- key resolves through IAM to a user,
// and both open every other org-scoped surface the platform has. Handing either
// to a sandbox is handing it /v1/kms/secrets.
//
// A run key names an ACT: this run may buy inference, and the org that started it
// pays. It authenticates nobody. There is no user record behind it, nothing to log
// in as, and no second surface it opens.
//
// # What it resolves to
//
// A machine bearer, prefix-dispatched, that resolves to an identity owned by the
// org that started the run — so the balance gate, the budget reservation and the
// usage debit all engage exactly as for a person. It is minted per run and dies
// with it.
//
// Nothing caps it beyond that balance. A run was started by an authenticated member
// of the org that pays for it, so it buys what that org can afford; the public lane,
// which serves a stranger nobody can bill, is the one that carries a token ceiling.
//
// # What is NOT here
//
// No table, no expiry, no minting. Those belong to whoever owns the run's
// lifetime, which is not this module — and a copy of them here would be a second
// place where a run could be granted the right to spend. This file states the
// QUESTION ("is this token a live run, and whose?") and the process that knows
// answers it. Until something installs an answer there are no run keys, which is
// the correct behaviour for a deployment that has no run orchestrator.

import (
	"strings"
	"sync/atomic"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// runPrefix marks a run key, in the one place a human or a secret scanner sees
// it. It is deliberately NOT sk-/pk-: those are IAM's spelling, and a token that
// could be mistaken for one would be sent to IAM, which is the widening this
// whole design exists to prevent.
const runPrefix = "hrun_"

// isRunKey reports whether a token is a run key by its spelling alone. Cheap, and
// total: nothing is looked up to decide which BRANCH a token takes, so an
// unresolvable run key fails as a run key rather than falling through to IAM and
// being reported as a bad API key.
func isRunKey(token string) bool { return strings.HasPrefix(token, runPrefix) }

// Run is what a live run key resolves to: the org that pays, and the run it was
// minted for. Empty Org is not a run — there is no third answer.
type Run struct {
	Org string
	// ID names the run for attribution. It reaches the usage record so one run's
	// spend is a SUM the org can read back, and never a guess.
	ID string
}

// runs is the installed answer to "is this token a live run". Held atomically
// because it is written once at wire-up and read on every inference call.
var runs atomic.Pointer[func(token string) (Run, bool)]

// SetRunResolver installs the lookup that resolves a run key. Called once at
// startup by the process that owns run lifetimes; passing nil removes it, and
// then no run key resolves.
//
// This is the same seam shape model.SetContextWindowResolver uses, for the same
// reason: this package states WHAT it needs to know without importing where the
// answer lives, so the orchestrator depends on the inference package and never
// the reverse.
func SetRunResolver(f func(token string) (Run, bool)) {
	if f == nil {
		runs.Store(nil)
		return
	}
	runs.Store(&f)
}

// resolveRun answers whether a token is a live run and whose. Fails CLOSED: no
// installed resolver, an unknown token, or an answer naming no org are all "not a
// run", which costs a 401 and never a free call on somebody else's ledger.
func resolveRun(token string) (Run, bool) {
	f := runs.Load()
	if f == nil {
		return Run{}, false
	}
	r, ok := (*f)(token)
	if !ok || strings.TrimSpace(r.Org) == "" {
		return Run{}, false
	}
	return r, true
}

// resolveProviderFromRunKey resolves a run key to the paying org's provider and a
// machine principal that bills that org.
//
// THE ORG COMES FROM THE TOKEN AND NEVER FROM THE REQUEST. There is no X-Org-Id
// here and there must never be: the run key is minted for one org, and a caller
// able to name the org in a header could spend another tenant's balance with a
// key it legitimately holds. This is the same rule the IAM-key path states ("it
// can never switch org: it bills the org that owns the key").
//
// The ledger is the org itself. A run has no home/effective split to make — that
// distinction exists for a SuperAdmin acting inside another tenant, and a run is
// not a person and never acts for one.
func resolveProviderFromRunKey(token, requestedModel, lang string) (*object.Provider, *iam.User, string, error) {
	r, ok := resolveRun(token)
	if !ok {
		return nil, nil, "", authError("invalid or expired run key")
	}
	// Type "application" marks this a machine, which is what makes account.Payer
	// resolve the billing subject to the ORG account rather than to a person who
	// does not exist.
	user := &iam.User{Owner: r.Org, Type: "application", Name: "run"}
	return resolveProviderForUser(user, r.Org, requestedModel, lang)
}
