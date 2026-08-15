package object

import (
	"errors"
	"testing"
)

// A provider credential is money, and where it is READ FROM decides who can take
// it. The process environment is readable by anything that can reach the
// process, sits outside the per-secret policy KMS already keeps, and records
// nothing about who read it. These pin that a family provider resolves through
// the store FIRST — so sealing a key in KMS is on its own enough to take that
// value out of the environment — and that a deployment which has not sealed
// anything yet keeps working.

func TestFamilyCredentialComesFromTheStoreFirst(t *testing.T) {
	st := &fakeStore{vals: map[string]string{"OPENROUTER_API_KEY": "sk-from-kms"}}
	bind(t, st)
	t.Setenv("OPENROUTER_API_KEY", "sk-from-env")

	if got := resolveSecretName("OPENROUTER_API_KEY"); got != "sk-from-kms" {
		t.Fatalf("credential resolved to %q, want the store's value", got)
	}
	if st.gets == 0 {
		t.Fatal("the store was never asked — the environment was read directly")
	}
}

// Absence in the store is "no value here", not an error, so an unsealed
// deployment still serves. This is what makes sealing key-by-key safe.
func TestEnvStillAnswersWhenTheStoreHasNothing(t *testing.T) {
	bind(t, &fakeStore{vals: map[string]string{}})
	t.Setenv("FIREWORKS_API_KEY", "sk-from-env")
	if got := resolveSecretName("FIREWORKS_API_KEY"); got != "sk-from-env" {
		t.Fatalf("got %q, want the environment's value", got)
	}
}

// A store OUTAGE must not take down a provider whose key is still env-fed —
// otherwise sealing one key makes every other provider fail on a blip.
func TestAStoreOutageDoesNotBreakAnEnvFedProvider(t *testing.T) {
	bind(t, &fakeStore{err: errors.New("store down")})
	t.Setenv("ANTHROPIC_API_KEY", "sk-from-env")
	if got := resolveSecretName("ANTHROPIC_API_KEY"); got != "sk-from-env" {
		t.Fatalf("got %q, want the environment's value during a store outage", got)
	}
}

// No store bound at all — the standalone and dev posture — changes nothing.
func TestNoStoreIsTheEnvironment(t *testing.T) {
	bind(t, nil)
	t.Setenv("DO_AI_API_KEY", "sk-from-env")
	if got := resolveSecretName("DO_AI_API_KEY"); got != "sk-from-env" {
		t.Fatalf("got %q, want the environment's value", got)
	}
}
