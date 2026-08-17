package routers

import (
	"github.com/zap-proto/zip"

	"strings"
	"testing"
)

// filterPaths is every /v1 path a filter keys on to decide something. Each is a
// claim that a route serves it — a claim nothing checked until this test, which
// is how three of them went on naming routes that had been renamed away.
//
// A filter keyed on a path no route serves is not an error anywhere: the compare
// simply never matches, so the rule reads as passing while protecting nothing.
// Add the path here when you add the filter, and this holds the two together.
var filterPaths = []struct {
	path string
	why  string
}{
	{"/v1/ai/nodes", "CacheControlFilter: a node's tunnel action carries its password"},
	{"/v1/ai/chats", "CacheControlFilter: a caller's own conversations"},
	{"/v1/ai/messages", "CacheControlFilter: what was said in them"},
	{"/v1/ai/version", "ratelimit: an operational readout is exempt"},
	{"/v1/ai/system", "ratelimit: an operational readout is exempt"},
	{"/v1/ai/signin", "RecordMessage: no principal to attribute yet"},
	{"/v1/ai/assets", "RecordMessage: static, nothing to record"},
}

// TestFilterPathsAreServed holds every filter's path to a route that exists.
//
// The filters match by prefix, so a path qualifies when some registered pattern
// starts with it — /v1/ai/nodes covers the member tunnel action beneath it.
func TestFilterPathsAreServed(t *testing.T) {
	// The live table, built the way the runtime builds it.
	app := zip.New(zip.Config{DisableStartupMessage: true, ReadBufferSize: 32 << 10})
	Register(app)
	patterns := Patterns(app)
	if len(patterns) == 0 {
		t.Fatal("no routes registered; the instrument is broken, not the filters")
	}

	for _, fp := range filterPaths {
		served := false
		for pat := range patterns {
			if pat == fp.path || strings.HasPrefix(pat, fp.path+"/") {
				served = true
				break
			}
		}
		if !served {
			t.Errorf("no route serves %q, so the filter keyed on it never fires (%s)",
				fp.path, fp.why)
		}
	}
}

// TestFiltersNameNoVerbPath keeps the filters on the resource surface. A verb
// path here is a rename that was not finished: the route moved and the filter
// stayed, which is silent because a prefix that matches nothing just never fires.
func TestFiltersNameNoVerbPath(t *testing.T) {
	verbs := []string{"/v1/get-", "/v1/add-", "/v1/update-", "/v1/delete-", "/v1/list-", "/v1/set-"}
	for _, fp := range filterPaths {
		for _, v := range verbs {
			if strings.HasPrefix(fp.path, v) {
				t.Errorf("%q is a verb path; the resource surface is /v1/<ns>/<noun>", fp.path)
			}
		}
	}
}
