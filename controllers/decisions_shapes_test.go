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
	"encoding/json"
	"reflect"
	"testing"
)

// Every form the Decisions API takes and answers survives the types the document
// is built from: a choice over a map and over a list of labels, a score and a
// noul, each read as the variant its `type` names (as the published discriminator
// does), a string, object or array state, and a response whose legend holds
// strings. A form a type cannot carry is a form the document does not describe.
func TestEveryDecisionFormRoundTrips(t *testing.T) {
	variant := map[string]func() any{
		"choice":  func() any { return &decisionsChoice{} },
		"score":   func() any { return &decisionsScore{} },
		"noul":    func() any { return &decisionsNoul{} },
		"boolean": func() any { return &decisionsBoolean{} },
	}
	same := func(t *testing.T, into any, wire string) {
		t.Helper()
		if err := json.Unmarshal([]byte(wire), into); err != nil {
			t.Fatalf("decode %s: %v", wire, err)
		}
		out, err := json.Marshal(into)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		var want, got any
		_ = json.Unmarshal([]byte(wire), &want)
		_ = json.Unmarshal(out, &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip\n got %s\nwant %s", out, wire)
		}
	}
	for _, q := range []string{
		`{"type":"choice","instructions":"What does the customer want?","criteria":{"refund":"money back","replacement":"a new item","status":null}}`,
		`{"type":"choice","criteria":["account","payments","shipping"]}`,
		`{"type":"score","instructions":"How urgent is it?","criteria":["later","this week","today","now"]}`,
		`{"type":"noul","instructions":{"q":"Does this read a key?"},"criteria":{"true":"it reads a key","false":"it does not"},"labels":{"true":"yes","false":"no"}}`,
		`{"type":"boolean","instructions":"Is it urgent?","criteria":{"true":"today"}}`,
	} {
		var kind struct{ Type string }
		_ = json.Unmarshal([]byte(q), &kind)
		t.Run(kind.Type, func(t *testing.T) { same(t, variant[kind.Type](), q) })
	}
	for _, state := range []string{`"My order arrived broken and I want my money back."`, `{"message":"where is my parcel?"}`, `["a","b"]`} {
		var c decisionContent
		same(t, &c, state)
	}
	same(t, &decisionsResponse{}, `{"id":"dec_1","model":"kai","provider":"hanzo",
		"answers":{"hot":{"type":"boolean","probability":0.91},"bug":{"type":"noul","noul":0.2},
		"urgency":{"type":"score","score":1.95,"confidence":0.4667,
		"legend":{"0":"later","1":"this week","2":"today","3":"now"},
		"probabilities":{"0":0.05,"1":0.2,"2":0.5,"3":0.25}}},
		"usage":{"input_tokens":41,"output_tokens":0},"routing":{"backend":"kai","checkpoint":"a7","reason":"asked"},
		"state_hash":"h","latency_ms":12}`)
	// A yes/no answer in either spelling, and a noul as Kai v0.3 answers it.
	for _, a := range []string{
		`{"type":"boolean","probability":0.25}`,
		`{"type":"noul","noul":0.25}`,
		`{"type":"noul","noul":0.25,"confidence":0.5,"answer_confidence":0.75}`,
	} {
		same(t, &decisionsAnswer{}, a)
	}
}
