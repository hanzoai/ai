package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCodexCLIResponsesWire is an opt-in acceptance test against the installed
// Codex binary. Unit tests cover every event; this proves the real client can
// consume those events without depending on Codex in normal CI images.
func TestCodexCLIResponsesWire(t *testing.T) {
	if os.Getenv("HANZO_CODEX_INTEGRATION") == "" {
		t.Skip("set HANZO_CODEX_INTEGRATION=1 to run the installed Codex CLI acceptance test")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is not installed")
	}

	var calls atomic.Int32
	var sawToolOutput atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			var request OpenAIResponsesRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if strings.Contains(string(request.Input), `"type":"function_call_output"`) &&
				strings.Contains(string(request.Input), "HANZO_CODEX_TOOL_OK") {
				sawToolOutput.Store(true)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			emit := func(event string, data interface{}) error {
				payload, err := json.Marshal(data)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				return err
			}
			toolKinds := map[string]string{}
			for _, tool := range request.Tools {
				toolKinds[tool.Name] = tool.Type
			}
			translator := newResponsesStreamTranslator(emit, &request, toolKinds)
			var chunk openaiStreamChunk
			if calls.Add(1) == 1 {
				_ = json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_hanzo","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"printf HANZO_CODEX_TOOL_OK\",\"yield_time_ms\":10000,\"max_output_tokens\":2000}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}`), &chunk)
			} else {
				_ = json.Unmarshal([]byte(`{"choices":[{"delta":{"content":"HANZO_CODEX_TOOL_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`), &chunk)
			}
			_ = translator.handleChunk(&chunk)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(
		"codex",
		"--dangerously-bypass-approvals-and-sandbox",
		"-c", `model_provider=hanzo_test`,
		"-c", `model_providers.hanzo_test.name="Hanzo Test"`,
		"-c", `model_providers.hanzo_test.base_url="`+server.URL+`/v1"`,
		"-c", `model_providers.hanzo_test.env_key="OPENAI_API_KEY"`,
		"-c", `model_providers.hanzo_test.wire_api="responses"`,
		"-c", `features.remote_models=false`,
		"-c", `model_context_window=262144`,
		"-m", "zen4-coder",
		"exec", "--skip-git-repo-check", "--ephemeral", "Use the shell tool, then reply with the exact token it prints.",
	)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY=test-key")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("codex failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "HANZO_CODEX_TOOL_OK") {
		t.Fatalf("Codex did not consume the Responses stream:\n%s", combined)
	}
	if calls.Load() < 2 || !sawToolOutput.Load() {
		t.Fatalf("Codex did not complete the tool loop: calls=%d saw_tool_output=%v\n%s", calls.Load(), sawToolOutput.Load(), combined)
	}
	if strings.Contains(combined, "failed to refresh available models") {
		t.Fatalf("Codex attempted the incompatible remote catalog despite the launcher override:\n%s", combined)
	}
}
