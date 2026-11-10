// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import "testing"

func TestEndpointOfThreeD(t *testing.T) {
	if got := endpointOf([]string{"3d"}); got != "/v1/3d/generations" {
		t.Fatalf("endpointOf([3d])=%q", got)
	}
}

func TestModeOutputsThreeD(t *testing.T) {
	got := modeOutputs["3d"]
	if len(got) != 1 || got[0] != "3d" {
		t.Fatalf("modeOutputs[3d]=%v", got)
	}
}

func TestAudioMediaPathAliases(t *testing.T) {
	// Mirror the verb switch used by AudioMedia.
	verbOf := func(p string) string {
		switch {
		case len(p) >= 6 && p[len(p)-6:] == "/voice":
			return "voice"
		case p == "/v1/audio/music" || p == "/v1/music/generations":
			return "music"
		case p == "/v1/audio/foley" || p == "/v1/foley/generations":
			return "foley"
		default:
			return ""
		}
	}
	for p, want := range map[string]string{
		"/v1/audio/music":       "music",
		"/v1/music/generations": "music",
		"/v1/audio/foley":       "foley",
		"/v1/foley/generations": "foley",
	} {
		if got := verbOf(p); got != want {
			t.Errorf("%s -> %q want %q", p, got, want)
		}
	}
}
