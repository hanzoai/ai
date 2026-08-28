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

package object

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

var personaTestSeq int64

func usePersonaTestDB(t *testing.T) {
	t.Helper()
	n := atomic.AddInt64(&personaTestSeq, 1)
	dsn := fmt.Sprintf("file:personatest_%d?mode=memory&cache=shared", n)
	restore, err := UseMemoryDB(dsn, &Persona{})
	if err != nil {
		t.Fatalf("usePersonaTestDB: %v", err)
	}
	t.Cleanup(restore)
}

func savePersona(t *testing.T, owner, userID, name, displayName string) *Persona {
	t.Helper()
	p := &Persona{Owner: owner, UserId: userID, Name: name, DisplayName: displayName}
	if err := SavePersona(p); err != nil {
		t.Fatalf("SavePersona(%s/%s/%s): %v", owner, userID, name, err)
	}
	return p
}

// The catalogue ships in the binary, so it must be non-empty and addressable
// without any database at all.
func TestCatalogIsCompiledIn(t *testing.T) {
	if got := CatalogSize(); got < 500 {
		t.Fatalf("CatalogSize() = %d, want the shipped dataset (>=500)", got)
	}
	feynman := CatalogPersona("feynman")
	if feynman == nil {
		t.Fatal("CatalogPersona(feynman) = nil, want the stock persona")
	}
	if feynman.Source != PersonaSourceCatalog {
		t.Errorf("Source = %q, want %q", feynman.Source, PersonaSourceCatalog)
	}
	if feynman.Ocean.Openness == 0 {
		t.Error("Ocean.Openness = 0, want the dataset's score")
	}
	if CatalogPersona("no_such_person_anywhere") != nil {
		t.Error("CatalogPersona(unknown) should be nil")
	}
}

// The two index directories in the dataset are not people and must not be
// addressable as personas.
func TestCatalogExcludesIndexes(t *testing.T) {
	for _, name := range []string{"index", "categories"} {
		if p := CatalogPersona(name); p != nil {
			t.Errorf("CatalogPersona(%q) = %+v, want nil — it is an index, not a person", name, p)
		}
	}
}

// A persona carrying hand-written prose uses it verbatim; one with only
// structured fields still produces a usable system turn rather than nothing.
func TestSystemTurn(t *testing.T) {
	authored := &Persona{Name: "x", Prompt: "  You are X.  "}
	if got := authored.System(); got != "You are X." {
		t.Errorf("System() = %q, want the authored prompt verbatim", got)
	}

	composed := &Persona{
		Name:          "ada",
		DisplayName:   "Ada Lovelace",
		Description:   "The first programmer",
		Philosophy:    "The Analytical Engine weaves algebraic patterns.",
		Ocean:         Ocean{Openness: 97, Conscientiousness: 88},
		Tools:         PersonaTools{Domains: []string{"mathematics"}},
		Contributions: StringSlice{"the first algorithm"},
		Quotes:        StringSlice{"That brain of mine is something more than merely mortal."},
	}
	got := composed.System()
	for _, want := range []string{
		"Ada Lovelace", "The first programmer", "Analytical Engine",
		"mathematics", "the first algorithm", "merely mortal", "openness 97",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("System() missing %q\n---\n%s", want, got)
		}
	}
}

// Scores outside the scale are held at its edges rather than stored as given.
func TestOceanClampedOnSave(t *testing.T) {
	usePersonaTestDB(t)
	p := &Persona{
		Owner: "hanzo", UserId: "alice", Name: "wild",
		Ocean: Ocean{Openness: 500, Neuroticism: -20},
	}
	if err := SavePersona(p); err != nil {
		t.Fatalf("SavePersona: %v", err)
	}
	if p.Ocean.Openness != 100 || p.Ocean.Neuroticism != 0 {
		t.Errorf("Ocean = %+v, want openness clamped to 100 and neuroticism to 0", p.Ocean)
	}
}

func TestPersonaNameValidation(t *testing.T) {
	for _, ok := range []string{"feynman", "north-star", "my_agent_2"} {
		if !ValidPersonaName(ok) {
			t.Errorf("ValidPersonaName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Feynman", "with space", "slash/es", "dots.", strings.Repeat("a", 129)} {
		if ValidPersonaName(bad) {
			t.Errorf("ValidPersonaName(%q) = true, want false", bad)
		}
	}
	usePersonaTestDB(t)
	err := SavePersona(&Persona{Owner: "hanzo", UserId: "alice", Name: "Bad Name"})
	if err == nil {
		t.Error("SavePersona with an invalid name should fail")
	}
}

// A saved persona replaces the catalogue entry of the same name for its owner,
// and deleting it uncovers the original again.
func TestSavedPersonaShadowsCatalog(t *testing.T) {
	usePersonaTestDB(t)
	const org, user = "hanzo", "alice"

	stock, err := GetPersona(org, user, "feynman")
	if err != nil {
		t.Fatalf("GetPersona: %v", err)
	}
	if stock == nil || stock.Source != PersonaSourceCatalog {
		t.Fatalf("before save: got %+v, want the catalogue entry", stock)
	}

	savePersona(t, org, user, "feynman", "My Feynman")

	mine, err := GetPersona(org, user, "feynman")
	if err != nil {
		t.Fatalf("GetPersona: %v", err)
	}
	if mine.Source != PersonaSourceUser || mine.DisplayName != "My Feynman" {
		t.Fatalf("after save: got %+v, want the saved persona", mine)
	}

	// The name appears once, not twice, in the merged list.
	all, err := GetPersonas(org, user)
	if err != nil {
		t.Fatalf("GetPersonas: %v", err)
	}
	seen := 0
	for _, p := range all {
		if p.Name == "feynman" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("feynman appears %d times in the list, want exactly 1", seen)
	}
	if len(all) != CatalogSize() {
		t.Errorf("len(list) = %d, want %d — a shadowing save must not grow the list", len(all), CatalogSize())
	}

	deleted, err := DeletePersona(org, user, "feynman")
	if err != nil || !deleted {
		t.Fatalf("DeletePersona: deleted=%v err=%v", deleted, err)
	}
	back, err := GetPersona(org, user, "feynman")
	if err != nil {
		t.Fatalf("GetPersona: %v", err)
	}
	if back == nil || back.Source != PersonaSourceCatalog {
		t.Errorf("after delete: got %+v, want the catalogue entry back", back)
	}
}

// The security gate: alice and bob share an org, but bob must never reach
// alice's persona through any accessor, and must not be able to delete it.
func TestPersonaCrossUserIsolation(t *testing.T) {
	usePersonaTestDB(t)
	const org = "hanzo"

	savePersona(t, org, "alice", "secret-voice", "Alice's Voice")

	got, err := GetPersona(org, "bob", "secret-voice")
	if err != nil {
		t.Fatalf("GetPersona: %v", err)
	}
	if got != nil {
		t.Fatalf("bob read alice's persona: %+v", got)
	}

	bobList, err := GetPersonas(org, "bob")
	if err != nil {
		t.Fatalf("GetPersonas: %v", err)
	}
	for _, p := range bobList {
		if p.Name == "secret-voice" {
			t.Fatal("alice's persona appears in bob's list")
		}
	}

	deleted, err := DeletePersona(org, "bob", "secret-voice")
	if err != nil {
		t.Fatalf("DeletePersona: %v", err)
	}
	if deleted {
		t.Fatal("bob deleted alice's persona")
	}
	if still, _ := GetPersona(org, "alice", "secret-voice"); still == nil {
		t.Fatal("alice's persona was removed by bob's delete")
	}

	// Owner+Name is the primary key, so bob cannot take a name alice holds.
	if err := SavePersona(&Persona{Owner: org, UserId: "bob", Name: "secret-voice"}); err == nil {
		t.Fatal("bob overwrote a name alice already holds")
	}
}

// Saving twice under one name updates in place rather than adding a row, and the
// creation time is preserved.
func TestPersonaSaveIsUpsert(t *testing.T) {
	usePersonaTestDB(t)
	const org, user = "hanzo", "alice"

	first := savePersona(t, org, user, "coach", "Coach")
	second := savePersona(t, org, user, "coach", "Coach v2")

	if second.CreatedTime != first.CreatedTime {
		t.Errorf("CreatedTime changed on update: %q -> %q", first.CreatedTime, second.CreatedTime)
	}
	got, err := GetPersona(org, user, "coach")
	if err != nil {
		t.Fatalf("GetPersona: %v", err)
	}
	if got.DisplayName != "Coach v2" {
		t.Errorf("DisplayName = %q, want the updated value", got.DisplayName)
	}
}

// Identity is required: a persona with no owner or no user is not anonymous, it
// is unauthenticated.
func TestPersonaRequiresIdentity(t *testing.T) {
	usePersonaTestDB(t)
	if err := SavePersona(&Persona{Owner: "hanzo", Name: "x"}); err == nil {
		t.Error("SavePersona with no user should fail")
	}
	if err := SavePersona(&Persona{UserId: "alice", Name: "x"}); err == nil {
		t.Error("SavePersona with no owner should fail")
	}
}
