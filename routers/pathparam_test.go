package routers

import (
	"regexp"
	"testing"
)

// A templated segment names a parameter a caller has to supply. When the
// document omits the declaration the generator still emits the method — named
// for the value it cannot take — so `get_videos_by_id(self)` requested the
// literal "{id}". The spelling half of that was already fixed (openAPIPath);
// this holds the other half.
func TestTemplatedSegmentsAreDeclared(t *testing.T) {
	doc := Document(built())
	paths, _ := doc["paths"].(map[string]any)
	re := regexp.MustCompile(`\{(\w+)\}`)
	bad := 0
	for p, item := range paths {
		want := re.FindAllStringSubmatch(p, -1)
		if len(want) == 0 {
			continue
		}
		methods, _ := item.(map[string]any)
		for verb, op := range methods {
			o, _ := op.(map[string]any)
			have := map[string]bool{}
			params, _ := o["parameters"].([]any)
			for _, q := range params {
				m, _ := q.(map[string]any)
				if m["in"] == "path" {
					n, _ := m["name"].(string)
					have[n] = true
				}
			}
			for _, w := range want {
				if !have[w[1]] {
					t.Errorf("%s %s: path param %q undeclared", verb, p, w[1])
					bad++
				}
			}
		}
	}
	t.Logf("templated paths checked; undeclared = %d", bad)
}
