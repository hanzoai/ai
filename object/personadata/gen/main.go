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

// Compiles the hanzoai/personas dataset into the single JSON file the AI binary
// embeds. Point -src at a checkout of that repository; the dataset is the source
// of truth and this output is a build artifact of it.
//
// The dataset is not uniform — some entries carry `tools` as a list rather than
// an object, some have no profile.json at all, and two directories (index,
// categories) are indexes rather than people. Each of those is normalized or
// dropped here and reported on stderr, so the shape the server reads is regular
// and the irregularities stay visible instead of being silently absorbed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ocean struct {
	Openness          int `json:"openness"`
	Conscientiousness int `json:"conscientiousness"`
	Extraversion      int `json:"extraversion"`
	Agreeableness     int `json:"agreeableness"`
	Neuroticism       int `json:"neuroticism"`
}

type tools struct {
	Essential []string `json:"essential"`
	Preferred []string `json:"preferred"`
	Domains   []string `json:"domains"`
}

type entry struct {
	Id            string   `json:"id"`
	Name          string   `json:"name"`
	Category      string   `json:"category"`
	Description   string   `json:"description,omitempty"`
	Philosophy    string   `json:"philosophy,omitempty"`
	Ocean         ocean    `json:"ocean"`
	Tools         tools    `json:"tools"`
	Contributions []string `json:"contributions,omitempty"`
	Quotes        []string `json:"quotes,omitempty"`
	Prompt        string   `json:"prompt,omitempty"`
}

// notPeople are directories in the dataset that hold indexes, not personalities.
var notPeople = map[string]bool{"index": true, "categories": true}

func main() {
	src := flag.String("src", "../personas", "checkout of hanzoai/personas")
	out := flag.String("out", "personadata/personas.json", "generated catalogue")
	flag.Parse()

	dir := filepath.Join(*src, "personas")
	names, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", dir, err)
		os.Exit(1)
	}

	var entries []entry
	var notes []string
	for _, d := range names {
		if !d.IsDir() || notPeople[d.Name()] {
			if notPeople[d.Name()] {
				notes = append(notes, d.Name()+": index, not a person — dropped")
			}
			continue
		}
		e, ns := load(dir, d.Name())
		notes = append(notes, ns...)
		if e != nil {
			entries = append(entries, *e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Id < entries[j].Id })

	buf, err := json.Marshal(entries)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "%d personas -> %s (%d bytes)\n", len(entries), *out, len(buf))
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "  "+n)
	}
}

func load(dir, slug string) (*entry, []string) {
	var notes []string
	e := entry{Id: slug, Ocean: ocean{50, 50, 50, 50, 50}}

	if md, err := os.ReadFile(filepath.Join(dir, slug, "PERSONA.md")); err == nil {
		e.Prompt = string(md)
	}

	raw, err := os.ReadFile(filepath.Join(dir, slug, "profile.json"))
	if err != nil {
		if e.Prompt == "" {
			return nil, append(notes, slug+": neither profile.json nor PERSONA.md — dropped")
		}
		// Prose with no profile: the role personas (mentor, engineer, analyst).
		e.Name = title(slug)
		e.Category = "role"
		return &e, append(notes, slug+": PERSONA.md only, no profile.json")
	}

	var p map[string]json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, append(notes, slug+": unparseable profile.json — dropped")
	}

	e.Name = str(p["name"])
	if e.Name == "" {
		e.Name = title(slug)
	}
	if id := str(p["id"]); id != "" && id != slug {
		notes = append(notes, fmt.Sprintf("%s: id is %q — using the directory name", slug, id))
	}
	e.Category = str(p["category"])
	if e.Category == "" {
		e.Category = "other"
		notes = append(notes, slug+": no category")
	}
	e.Description = str(p["description"])
	e.Philosophy = str(p["philosophy"])
	if raw, ok := p["ocean"]; ok {
		if err := json.Unmarshal(raw, &e.Ocean); err != nil {
			notes = append(notes, slug+": ocean is not an object — defaulted to 50s")
		}
	} else {
		notes = append(notes, slug+": no ocean — defaulted to 50s")
	}
	if raw, ok := p["tools"]; ok {
		if err := json.Unmarshal(raw, &e.Tools); err != nil {
			// Some entries carry tools as a bare list of essentials.
			var flat []string
			if json.Unmarshal(raw, &flat) == nil {
				e.Tools.Essential = flat
				notes = append(notes, slug+": tools is a list — read as essential")
			} else {
				notes = append(notes, slug+": tools unreadable — dropped")
			}
		}
	}
	e.Contributions = list(p["contributions"])
	e.Quotes = list(p["quotes"])
	clamp(&e.Ocean)
	return &e, notes
}

func str(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func list(raw json.RawMessage) []string {
	var out []string
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil {
		return nil
	}
	kept := out[:0]
	for _, s := range out {
		if s = strings.TrimSpace(s); s != "" {
			kept = append(kept, s)
		}
	}
	return kept
}

func clamp(o *ocean) {
	for _, f := range []*int{&o.Openness, &o.Conscientiousness, &o.Extraversion, &o.Agreeableness, &o.Neuroticism} {
		if *f < 0 {
			*f = 0
		} else if *f > 100 {
			*f = 100
		}
	}
}

func title(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool { return r == '_' || r == '-' })
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
