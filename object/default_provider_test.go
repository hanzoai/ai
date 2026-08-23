// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// Every category answers with the provider marked default in it. Three of these
// used to ask for the row named "" in the admin org — no row — so they reported
// no default however many were marked one, and a store that named no provider of
// its own could never resolve one.
func TestEachCategoryFindsItsDefault(t *testing.T) {
	withStore(t)
	categories := map[string]func() (*Provider, error){
		"Storage":        GetDefaultStorageProvider,
		"Video":          GetDefaultVideoProvider,
		"Embedding":      GetDefaultEmbeddingProvider,
		"Agent":          GetDefaultAgentProvider,
		"Text-to-Speech": GetDefaultTextToSpeechProvider,
		"Speech-to-Text": GetDefaultSpeechToTextProvider,
	}

	// Nothing seeded: every category has no default, and that is not an error.
	for name, get := range categories {
		got, err := get()
		if err != nil {
			t.Errorf("%s answered %v with nothing seeded", name, err)
		}
		if got != nil {
			t.Errorf("%s found %q with nothing seeded", name, got.Name)
		}
	}

	for category := range categories {
		if _, err := AddProvider(&Provider{
			Owner: "admin", Name: "default-" + category, Category: category,
			IsDefault: true, State: "Active",
		}); err != nil {
			t.Fatal(err)
		}
		// A second one in the same category, not marked default.
		if _, err := AddProvider(&Provider{
			Owner: "admin", Name: "other-" + category, Category: category, State: "Active",
		}); err != nil {
			t.Fatal(err)
		}
	}

	for name, get := range categories {
		got, err := get()
		if err != nil {
			t.Errorf("%s answered %v", name, err)
			continue
		}
		if got == nil {
			t.Errorf("%s found no default, and one is marked", name)
			continue
		}
		if got.Name != "default-"+name {
			t.Errorf("%s found %q, want the one marked default", name, got.Name)
		}
		if got.Category != name {
			t.Errorf("%s found a %s provider", name, got.Category)
		}
	}
}
