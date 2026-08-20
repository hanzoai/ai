// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package object

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"
)

// Saving a provider carries the upstream key in the request body on its way to
// being sealed. A record of that request keeps a copy, and a copy in a row is a
// copy every reader of the table can have — so the body is redacted before it is
// stored rather than on the way out.
func TestARecordedBodyKeepsNoCredential(t *testing.T) {
	for _, body := range []string{
		`{"name":"openrouter","clientSecret":"sk-or-v1-REALKEY"}`,
		`{"api_key":"sk-proj-REALKEY"}`,
		`{"token":"hf_REALKEY"}`,
		`{"password":"correct-horse"}`,
	} {
		got := RedactBody(body)
		for _, secret := range []string{"sk-or-v1-REALKEY", "sk-proj-REALKEY", "hf_REALKEY", "correct-horse"} {
			if strings.Contains(got, secret) {
				t.Fatalf("a credential survived into the record: %s", got)
			}
		}
		if !strings.Contains(got, "redacted") {
			t.Fatalf("nothing was redacted in %q -> %q", body, got)
		}
	}
}

// The paired control: a body carrying no credential is stored as it was, so the
// record still answers what the request actually said.
func TestAnOrdinaryBodyIsKeptWhole(t *testing.T) {
	const body = `{"model":"zen5","messages":[{"role":"user","content":"hello"}]}`
	if got := RedactBody(body); got != body {
		t.Fatalf("an ordinary body was altered:\n got %s\nwant %s", got, body)
	}
}

// The redactor only helps if the write calls it. A test that exercises
// RedactBody alone passes just as well when NewRecord stores the body raw —
// which is exactly how the raw store survived having a redactor in the tree.
func TestTheRecordWriteRedactsWhatItStores(t *testing.T) {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "record.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "NewRecord" {
			fn = d
		}
		return true
	})
	if fn == nil {
		t.Fatal("NewRecord not found — this test has stopped covering the write")
	}
	var body strings.Builder
	if err := printer.Fprint(&body, fs, fn); err != nil {
		t.Fatal(err)
	}
	src := body.String()
	if !strings.Contains(src, "ctx.Body()") {
		t.Fatal("NewRecord no longer reads the body — delete this test or repoint it")
	}
	if !strings.Contains(src, "RedactBody(") {
		t.Fatal("NewRecord stores the request body without redacting it")
	}
}
