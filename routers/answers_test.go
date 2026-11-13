package routers

import (
	"strings"
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

// A wrong-door refusal is part of each operation's published contract: the 400 on
// /v1/decisions names the chat endpoint, and the 400 on /v1/chat/completions names
// the decisions endpoint, so a client generated from the document can say which.
func TestWrongDoorRefusalsArePublished(t *testing.T) {
	paths := docPaths(t)
	for path, door := range map[string]string{
		"/v1/decisions":        "POST /v1/chat/completions",
		"/v1/chat/completions": "POST /v1/decisions",
	} {
		item, _ := paths[path].(map[string]any)
		post, _ := item["post"].(map[string]any)
		responses, _ := post["responses"].(map[string]any)
		bad, _ := responses["400"].(map[string]any)
		says, _ := bad["description"].(string)
		if !strings.Contains(says, door) {
			t.Errorf("POST %s 400 says %q, want it to name %s", path, says, door)
		}
		if bad["content"] == nil {
			t.Errorf("POST %s 400 publishes no body schema", path)
		}
	}
}
