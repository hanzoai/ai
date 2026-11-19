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

package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/provenance"
)

const servedDigest = "sha256:4b227777d4dd1fc61c6f884f48641d02b4d121d3fd328cb08b5531fcacdabf8a"

// The runtime a node states is read into the record, from the head of an answer
// or from the trailer at its end, and is never relayed to the caller
// (hanzo-inc/cloud#800).
func TestTheServingRuntimeIsReadAndNeverRelayed(t *testing.T) {
	hdr := http.Header{}
	hdr.Set(provenance.RuntimeDigestHeader, "registry.internal/hanzo/engine@"+servedDigest)
	hdr.Set(provenance.CacheConnectorHeader, "LMCacheConnectorV1")
	sv := servingOf(hdr)
	if sv.runtime == "" || sv.connector != "LMCacheConnectorV1" {
		t.Fatalf("servingOf did not read the runtime: %+v", sv)
	}

	// A stream states it in its trailer, which wins over the head.
	tr := http.Header{}
	tr.Set(provenance.RuntimeDigestHeader, servedDigest)
	tr.Set(provenance.CacheConnectorHeader, "cpu-offload")
	sv.merge(tr)
	if sv.runtime != servedDigest || sv.connector != "cpu-offload" {
		t.Errorf("the trailer was not merged: runtime %q connector %q", sv.runtime, sv.connector)
	}

	cooled.forget()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(servedHeader, "z-ai/glm-5.3")
		w.Header().Set(provenance.RuntimeDigestHeader, servedDigest)
		w.Header().Set(provenance.CacheConnectorHeader, "redis://default:hunter2@kv-1.internal:6379/0")
		_, _ = w.Write([]byte(`{"id":"gen-1","model":"enso","choices":[{"index":0,"message":{"role":"assistant","content":"4"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":6}}`))
	}))
	defer srv.Close()

	fam := otherFamily(t, srv.URL)
	body := []byte(`{"model":"enso","messages":[{"role":"user","content":"2+2?"}]}`)
	c := visit(http.MethodPost, "/v1/chat/completions")
	c.Fiber().Request().SetBody(body)
	if refused := c.pipeToFamily(fam, "chat/completions", "openai", "enso", body, false, 0, "acme", nil, false, nil, time.Now()); refused != nil {
		t.Fatalf("the family refused: %+v", refused)
	}
	h := &c.Fiber().Response().Header
	for _, k := range []string{provenance.RuntimeDigestHeader, provenance.CacheConnectorHeader} {
		if got := string(h.Peek(k)); got != "" {
			t.Errorf("%s = %q reached the caller", k, got)
		}
	}
}

// Every span and every usage row names the runtime: the digest and connector a node
// stated, read through the provenance rule, or "unknown" when it stated none. A
// connector stated as a URL keeps its scheme alone, so no host or credential is
// recorded.
func TestEverySpanAndRowNamesTheRuntime(t *testing.T) {
	rowAt := func(rec *usageRecord) map[string]any {
		row := cloudUsageValues(rec, time.Now())
		at := map[string]any{}
		for i, c := range object.CloudUsageColumns {
			if i < len(row) {
				at[c] = row[i]
			}
		}
		return at
	}

	for _, c := range []struct {
		name                      string
		digest, connector         string
		wantDigest, wantConnector string
	}{
		{"stated", "repo@" + servedDigest, "redis://default:hunter2@kv-1.internal:6379/0", servedDigest, "external:redis"},
		{"unstated", "", "", provenance.Unknown, provenance.Unknown},
		{"a tag is no digest", "ghcr.io/hanzoai/engine:v1.7.108", "whatever", provenance.Unknown, provenance.Unknown},
	} {
		rec := &usageRecord{Model: "enso-pro", Provider: "enso", Status: "success",
			RuntimeDigest: c.digest, CacheConnector: c.connector}

		m := attrMap(buildGenAISpanFields(rec, 0, 0, nil, nil, nil, false).attrs)
		if got := m[provenance.AttrRuntimeDigest].AsString(); got != c.wantDigest {
			t.Errorf("%s: span %s = %q, want %q", c.name, provenance.AttrRuntimeDigest, got, c.wantDigest)
		}
		if got := m[provenance.AttrCacheConnector].AsString(); got != c.wantConnector {
			t.Errorf("%s: span %s = %q, want %q", c.name, provenance.AttrCacheConnector, got, c.wantConnector)
		}

		at := rowAt(rec)
		if got, _ := at["runtime_digest"].(string); got != c.wantDigest {
			t.Errorf("%s: row runtime_digest = %q, want %q", c.name, got, c.wantDigest)
		}
		if got, _ := at["cache_connector"].(string); got != c.wantConnector {
			t.Errorf("%s: row cache_connector = %q, want %q", c.name, got, c.wantConnector)
		}
	}

	// A served answer recorded through TraceServedUsage (a host that serves a model
	// in process) carries what it was handed.
	rec := servedRecord(ServedUsage{Model: "zen5", RuntimeDigest: servedDigest, CacheConnector: "in-memory"})
	if rec.RuntimeDigest != servedDigest || rec.CacheConnector != "in-memory" {
		t.Errorf("servedRecord dropped the runtime: %q %q", rec.RuntimeDigest, rec.CacheConnector)
	}
}
