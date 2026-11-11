package model

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestValidateUpstreamResponseDetectsSilentFailuresAndSubstitutions(t *testing.T) {
	aliases := map[string]string{
		"zen-latest": "zen-1.5-pro",
	}

	// 1. Valid canonical response
	okRes := UpstreamAttemptResult{
		Provider:     "hanzo-primary",
		ServedModel:  "zen-1.5-pro",
		StatusCode:   http.StatusOK,
		Body:         `{"id":"chatcmpl-1","model":"zen-1.5-pro"}`,
		Content:      "Verified response",
		FinishReason: "stop",
		StreamDone:   true,
		Latency:      45 * time.Millisecond,
	}
	if err := ValidateUpstreamResponse("zen-latest", aliases, okRes); err != nil {
		t.Fatalf("expected valid response, got error: %v", err)
	}

	// 2. Silent HTTP 200 with empty content
	emptyRes := okRes
	emptyRes.Content = "   "
	if err := ValidateUpstreamResponse("zen-latest", aliases, emptyRes); err == nil {
		t.Fatal("expected error for silent HTTP 200 empty content")
	}

	// 3. HTML error page masquerading as 200 OK
	htmlRes := okRes
	htmlRes.Body = "<!DOCTYPE html><html><body>502 Bad Gateway</body></html>"
	if err := ValidateUpstreamResponse("zen-latest", aliases, htmlRes); err == nil {
		t.Fatal("expected error for HTML body masquerading as 200 OK")
	}

	// 4. Undisclosed model substitution
	subRes := okRes
	subRes.ServedModel = "gpt-3.5-turbo"
	if err := ValidateUpstreamResponse("zen-latest", aliases, subRes); err == nil {
		t.Fatal("expected error for undisclosed model substitution")
	}
}

func TestExecuteWithFailoverPreservesRequestParameters(t *testing.T) {
	aliases := map[string]string{"claude-3-7-sonnet": "claude-3-7-sonnet-20250219"}
	req := RoutingRequest{
		Model:          "claude-3-7-sonnet",
		SystemPrompt:   "Return JSON only.",
		Tools:          []map[string]any{{"type": "function", "function": map[string]any{"name": "get_quote"}}},
		ResponseFormat: map[string]any{"type": "json_object"},
		Stream:         true,
	}

	chain := []string{"provider-timeout", "provider-429", "provider-silent-empty", "provider-healthy"}
	res, trace, err := ExecuteWithFailover(req, aliases, chain, func(provider string, r RoutingRequest) (UpstreamAttemptResult, error) {
		if len(r.Tools) != 1 || r.ResponseFormat["type"] != "json_object" || !r.Stream || r.SystemPrompt == "" {
			t.Fatalf("failover lost request parameters on provider %s", provider)
		}
		switch provider {
		case "provider-timeout":
			return UpstreamAttemptResult{}, errors.New("context_deadline_exceeded")
		case "provider-429":
			return UpstreamAttemptResult{Provider: provider, StatusCode: http.StatusTooManyRequests}, nil
		case "provider-silent-empty":
			return UpstreamAttemptResult{
				Provider:     provider,
				ServedModel:  "claude-3-7-sonnet-20250219",
				StatusCode:   http.StatusOK,
				Body:         `{}`,
				Content:      "",
				FinishReason: "stop",
				StreamDone:   true,
			}, nil
		default:
			return UpstreamAttemptResult{
				Provider:     provider,
				ServedModel:  "claude-3-7-sonnet-20250219",
				StatusCode:   http.StatusOK,
				Body:         `{"id":"msg_01"}`,
				Content:      `{"status":"ok"}`,
				FinishReason: "stop",
				StreamDone:   true,
			}, nil
		}
	})

	if err != nil {
		t.Fatalf("expected failover to succeed on healthy provider, got: %v", err)
	}
	if res.Provider != "provider-healthy" || len(trace) != 4 {
		t.Fatalf("unexpected failover trace: %+v", trace)
	}
}
