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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoutedRecordNamesItsHost — a record that names a routed provider must also
// name the host that answered.
//
// Provider is our LABEL for a route; Origin is the address the bytes went to.
// Recording only the label records what we intended to call, and a fallback or a
// reroute is exactly the moment those two stop agreeing — the moment the label
// keeps reading correct while being wrong. Both together can be asked whether
// they still match; that answer is a measurement rather than a belief.
//
// This is a source-level pin because the defect is invisible at runtime: a
// constructor that omits Origin compiles, runs, and writes a complete-looking row
// forever. The gap was NOT hypothetical — the three ZAP error paths each set
// Provider from a provider that was right there in scope, and left the host empty
// on precisely the calls where a reroute had fired.
//
// Scope is deliberately narrow: only literals whose Provider comes from a provider
// VALUE in scope (provider.X / actualProvider.X). A surface that names a provider
// by constant string has no address to derive and is not asked for one.
func TestRoutedRecordNamesItsHost(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	var missing []string
	var checked int

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, isLit := n.(*ast.CompositeLit)
			if !isLit {
				return true
			}
			if id, isID := lit.Type.(*ast.Ident); !isID || id.Name != "usageRecord" {
				return true
			}

			var routed, hasOrigin bool
			for _, elt := range lit.Elts {
				kv, isKV := elt.(*ast.KeyValueExpr)
				if !isKV {
					continue
				}
				key, isID := kv.Key.(*ast.Ident)
				if !isID {
					continue
				}
				switch key.Name {
				case "Provider":
					// Routed == the value is a field/method of a provider in scope.
					if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
						if base, ok := sel.X.(*ast.Ident); ok {
							routed = base.Name == "provider" || base.Name == "actualProvider"
						}
					}
				case "Origin":
					hasOrigin = true
				}
			}
			if !routed {
				return true
			}
			checked++
			if !hasOrigin {
				missing = append(missing, fmt.Sprintf("%s:%d", f, fset.Position(lit.Pos()).Line))
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatal("found no routed usageRecord literals — the scan matched nothing, so it proves nothing")
	}
	if len(missing) > 0 {
		t.Errorf("%d routed usage record(s) name a provider but not the host that answered:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}
