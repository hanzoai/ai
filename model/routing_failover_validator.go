package model

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RoutingRequest represents an incoming OpenAI/Anthropic compatible model request
// whose parameters must be preserved across primary routing and fallback execution.
type RoutingRequest struct {
	Model          string                 `json:"model"`
	Messages       []map[string]any       `json:"messages"`
	Tools          []map[string]any       `json:"tools,omitempty"`
	ResponseFormat map[string]any         `json:"response_format,omitempty"`
	SystemPrompt   string                 `json:"system,omitempty"`
	Stream         bool                   `json:"stream,omitempty"`
	Metadata       map[string]string      `json:"metadata,omitempty"`
}

// UpstreamAttemptResult captures an upstream provider response for routing & failover inspection.
type UpstreamAttemptResult struct {
	Provider       string
	ServedModel    string
	StatusCode     int
	ContentType    string
	Body           string
	Content        string
	FinishReason   string
	StreamDone     bool
	Latency        time.Duration
}

// ValidateUpstreamResponse detects silent HTTP 200 failures (empty content, HTML error pages
// masquerading as 200 OK, truncated SSE streams without finish_reason/[DONE], or undisclosed model substitution).
func ValidateUpstreamResponse(requestedModel string, allowedAliases map[string]string, res UpstreamAttemptResult) error {
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		return fmt.Errorf("upstream_retryable_status_%d", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream_non_200_status_%d", res.StatusCode)
	}

	trimmedBody := strings.TrimSpace(res.Body)
	if strings.HasPrefix(strings.ToLower(trimmedBody), "<!doctype html") ||
		strings.HasPrefix(strings.ToLower(trimmedBody), "<html") {
		return errors.New("silent_failure_html_masquerading_as_200_ok")
	}

	if strings.TrimSpace(res.Content) == "" {
		return errors.New("silent_failure_empty_200_content")
	}

	if !res.StreamDone || strings.TrimSpace(res.FinishReason) == "" {
		return errors.New("silent_failure_truncated_stream_missing_finish_reason")
	}

	expectedCanonical := requestedModel
	if canonical, ok := allowedAliases[requestedModel]; ok {
		expectedCanonical = canonical
	}
	if res.ServedModel == "" {
		return errors.New("missing_served_model_metadata")
	}
	if res.ServedModel != requestedModel && res.ServedModel != expectedCanonical {
		return fmt.Errorf("unexpected_model_substitution: requested=%s expected=%s got=%s",
			requestedModel, expectedCanonical, res.ServedModel)
	}

	return nil
}

// ExecuteWithFailover runs a request across an ordered chain of upstream providers,
// failing over automatically on timeouts, 429s, 5xx errors, or silent 200 failures
// while strictly preserving all request parameters (tools, response_format, system prompt, streaming).
func ExecuteWithFailover(
	req RoutingRequest,
	allowedAliases map[string]string,
	chain []string,
	invoker func(provider string, clonedReq RoutingRequest) (UpstreamAttemptResult, error),
) (UpstreamAttemptResult, []string, error) {
	var trace []string
	for _, provider := range chain {
		// Deep-preserve request parameters for each failover attempt
		cloned := RoutingRequest{
			Model:          req.Model,
			Messages:       req.Messages,
			Tools:          req.Tools,
			ResponseFormat: req.ResponseFormat,
			SystemPrompt:   req.SystemPrompt,
			Stream:         req.Stream,
			Metadata:       req.Metadata,
		}
		res, err := invoker(provider, cloned)
		if err != nil {
			trace = append(trace, fmt.Sprintf("%s:error:%v", provider, err))
			continue
		}
		if vErr := ValidateUpstreamResponse(req.Model, allowedAliases, res); vErr != nil {
			trace = append(trace, fmt.Sprintf("%s:invalid:%v", provider, vErr))
			continue
		}
		trace = append(trace, fmt.Sprintf("%s:ok:%s", provider, res.ServedModel))
		return res, trace, nil
	}
	return UpstreamAttemptResult{}, trace, errors.New("all_failover_providers_exhausted")
}
