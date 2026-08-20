package controllers

import (
	"encoding/json"
	"testing"
)

// BenchmarkListAvailableModels measures what GET /v1/models costs per call against
// the real catalog. Every SDK asks for this list on startup, so its per-call cost is
// the ceiling on how many clients can come up at once.
func BenchmarkListAvailableModels(b *testing.B) {
	useCatalog(b, "../conf/models.yaml")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if got := listAvailableModels(); len(got) == 0 {
			b.Fatal("empty catalog")
		}
	}
}

// BenchmarkListModelsResponse measures the whole answer: build the list, wrap it, and
// marshal the bytes that go on the wire.
func BenchmarkListModelsResponse(b *testing.B) {
	useCatalog(b, "../conf/models.yaml")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		body, err := json.Marshal(modelListEnvelope(listAvailableModels()))
		if err != nil {
			b.Fatal(err)
		}
		if len(body) == 0 {
			b.Fatal("empty body")
		}
	}
}
