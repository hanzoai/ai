// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package object

import "testing"

// TestWidgetKeyOwner_Resolution pins the widget-key → owner-org resolution that
// both tenant isolation (RAG) and per-org billing depend on. The billing-critical
// invariant is the fail-secure empty return: an unattributable widget key resolves
// to "" so callers refuse it rather than spend the shared upstream for free.
func TestWidgetKeyOwner_Resolution(t *testing.T) {
	t.Run("explicit key map (JSON)", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_OWNERS", `{"hz_a":"tenant-a","hz_b":"tenant-b"}`)
		t.Setenv("WIDGET_DEFAULT_OWNER", "")
		if got := WidgetKeyOwner("hz_a"); got != "tenant-a" {
			t.Fatalf("hz_a => %q, want tenant-a", got)
		}
		if got := WidgetKeyOwner("hz_b"); got != "tenant-b" {
			t.Fatalf("hz_b => %q, want tenant-b", got)
		}
	})

	t.Run("explicit key map (comma list)", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_OWNERS", "hz_a=tenant-a,hz_b=tenant-b")
		t.Setenv("WIDGET_DEFAULT_OWNER", "")
		if got := WidgetKeyOwner("hz_a"); got != "tenant-a" {
			t.Fatalf("hz_a (list) => %q, want tenant-a", got)
		}
	})

	t.Run("falls back to default owner", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_OWNERS", `{"hz_a":"tenant-a"}`)
		t.Setenv("WIDGET_DEFAULT_OWNER", "house")
		if got := WidgetKeyOwner("hz_unmapped"); got != "house" {
			t.Fatalf("unmapped key => %q, want house (WIDGET_DEFAULT_OWNER)", got)
		}
	})

	t.Run("unattributable resolves empty (fail-secure)", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_OWNERS", `{"hz_a":"tenant-a"}`)
		t.Setenv("WIDGET_DEFAULT_OWNER", "")
		if got := WidgetKeyOwner("hz_unmapped"); got != "" {
			t.Fatalf("unmapped key with no default => %q, want empty (caller must refuse)", got)
		}
	})
}

// TestWidgetKeyModels_Resolution pins the per-key model allowlist that binds a
// public widget key to exactly the models its owner provisioned (the docs key to
// `enso` only). The security invariant is that a bound key resolves a NON-EMPTY set
// (so the caller enforces it), while an unbound key resolves nil (so the caller
// falls back to the global allowlist) — never the reverse.
func TestWidgetKeyModels_Resolution(t *testing.T) {
	t.Run("single model, JSON map (docs → enso)", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_MODELS", `{"hz_docs":"enso"}`)
		got := WidgetKeyModels("hz_docs")
		if len(got) != 1 || got[0] != "enso" {
			t.Fatalf("hz_docs => %v, want [enso]", got)
		}
	})

	t.Run("lowercases + splits on space and comma", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_MODELS", `{"hz_docs":"Enso enso-pro,ENSO-ULTRA"}`)
		got := WidgetKeyModels("hz_docs")
		want := []string{"enso", "enso-pro", "enso-ultra"}
		if len(got) != len(want) {
			t.Fatalf("=> %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("=> %v, want %v", got, want)
			}
		}
	})

	t.Run("comma-list form (one model per key)", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_MODELS", "hz_docs=enso,hz_help=claude-haiku-4-5")
		if got := WidgetKeyModels("hz_docs"); len(got) != 1 || got[0] != "enso" {
			t.Fatalf("hz_docs => %v, want [enso]", got)
		}
		if got := WidgetKeyModels("hz_help"); len(got) != 1 || got[0] != "claude-haiku-4-5" {
			t.Fatalf("hz_help => %v, want [claude-haiku-4-5]", got)
		}
	})

	t.Run("unbound key resolves nil (fall back to global allowlist)", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_MODELS", `{"hz_docs":"enso"}`)
		if got := WidgetKeyModels("hz_other"); got != nil {
			t.Fatalf("unbound key => %v, want nil (caller falls back to global allowlist)", got)
		}
	})

	t.Run("no config resolves nil", func(t *testing.T) {
		t.Setenv("WIDGET_KEY_MODELS", "")
		if got := WidgetKeyModels("hz_docs"); got != nil {
			t.Fatalf("no config => %v, want nil", got)
		}
	})
}
