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

package main

import (
	"bytes"
	"os"
	"testing"
)

// The lifted file is a DERIVED artifact, so the gate is that re-deriving it changes
// nothing. Without this a doc comment can be improved, or a route added, and the
// published sentence stays whatever it was at the last generate — the artifact and
// the routers' own bijection test would both still pass, because they check the
// file against itself.
func TestWiredGenIsUpToDate(t *testing.T) {
	got, err := render("../..")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../routers/wired_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("routers/wired_gen.go is stale — a handler's doc comment or a registration " +
			"moved without the lift being re-run. Run `go generate ./routers` and commit it.")
	}
}

// The projections of one comment, as a reader meets them.
func TestProse(t *testing.T) {
	for _, c := range []struct{ name, in, summary, description string }{
		{
			name:        "godoc convention drops the subject and its copula",
			in:          "AudioSpeech is the OpenAI-compatible TTS endpoint. It streams the bytes back.\n",
			summary:     "The OpenAI-compatible TTS endpoint.",
			description: "The OpenAI-compatible TTS endpoint. It streams the bytes back.",
		},
		{
			name:        "a wrapped first sentence becomes ONE line",
			in:          "ListModels returns the models\nfrom the routing table. Requires a Bearer token.\n",
			summary:     "Returns the models from the routing table.",
			description: "Returns the models\nfrom the routing table. Requires a Bearer token.",
		},
		{
			name:        "annotations are a generator's input, not prose",
			in:          "Health\n@Title Health\n@Tag System API\n@Description check if the system is live\n@router /health [get]\n",
			summary:     "Check if the system is live",
			description: "Check if the system is live",
		},
		{
			name:        "an indented annotation continuation belongs to the annotation",
			in:          "GetAdminProviders\n@Title GetAdminProviders\n@Description List admin-owned Model providers\n\n\t(enabled/primary). Never returns secret material.\n\n@router /admin/providers [get]\n",
			summary:     "List admin-owned Model providers (enabled/primary).",
			description: "List admin-owned Model providers\n(enabled/primary). Never returns secret material.",
		},
		{
			name:        "a version is not a sentence end",
			in:          "Ping answers v1.2 of the protocol. Nothing else.\n",
			summary:     "Ping answers v1.2 of the protocol.",
			description: "Ping answers v1.2 of the protocol. Nothing else.",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			handler := "AudioSpeech"
			switch {
			case bytes.HasPrefix([]byte(c.in), []byte("ListModels")):
				handler = "ListModels"
			case bytes.HasPrefix([]byte(c.in), []byte("Health")):
				handler = "Health"
			case bytes.HasPrefix([]byte(c.in), []byte("GetAdminProviders")):
				handler = "GetAdminProviders"
			case bytes.HasPrefix([]byte(c.in), []byte("Ping")):
				handler = "Pong" // not the leading identifier, so nothing is stripped
			}
			summary, description := prose(c.in, handler)
			if summary != c.summary {
				t.Errorf("summary\n got %q\nwant %q", summary, c.summary)
			}
			if description != c.description {
				t.Errorf("description\n got %q\nwant %q", description, c.description)
			}
		})
	}
}

// A handler with no doc comment is a build failure naming the route, never a
// published operation with nothing to say.
func TestSilenceIsRefused(t *testing.T) {
	if s, d := prose("", "Whatever"); s != "" || d != "" {
		t.Errorf("prose of an empty comment = (%q, %q), want silence", s, d)
	}
}
