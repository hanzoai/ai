package routers

import (
	"testing"

	"github.com/hanzoai/ai/controllers"
)

// Every handler the answers table names must be one this package actually routes.
//
// The compiler already refuses a shape whose Go type was renamed or removed. What
// it cannot see is the KEY: rename a handler and its entry keeps compiling, still
// matches nothing, and the operation quietly goes back to publishing no body — the
// same silence this table exists to end, arrived at by a rename nobody connected
// to the document.
func TestAnswersNameRoutedHandlers(t *testing.T) {
	routed := map[string]bool{}
	for _, w := range wired {
		routed[w.Handler] = true
	}
	for name := range controllers.Answers() {
		if !routed[name] {
			t.Errorf("answers names %q, which no route reaches — renamed, or removed", name)
		}
	}
}
