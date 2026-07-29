package routers

import "testing"

func TestZZZCountResourceRoutes(t *testing.T) {
	p := OpenAPIPaths()
	ops := 0
	for _, v := range p {
		if m, ok := v.(map[string]any); ok {
			ops += len(m)
		}
	}
	t.Logf("AI_OPENAPI_PATHS=%d AI_OPENAPI_OPS=%d", len(p), ops)
}
