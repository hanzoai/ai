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
