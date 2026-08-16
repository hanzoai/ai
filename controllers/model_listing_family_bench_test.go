package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// BenchmarkListWithFamily measures GET /v1/models against a catalog the size
// production serves: the configured routes plus a family lineup discovered from an
// upstream. orCatalog is the upstream body; point it at a saved openrouter catalog to
// reproduce the real shape.
func BenchmarkListWithFamily(b *testing.B) {
	body, err := os.ReadFile(os.Getenv("FAMILY_CATALOG"))
	if err != nil {
		b.Skipf("set FAMILY_CATALOG to an upstream /v1/models body: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	b.Setenv("OPENROUTER_URL", srv.URL)
	b.Setenv("OPENROUTER_API_KEY", "bench")

	if err := InitModelConfig("../conf/models.yaml"); err != nil {
		b.Fatalf("load conf/models.yaml: %v", err)
	}

	got := listAvailableModels()
	b.Logf("catalog size: %d models", len(got))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if out, err := json.Marshal(modelListEnvelope(listAvailableModels())); err != nil || len(out) == 0 {
			b.Fatal("marshal failed")
		}
	}
}
