// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package model

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// answerWithoutPanic runs one QueryText and turns a panic into a test failure,
// so a regression reads as what it is rather than aborting the package binary.
func answerWithoutPanic(t *testing.T, p ModelProvider) (result *ModelResult, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("QueryText panicked: %v", r)
		}
	}()
	return p.QueryText("hello", httptest.NewRecorder(), nil, "", nil, nil, "en")
}

// Every upstream that Open serves through a LocalModelProvider reaches it with a
// client. Zen, Enso and Hanzo were built as providers of their own type, which
// no branch named, so the first call dereferenced a nil client.
func TestEveryLocalUpstreamHasAClient(t *testing.T) {
	for _, u := range []Upstream{Ollama, Local, DigitalOcean, Zen, Enso, Hanzo} {
		made, err := Open(Spec{Upstream: u, Model: "m", Secret: "k", URL: "http://127.0.0.1:9/v1"})
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		local, ok := made.(*LocalModelProvider)
		if !ok {
			t.Fatalf("%s: Open made a %T, not a LocalModelProvider; this list is stale", u, made)
		}
		client, flushThink, flushStandard := local.dialect()
		if client == nil {
			t.Errorf("%s (type %q) has no client", u, local.typ)
		}
		if (flushThink == nil) == (flushStandard == nil) {
			t.Errorf("%s (type %q) must have exactly one flush", u, local.typ)
		}
	}
}

// Each type has one client and one flush, and the flush a streaming loop uses is
// the one the type holds. The loops used to re-test the type by name and assert
// a shape: a Local or DigitalOcean completion model held the think flush and was
// asserted to the standard one, and panicked.
func TestEachLocalTypeFlushesWithTheShapeItHolds(t *testing.T) {
	for typ, thinks := range map[string]bool{
		"Local": true, "DigitalOcean": true, "Custom-think": true,
		"Zen": true, "Enso": true, "Hanzo": true,
		"Custom": false, "GitHub": false, "Azure": false,
	} {
		p := &LocalModelProvider{typ: typ, providerUrl: "http://127.0.0.1:9/v1", apiVersion: "2024-02-01"}
		client, flushThink, flushStandard := p.dialect()
		if client == nil {
			t.Errorf("%s has no client", typ)
		}
		if got := flushThink != nil; got != thinks {
			t.Errorf("%s streams reasoning = %v, want %v", typ, got, thinks)
		}
		if (flushThink == nil) == (flushStandard == nil) {
			t.Errorf("%s must have exactly one flush", typ)
		}
	}
}

// A type with no client is refused before the call, never dereferenced.
func TestAnUnknownLocalTypeIsRefused(t *testing.T) {
	for _, typ := range []string{"", "Nobody"} {
		p := &LocalModelProvider{typ: typ, subType: "gpt-4o"}
		result, err := answerWithoutPanic(t, p)
		if err == nil {
			t.Errorf("type %q answered %+v with no error", typ, result)
		}
	}
}

// DeepSeek knows a type for chat and reasoner only. Any other model used to reach
// the local provider with no type and panic; it is refused instead, since its
// answer could not be priced either.
func TestDeepSeekRefusesAModelItCannotServe(t *testing.T) {
	p := &DeepSeekProvider{subType: "deepseek-v4", apiKey: "k"}
	result, err := answerWithoutPanic(t, p)
	if err == nil {
		t.Fatalf("deepseek-v4 answered %+v with no error", result)
	}
	if want := fmt.Sprintf("unknown model type: %s", "deepseek-v4"); !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to name the model (%q)", err, want)
	}
}
