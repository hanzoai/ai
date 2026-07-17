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

package router

import (
	"math"
	"testing"
)

// These benchmarks measure the DECISION COMPUTE of the three router families —
// the work done AFTER any input is available, so heuristic/vector/linear are
// compared apples-to-apples. The vector + linear families additionally need a
// prompt embedding (measured separately against the real embedder), which
// dominates their end-to-end latency; these numbers isolate the routing math so
// we know how much the router itself costs once the embedding exists.
//
//   go test ./router/ -bench BenchmarkApproach -benchmem -run '^$'

const (
	embedDim  = 768 // a typical embedding dimension (Qwen3-Embedding-0.6B)
	numModels = 20  // candidate models/concept vectors
	numTasks  = 8   // heuristic task buckets
)

// ── vector router: cosine nearest-concept over K model vectors ────────────────

// buildConcepts makes K unit-ish concept vectors (one per model) — in production
// these are precomputed once (mean embedding of each model's exemplar prompts).
func buildConcepts() [][]float32 {
	cs := make([][]float32, numModels)
	for i := range cs {
		v := make([]float32, embedDim)
		for j := range v {
			v[j] = float32(math.Sin(float64(i*embedDim + j)))
		}
		cs[i] = normalize(v)
	}
	return cs
}

func normalize(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(s))
	if n == 0 {
		return v
	}
	for i := range v {
		v[i] /= n
	}
	return v
}

// vectorRoute picks the concept with the highest cosine to the prompt embedding.
// This is the ENTIRE vector-router decision given an embedding: K dot products.
func vectorRoute(emb []float32, concepts [][]float32) int {
	best, bestScore := 0, float32(-2)
	for i, c := range concepts {
		var dot float32
		for j := range emb {
			dot += emb[j] * c[j]
		}
		if dot > bestScore {
			bestScore, best = dot, i
		}
	}
	return best
}

// BenchmarkApproachVectorNN — the vector router's decision cost (K=20 cosine
// comparisons over 768-dim), EXCLUDING the embedding call.
func BenchmarkApproachVectorNN(b *testing.B) {
	concepts := buildConcepts()
	emb := normalize(buildConcepts()[3]) // a query embedding
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = vectorRoute(emb, concepts)
	}
}

// ── learned linear router: softmax-logistic over the embedding ────────────────

// A [numModels x embedDim] weight matrix + bias — a logistic-regression / linear
// classifier trained on (embedding → best model) pairs. Inference is one matvec.
type linearHead struct {
	W [][]float32 // numModels x embedDim
	b []float32   // numModels
}

func buildLinearHead() *linearHead {
	h := &linearHead{W: make([][]float32, numModels), b: make([]float32, numModels)}
	for i := range h.W {
		row := make([]float32, embedDim)
		for j := range row {
			row[j] = float32(math.Cos(float64(i*7+j))) * 0.1
		}
		h.W[i] = row
		h.b[i] = float32(i) * 0.01
	}
	return h
}

// predict returns the argmax model — the entire learned-linear-router decision
// given an embedding: one [K x D] matvec.
func (h *linearHead) predict(emb []float32) int {
	best, bestLogit := 0, float32(math.Inf(-1))
	for i := range h.W {
		logit := h.b[i]
		row := h.W[i]
		for j := range emb {
			logit += row[j] * emb[j]
		}
		if logit > bestLogit {
			bestLogit, best = logit, i
		}
	}
	return best
}

// BenchmarkApproachLinearHead — the learned linear/logistic router's decision
// cost (one [20x768] matvec), EXCLUDING the embedding call. This is what a
// trained-on-real-token-data router costs to run per request.
func BenchmarkApproachLinearHead(b *testing.B) {
	h := buildLinearHead()
	emb := normalize(buildConcepts()[5])
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.predict(emb)
	}
}

// BenchmarkApproachHeuristic — the current keyword-rule router, for the
// side-by-side (no embedding needed — it reads the raw text).
func BenchmarkApproachHeuristic(b *testing.B) {
	req := Request{Text: "Refactor this Go function to fix the nested loop bug and prove the complexity", ApproxTokens: 40}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Classify(req)
	}
}
