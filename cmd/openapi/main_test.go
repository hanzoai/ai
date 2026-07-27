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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/ai/routers"
)

// specPath locates the canonical spec: HANZO_OPENAPI_SPEC, else the sibling
// checkout beside this repo. Returns "" when it is not present.
//
// The sibling is OPTIONAL on purpose. hanzoai/openapi is a separate repo, so a
// clone of this one may not have it, and a test that fails on a missing sibling
// would just be turned off. The drift check skips when the spec is absent and
// runs wherever both are checked out — which is CI and every dev who has the
// spec they are about to publish.
func specPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("HANZO_OPENAPI_SPEC"); p != "" {
		return p
	}
	// cmd/openapi -> repo root -> sibling checkout.
	root, err := filepath.Abs("../..")
	if err != nil {
		return ""
	}
	p := filepath.Join(filepath.Dir(root), "openapi", "ai", "openapi.yaml")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// TestSpecMatchesTheTable is the whole point of generating: the published
// contract cannot describe a surface this service does not serve.
//
// A hand-maintained spec drifts in the direction that hurts — it is what every
// customer, SDK and docs page believes, so it goes stale silently while the
// routes move underneath it. This repo already carries the proof: swagger.json
// still documents `/api/<verb>-<noun>`, a base path nothing has ever served, and
// nothing failed when that became untrue.
func TestSpecMatchesTheTable(t *testing.T) {
	p := specPath(t)
	if p == "" {
		t.Skip("hanzoai/openapi checkout not found — set HANZO_OPENAPI_SPEC to run the drift check")
	}
	current, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	next, err := render(string(current))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if string(current) != next {
		t.Errorf("%s is OUT OF DATE with routers/resources.go.\n"+
			"The published contract would describe a different surface than the one served.\n"+
			"Regenerate:\n\n    go run ./cmd/openapi -spec %s\n", p, p)
	}
}

// TestRenderIsDeterministic guards the property the drift check depends on. Go
// randomizes map iteration, so a renderer that leaked map order would produce a
// different file on every run — the check above would then fail at random and be
// dismissed as flaky, which is how a drift guard dies.
func TestRenderIsDeterministic(t *testing.T) {
	const stub = beginMarker + "\n" + endMarker + "\n"
	first, err := render(stub)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for i := 0; i < 8; i++ {
		again, err := render(stub)
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("render is not deterministic — run %d differs; map order is leaking", i)
		}
	}
}

// TestRenderTouchesOnlyTheGeneratedRegion: the spec's hand-authored half (the
// inference paths with their real OpenAI request/response schemas, and every
// shared component) must survive regeneration byte-for-byte. A renderer that
// rewrote the whole file would silently discard work no table can reproduce.
func TestRenderTouchesOnlyTheGeneratedRegion(t *testing.T) {
	const sentinel = "KEEP-ME-EXACTLY-AS-IS"
	spec := "before: " + sentinel + "\n" + beginMarker + "\n  stale: true\n" + endMarker + "\nafter: " + sentinel + "\n"

	out, err := render(spec)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.HasPrefix(out, "before: "+sentinel+"\n") {
		t.Error("content BEFORE the generated region was modified")
	}
	if !strings.HasSuffix(out, endMarker+"\nafter: "+sentinel+"\n") {
		t.Error("content AFTER the generated region was modified")
	}
	if strings.Contains(out, "stale: true") {
		t.Error("the previous generated body survived — the region was appended to, not replaced")
	}
	if !strings.Contains(out, "/v1/ai/stores:") {
		t.Error("the generated body is missing — nothing was rendered into the region")
	}
}

// TestRenderRefusesASpecWithNoRegion — writing into a spec that never declared a
// generated region would either clobber it or silently no-op. Neither is
// acceptable, so it is an error with the two lines to add.
func TestRenderRefusesASpecWithNoRegion(t *testing.T) {
	_, err := render("openapi: 3.1.0\npaths: {}\n")
	if err == nil {
		t.Fatal("render accepted a spec with no generated region")
	}
	if !strings.Contains(err.Error(), beginMarker) {
		t.Errorf("the error should name the markers to add, got: %v", err)
	}
}

// TestOperationIDsAreUnique — a duplicate operationId makes a code generator emit
// one method name twice, and most keep only the last silently. PATCH and PUT map
// to ONE handler here, so this is a live hazard, not a hypothetical.
func TestOperationIDsAreUnique(t *testing.T) {
	if err := routers.AssertOperationIDsUnique(); err != nil {
		t.Error(err)
	}
}

// TestEveryPathIsOpenAPIShaped — the router spells a parameter `:owner`, OpenAPI
// spells it `{owner}`. A spec left in the router's spelling validates fine and
// produces SDKs that request a literal ":owner".
func TestEveryPathIsOpenAPIShaped(t *testing.T) {
	for _, p := range routers.OpenAPIPathsSorted() {
		if strings.Contains(p, ":") {
			t.Errorf("%s carries router-style parameters; OpenAPI needs {name}", p)
		}
		if !strings.HasPrefix(p, "/v1/") {
			t.Errorf("%s is not rooted at /v1", p)
		}
	}
}
