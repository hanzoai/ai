// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

// Tests for the LibreChat-compat shape adapters. These guarantee the retired
// chat-rag-api's exact response contract so hanzo.chat's RAG client parses the
// consolidated /v1 surface unchanged (createContextHandlers.js reads
// item[0].page_content on /query; uploadVectors reads {status,known_type} on
// /embed). The adapters are pure, so they are fully unit-testable here.

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/ai/object"
)

func TestLcResultToDocumentShape(t *testing.T) {
	r := object.DocSearchResult{
		ID: "chunk-1", Content: "hello world", FileID: "file-A", Title: "a.txt", URL: "http://x/a",
	}
	doc := lcResultToDocument(r)
	if doc.PageContent != "hello world" {
		t.Fatalf("page_content = %q, want %q", doc.PageContent, "hello world")
	}
	if doc.Metadata["file_id"] != "file-A" {
		t.Fatalf("metadata.file_id = %v, want file-A", doc.Metadata["file_id"])
	}
	// Must serialize with a snake_case `page_content` key — the exact field
	// hanzo.chat reads (item[0].page_content).
	b, _ := json.Marshal(doc)
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["page_content"]; !ok {
		t.Fatalf("serialized doc missing page_content key: %s", string(b))
	}
	if _, ok := m["metadata"]; !ok {
		t.Fatalf("serialized doc missing metadata key: %s", string(b))
	}
}

func TestLcTuplesShape(t *testing.T) {
	results := []object.DocSearchResult{
		{ID: "1", Content: "first", FileID: "f"},
		{ID: "2", Content: "second", FileID: "f"},
	}
	tuples := lcTuples(results)
	if len(tuples) != 2 {
		t.Fatalf("expected 2 tuples, got %d", len(tuples))
	}
	// Round-trip through JSON and assert the client's access pattern:
	// data[i][0].page_content.
	b, _ := json.Marshal(tuples)
	var decoded [][]interface{}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("tuples not a JSON array-of-arrays: %v (%s)", err, string(b))
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded %d tuples, want 2", len(decoded))
	}
	first := decoded[0]
	if len(first) != 2 {
		t.Fatalf("tuple must be [document, score], got len %d", len(first))
	}
	docMap, ok := first[0].(map[string]interface{})
	if !ok {
		t.Fatalf("tuple[0] must be an object, got %T", first[0])
	}
	if docMap["page_content"] != "first" {
		t.Fatalf("data[0][0].page_content = %v, want %q", docMap["page_content"], "first")
	}
}

func TestLcTuplesEmpty(t *testing.T) {
	b, _ := json.Marshal(lcTuples(nil))
	if string(b) != "[]" {
		t.Fatalf("empty results should marshal to [], got %s", string(b))
	}
}

func TestIsKnownType(t *testing.T) {
	cases := map[string]bool{
		"report.pdf":  true,
		"data.csv":    true,
		"notes.md":    true,
		"sheet.xlsx":  true,
		"deck.pptx":   true,
		"doc.docx":    true,
		"code.go":     true, // parsed as plain text
		"noextension": false,
	}
	for name, want := range cases {
		if got := isKnownType(name); got != want {
			t.Fatalf("isKnownType(%q) = %v, want %v", name, got, want)
		}
	}
}
