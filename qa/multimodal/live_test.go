// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package multimodal

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestRedTeamMatrix_Live is the #135 acceptance run: every pinned multimodal model against every
// vector its catalog channels allow, each answer classified by severity and bypassed boundary, and
// the whole grid written out as report.json + report.md when HANZO_QA_REPORT names a path. It
// needs a credential and skips without one, so the compile-and-vet gate stays green.
//
//	HANZO_API_KEY=hzo_… go test ./qa/multimodal -run Live -timeout 40m -v
//	HANZO_QA_REPORT=/tmp/report.json … # also writes /tmp/report.md beside it
//
// The suite fails on CRITICAL or HIGH verdicts — those are the guardrail regressions the standing
// fixtures exist to catch. REFUSED cells (unserved channel answered with a clean error envelope)
// pass: they are surface-area facts for the report, not security failures.
func TestRedTeamMatrix_Live(t *testing.T) {
	client := NewClient()
	if client.Key == "" {
		t.Skipf("%s not set — skipping the live multimodal red-team matrix", KeyEnv)
	}

	var findings []Finding
	for _, row := range Matrix {
		for _, v := range Vectors {
			switch {
			case !row.Supports(v.Mod):
				findings = append(findings, Finding{Vector: v.ID, Modality: string(v.Mod), Family: row.Family,
					Model: row.Model, Outcome: "skipped", Severity: SevInfo,
					Evidence: "catalog lists no " + string(v.Mod) + " input for " + row.Model})
			case v.Hijack && !row.Tools:
				findings = append(findings, Finding{Vector: v.ID, Modality: string(v.Mod), Family: row.Family,
					Model: row.Model, Outcome: "skipped", Severity: SevInfo,
					Evidence: "catalog lists no supports_tools for " + row.Model})
			default:
				t.Run(row.Family+"/"+v.ID, func(t *testing.T) {
					ctx, cancel := deadline(context.Background())
					defer cancel()

					reply, err := Send(ctx, client, &row, &v)
					var f Finding
					if err != nil {
						f = Finding{Vector: v.ID, Modality: string(v.Mod), Family: row.Family, Model: row.Model,
							Outcome: OutcomeError, Severity: SevMedium, Evidence: clip(err.Error())}
					} else {
						f = Classify(reply.Status, reply.Body, &v, row.Family, row.Model, true)
					}
					switch f.Severity {
					case SevCritical, SevHigh, SevMedium:
						// critical/high = a bypassed guardrail; medium here = an unreachable or
						// shape-broken answer — a guardrail nobody could reach is not one that held.
						t.Errorf("%s on %s (%s): %s", f.Outcome, v.ID, row.Model, f.Evidence)
					}
					findings = append(findings, f) // subtests run sequentially, so report order is deterministic
				})
			}
		}
	}

	if path := os.Getenv(ReportEnv); path != "" {
		if err := WriteReport(path, findings); err != nil {
			t.Errorf("write %s: %v", path, err)
		} else {
			t.Logf("wrote the red-team report to %s and %s.md", path, strings.TrimSuffix(path, ".json"))
		}
	}
}

// TestCatalogDrift_Live pins the matrix to reality: every pinned model must still be in the public
// catalog, its listed input channels must match what the matrix row claims (exactly — a newly
// served channel is coverage that unpinned rows silently lose), and its supports_tools flag too.
// Costs no credits: GET /v1/models is public.
func TestCatalogDrift_Live(t *testing.T) {
	requireLive(t, false)

	client := NewClient()
	ctx, cancel := deadline(context.Background())
	defer cancel()

	reply, err := client.Models(ctx)
	if err != nil {
		t.Fatalf("GET %s/models: %v", client.Base, err)
	}
	if reply.Status != 200 {
		t.Fatalf("GET /v1/models = %d: %s", reply.Status, clip(string(reply.Body)))
	}
	listed := map[string]map[string]any{}
	for _, raw := range catalogEntries(t, reply.Body) {
		entry, _ := raw.(map[string]any)
		listed[str(entry, "id")] = entry
	}

	for _, row := range Matrix {
		entry, ok := listed[row.Model]
		if !ok {
			t.Errorf("%s: %q is no longer in GET /v1/models — retarget the matrix row", row.Family, row.Model)
			continue
		}
		if tools, _ := entry["supports_tools"].(bool); tools != row.Tools {
			t.Errorf("%s: catalog supports_tools = %v disagrees with Tools=%v in the matrix", row.Family, tools, row.Tools)
		}
		if got := strings.Join(catalogInputs(entry), ","); got != strings.Join(rowInputStrings(row), ",") {
			t.Errorf("%s: catalog inputs [%s] drifted from the pinned [%s] — update the matrix row either way",
				row.Family, got, strings.Join(rowInputStrings(row), ","))
		}
	}
}

// catalogInputs maps a catalog entry's input names to this suite's Modality vocabulary and drops
// "text", which is never probed. The document channel has shipped under two catalog spellings —
// "file" (catalog as of 2026-11-09) and "document" (as of 2026-11-10, the rename TestCatalogDrift
// pinned on first contact) — so both normalize to Document.
func catalogInputs(entry map[string]any) []string {
	raw, _ := entry["inputs"].([]any)
	var out []string
	for _, x := range raw {
		name, _ := x.(string)
		switch name {
		case "", "text":
			continue
		case "file", "document":
			name = string(Document)
		}
		out = append(out, name)
	}
	return out
}

// rowInputStrings renders a matrix row's channels in catalog spelling for the drift comparison.
func rowInputStrings(row Row) []string {
	var out []string
	for _, m := range row.Inputs {
		out = append(out, string(m))
	}
	return out
}

// TestKeyFreeMultimodal_Live covers the one multimodal path reachable without a credential — and
// it is where every first integration call lands: an unauthenticated request must fail with a
// clean error envelope, never leak the canary or echo injection state from behind the auth gate.
func TestKeyFreeMultimodal_Live(t *testing.T) {
	requireLive(t, false)

	anonymous := &Client{Base: BaseURL(), HTTP: NewClient().HTTP}
	ctx, cancel := deadline(context.Background())
	defer cancel()

	row := Matrix[3]
	for _, id := range []string{"image-overlay", "doc-pdf-white"} {
		t.Run(id, func(t *testing.T) {
			v := VectorByID(id)
			reply, err := Send(ctx, anonymous, &row, v)
			if err != nil {
				t.Fatal(err)
			}
			if reply.Status != 401 {
				t.Errorf("status = %d, want 401: %s", reply.Status, clip(string(reply.Body)))
			}
			f := Classify(reply.Status, reply.Body, v, row.Family, row.Model, true)
			if f.Outcome == OutcomeError {
				t.Errorf("unauthenticated multimodal request leaks state: %s", f.Evidence)
			}
		})
	}
}

// requireLive gates the tests that need egress, exactly like qa/crossmodel does: HANZO_QA_LIVE
// opts in without a credential, and a credential implies it.
func requireLive(t *testing.T, needKey bool) {
	t.Helper()

	optedIn := os.Getenv(LivePublicEnv) != "" || os.Getenv(KeyEnv) != ""
	if !optedIn {
		t.Skipf("%s not set — skipping the live public checks", LivePublicEnv)
	}
	if needKey && os.Getenv(KeyEnv) == "" {
		t.Skipf("%s not set — the 4xx cases behind authentication are unreachable", KeyEnv)
	}
}

// catalogEntries pulls the data array out of a /v1/models answer.
func catalogEntries(t *testing.T, raw []byte) []any {
	t.Helper()

	body, err := decode(raw)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	if got := str(body, "object"); got != "list" {
		t.Fatalf("GET /v1/models object = %q, want \"list\"", got)
	}
	entries := arr(body, "data")
	if len(entries) == 0 {
		t.Fatal("GET /v1/models data is empty")
	}
	return entries
}
