package builtin_tool

import "testing"

func TestWebToolsAreRegisteredAndExposed(t *testing.T) {
	r := NewToolRegistry()
	for _, name := range []string{"web_search", "fetch_url", "deep_research"} {
		if _, ok := r.GetTool(name); !ok {
			t.Errorf("%s is not registered — an agent cannot call it", name)
		}
	}
	// The Responses API hands agents GetToolsAsProtocolTools; a tool absent there is
	// invisible however well it is registered.
	exposed := map[string]bool{}
	for _, pt := range r.GetToolsAsProtocolTools() {
		exposed[pt.Name] = true
		if pt.Description == "" {
			t.Errorf("%s has no description; a model cannot tell when to use it", pt.Name)
		}
	}
	for _, name := range []string{"web_search", "fetch_url", "deep_research"} {
		if !exposed[name] {
			t.Errorf("%s is registered but not exposed in the protocol tool list", name)
		}
	}
	if len(r.GetAllTools()) != 8 {
		t.Errorf("registry holds %d tools, want 8 (5 time + 3 web)", len(r.GetAllTools()))
	}
}
