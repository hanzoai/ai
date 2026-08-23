// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"errors"
	"strings"
	"testing"
)

// Every provider constructor reports an unknown Type the same way — no error and
// no provider — so a nil arriving without one means we do not speak that type.
// Carrying it off instead is a nil dereference at whichever call site reached for
// it first.
func TestAProviderTypeWeDoNotSpeak(t *testing.T) {
	type maker interface{ Name() string }

	// The unknown type: nothing made, nothing wrong.
	got, err := supported[maker](nil, nil, "object:the scan provider type: %s is not supported", "acme-scanner", "en")
	if err == nil {
		t.Fatal("an unsupported type answered no error")
	}
	if got != nil {
		t.Errorf("it also answered a provider: %v", got)
	}
	if !strings.Contains(err.Error(), "acme-scanner") {
		t.Errorf("the error does not name the type: %v", err)
	}

	// A real failure is that failure, not this one.
	boom := errors.New("the endpoint refused the connection")
	if _, err := supported[maker](nil, boom, "object:the scan provider type: %s is not supported", "openai", "en"); !errors.Is(err, boom) {
		t.Errorf("a constructor's own error became %v", err)
	}

	// And something made is handed back.
	made := stubProvider{}
	back, err := supported[maker](made, nil, "object:the scan provider type: %s is not supported", "openai", "en")
	if err != nil {
		t.Fatalf("a provider that was made answered %v", err)
	}
	if back != maker(made) {
		t.Errorf("it came back as %v", back)
	}
}

type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
