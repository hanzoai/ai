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

// Command openapi renders the resource surface into the canonical spec.
//
//	go run ./cmd/openapi -spec ../openapi/ai/openapi.yaml          # write
//	go run ./cmd/openapi -spec ../openapi/ai/openapi.yaml -verify  # check only
//
// The canonical spec (hanzoai/openapi, OpenAPI 3.1, "v1.0.0 locked in") is the
// published contract every SDK and the docs site are generated from. The resource
// half of it is DERIVED from routers/resources.go — the same table that registers
// the routes — so the contract cannot describe a surface the service does not
// serve. That is the failure this replaces: the repo's swagger.json still
// documents `/api/<verb>-<noun>`, a base path nothing has ever served.
//
// Only the region between the generated markers is touched. The inference paths
// above it are hand-authored with real request/response schemas and stay that way:
// they are ten stable endpoints whose bodies are the OpenAI wire format, not
// something a table can know. Generation is for the part that repeats.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/hanzoai/ai/routers"
)

const (
	beginMarker = "  # BEGIN generated: resource surface (go run ./cmd/openapi) — DO NOT EDIT BY HAND"
	endMarker   = "  # END generated: resource surface"
)

func main() {
	spec := flag.String("spec", "", "path to the canonical ai/openapi.yaml")
	checkOnly := flag.Bool("verify", false, "exit non-zero if the spec is out of date; write nothing")
	flag.Parse()

	if *spec == "" {
		fail("-spec is required (path to hanzoai/openapi ai/openapi.yaml)")
	}
	if err := routers.AssertOperationIDsUnique(); err != nil {
		fail("refusing to render: %v", err)
	}

	current, err := os.ReadFile(*spec)
	if err != nil {
		fail("read spec: %v", err)
	}
	next, err := render(string(current))
	if err != nil {
		fail("%v", err)
	}

	if string(current) == next {
		fmt.Fprintf(os.Stderr, "openapi: up to date (%d paths)\n", len(routers.OpenAPIPathsSorted()))
		return
	}
	if *checkOnly {
		fail("spec is OUT OF DATE — run: go run ./cmd/openapi -spec %s", *spec)
	}
	if err := os.WriteFile(*spec, []byte(next), 0o644); err != nil {
		fail("write spec: %v", err)
	}
	fmt.Fprintf(os.Stderr, "openapi: wrote %d generated paths to %s\n", len(routers.OpenAPIPathsSorted()), *spec)
}

// render replaces the generated region with the freshly-rendered resource paths,
// leaving every other byte of the spec untouched.
func render(spec string) (string, error) {
	begin := strings.Index(spec, beginMarker)
	end := strings.Index(spec, endMarker)
	if begin < 0 || end < 0 || end < begin {
		return "", fmt.Errorf("spec has no generated region — add these two lines under `paths:`:\n%s\n%s", beginMarker, endMarker)
	}
	body, err := renderPaths()
	if err != nil {
		return "", err
	}
	return spec[:begin] + beginMarker + "\n" + body + spec[end:], nil
}

// renderPaths emits the path items as YAML at the two-space indent `paths:`
// entries sit at, in sorted order so the output is byte-stable across runs.
func renderPaths() (string, error) {
	paths := routers.OpenAPIPaths()
	keys := routers.OpenAPIPathsSorted()

	var b strings.Builder
	for _, k := range keys {
		item, _ := paths[k].(map[string]any)
		// Emit one path item at a time: yaml.Marshal of the whole map would sort
		// keys its own way and lose the method ordering below.
		node, err := ordered(item)
		if err != nil {
			return "", fmt.Errorf("encode %s: %w", k, err)
		}
		out, err := yaml.Marshal(map[string]*yaml.Node{k: node})
		if err != nil {
			return "", fmt.Errorf("marshal %s: %w", k, err)
		}
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String(), nil
}

// ordered returns the path item with its methods in conventional HTTP order, so a
// reader sees get before delete rather than alphabetical noise.
func ordered(item map[string]any) (*yaml.Node, error) {
	order := []string{"get", "post", "put", "patch", "delete"}
	rank := map[string]int{}
	for i, m := range order {
		rank[m] = i
	}
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, oki := rank[keys[i]]
		rj, okj := rank[keys[j]]
		if oki && okj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	// yaml.v3 sorts a Go map's keys on encode, so the METHOD order has to be
	// carried by an explicit mapping node rather than a map.
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		v := &yaml.Node{}
		if err := v.Encode(item[k]); err != nil {
			return nil, err
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k}, v)
	}
	return m, nil
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "openapi: "+format+"\n", a...)
	os.Exit(1)
}
