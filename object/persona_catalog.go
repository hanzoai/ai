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

// The persona catalogue: the hanzoai/personas dataset compiled into this binary.
//
// It is read-only and identical for every caller, so it is parsed once and held
// in memory rather than written to a table — no seeding on first boot, no
// migration when the dataset grows, and no chance of a stale copy in one
// deployment's database. Growing the catalogue is a version bump of this file.
//
// Regenerate with `go generate ./object` after pulling hanzoai/personas.
package object

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

//go:generate go run ./personadata/gen -src ../../personas -out personadata/personas.json

//go:embed personadata/personas.json
var personaCatalogJSON []byte

// catalogEntry is the on-disk shape of the dataset, which is not the shape of a
// stored persona: it has an id where a row has owner+name, and it carries the
// dataset's richer psychological fields, which are kept as raw prose for the
// system turn rather than modelled as columns nothing queries.
type catalogEntry struct {
	Id            string       `json:"id"`
	Name          string       `json:"name"`
	Category      string       `json:"category"`
	Description   string       `json:"description"`
	Philosophy    string       `json:"philosophy"`
	Ocean         Ocean        `json:"ocean"`
	Tools         PersonaTools `json:"tools"`
	Contributions []string     `json:"contributions"`
	Quotes        []string     `json:"quotes"`
	Prompt        string       `json:"prompt"`
}

var (
	catalogOnce  sync.Once
	catalogList  []*Persona
	catalogIndex map[string]*Persona
)

func loadCatalog() {
	var entries []catalogEntry
	if err := json.Unmarshal(personaCatalogJSON, &entries); err != nil {
		// An unparseable catalogue means an empty one: personas a caller saved
		// still work, and the failure is visible as a catalogue of zero rather
		// than as a process that will not start.
		catalogIndex = map[string]*Persona{}
		return
	}
	catalogList = make([]*Persona, 0, len(entries))
	catalogIndex = make(map[string]*Persona, len(entries))
	for _, e := range entries {
		if !ValidPersonaName(e.Id) {
			continue
		}
		p := &Persona{
			Name:          e.Id,
			DisplayName:   e.Name,
			Category:      e.Category,
			Description:   e.Description,
			Philosophy:    e.Philosophy,
			Ocean:         e.Ocean,
			Tools:         e.Tools,
			Contributions: StringSlice(e.Contributions),
			Quotes:        StringSlice(e.Quotes),
			Prompt:        e.Prompt,
			Source:        PersonaSourceCatalog,
		}
		if p.DisplayName == "" {
			p.DisplayName = p.Name
		}
		catalogList = append(catalogList, p)
		catalogIndex[p.Name] = p
	}
	sort.SliceStable(catalogList, func(i, j int) bool {
		return strings.ToLower(catalogList[i].DisplayName) < strings.ToLower(catalogList[j].DisplayName)
	})
}

// CatalogPersonas returns every stock persona, by display name.
//
// The returned pointers address the shared catalogue: callers read them, and a
// caller that needs to change one saves a persona of its own instead.
func CatalogPersonas() []*Persona {
	catalogOnce.Do(loadCatalog)
	return catalogList
}

// CatalogPersona returns one stock persona by name, or nil.
func CatalogPersona(name string) *Persona {
	catalogOnce.Do(loadCatalog)
	return catalogIndex[strings.TrimSpace(name)]
}

// CatalogSize is how many stock personas shipped in this binary.
func CatalogSize() int {
	catalogOnce.Do(loadCatalog)
	return len(catalogList)
}
