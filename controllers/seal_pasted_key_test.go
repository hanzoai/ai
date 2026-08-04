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

package controllers

import (
	"strings"
	"testing"

	"github.com/hanzoai/ai/object"
)

// TestSealPastedKey_LeavesAReferenceAlone: re-saving a provider from the admin
// UI without touching the key field must not re-seal, and must not mangle the
// reference.
func TestSealPastedKey_LeavesAReferenceAlone(t *testing.T) {
	p := &object.Provider{Owner: "admin", Name: "openrouter", ClientSecret: "kms://OPENROUTER_API_KEY"}
	if err := sealPastedKey("admin/openrouter", p); err != nil {
		t.Fatalf("sealPastedKey: %v", err)
	}
	if p.ClientSecret != "kms://OPENROUTER_API_KEY" {
		t.Errorf("ClientSecret = %q, want unchanged", p.ClientSecret)
	}
}

// TestSealPastedKey_EmptyIsNoop: an empty key field means "leave it as it is",
// not "erase the key".
func TestSealPastedKey_EmptyIsNoop(t *testing.T) {
	p := &object.Provider{Owner: "admin", Name: "openrouter"}
	if err := sealPastedKey("admin/openrouter", p); err != nil {
		t.Fatalf("sealPastedKey: %v", err)
	}
	if p.ClientSecret != "" {
		t.Errorf("ClientSecret = %q, want empty", p.ClientSecret)
	}
}

// TestSealPastedKey_RawFailsClosedWithoutKMS is the invariant that matters: with
// no store bound, a pasted key must ERROR rather than be written to the database
// in plaintext. Before this existed, UpdateProvider persisted it verbatim.
func TestSealPastedKey_RawFailsClosedWithoutKMS(t *testing.T) {
	object.SetSecretStore(nil)

	p := &object.Provider{Owner: "admin", Name: "openrouter", ClientSecret: "sk-or-v1-pasted"}
	err := sealPastedKey("admin/openrouter", p)
	if err == nil {
		t.Fatal("want an error with no KMS bound (never persist a raw key)")
	}
	if p.ClientSecret != "sk-or-v1-pasted" {
		t.Errorf("row mutated on failure: %q", p.ClientSecret)
	}
	if strings.Contains(err.Error(), "sk-or-v1-pasted") {
		t.Errorf("error echoes the key: %v", err)
	}
}
