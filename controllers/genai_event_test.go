package controllers

import (
	"encoding/json"
	"testing"
	"time"
)

// The LLM Analytics product reads these exact names. A rename here is silent —
// the dashboard keeps rendering, with a column of blanks — so the names are the
// thing worth asserting, not the plumbing around them.
func TestGenAIEventSpeaksTheProductVocabulary(t *testing.T) {
	record := &usageRecord{
		Model:            "claude-opus-5",
		Provider:         "anthropic",
		PromptTokens:     1200,
		CompletionTokens: 340,
		TotalTokens:      1540,
		CacheReadTokens:  800,
		Status:           "success",
		RequestID:        "req-abc",
		Organization:     "hanzo",
		User:             "z",
	}
	props := genAIEventProps(record, 2*time.Second)

	for _, key := range []string{
		"$ai_model", "$ai_provider", "$ai_input_tokens", "$ai_output_tokens",
		"$ai_total_cost_usd", "$ai_latency", "$ai_trace_id", "$ai_is_error",
	} {
		if _, ok := props[key]; !ok {
			t.Errorf("%s missing — the product reads it and would render a blank column", key)
		}
	}
	if props["$ai_model"] != "claude-opus-5" || props["$ai_provider"] != "anthropic" {
		t.Errorf("model/provider not carried: %v / %v", props["$ai_model"], props["$ai_provider"])
	}
	if props["$ai_cache_read_input_tokens"] != 800 {
		t.Errorf("cache reads dropped: %v", props["$ai_cache_read_input_tokens"])
	}
	// Seconds, not milliseconds: the product renders this as a duration.
	if props["$ai_latency"] != 2.0 {
		t.Errorf("latency = %v, want 2 (seconds)", props["$ai_latency"])
	}
	if props["$ai_is_error"] != false {
		t.Errorf("a successful call reported as an error")
	}
}

// Prompts and completions are gated on the span path and must not ride along
// here — an analytics event is a different store under a different retention.
func TestGenAIEventCarriesNoPrompts(t *testing.T) {
	props := genAIEventProps(&usageRecord{Model: "m", Status: "success"}, time.Second)
	for _, key := range []string{"$ai_input", "$ai_output", "$ai_output_choices", "$ai_input_state"} {
		if _, ok := props[key]; ok {
			t.Errorf("%s present — prompt content must not ride the analytics event", key)
		}
	}
}

func TestGenAIEventErrorsAreMarked(t *testing.T) {
	props := genAIEventProps(&usageRecord{Model: "m", Status: "error", ErrorMsg: "upstream 503"}, time.Second)
	if props["$ai_is_error"] != true {
		t.Error("a failed call must be marked, or error rate reads zero forever")
	}
	if props["$ai_error"] != "upstream 503" {
		t.Errorf("error message dropped: %v", props["$ai_error"])
	}
}

// distinct_id decides who the call is attributed to. A machine credential has no
// User, and an event with an empty distinct_id is attributed to nobody.
func TestGenAIEventAlwaysAttributes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record *usageRecord
		want   string
	}{
		{"user wins", &usageRecord{User: "z", Owner: "o", Organization: "hanzo"}, "z"},
		{"owner when no user", &usageRecord{Owner: "o", Organization: "hanzo"}, "o"},
		{"org as the last resort", &usageRecord{Organization: "hanzo"}, "hanzo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := genAIEventBody("hi_key", tc.record, time.Now(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Event      string `json:"event"`
				DistinctID string `json:"distinct_id"`
				APIKey     string `json:"api_key"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body.DistinctID != tc.want {
				t.Errorf("distinct_id = %q, want %q", body.DistinctID, tc.want)
			}
			if body.Event != "$ai_generation" {
				t.Errorf("event = %q — the product only reads $ai_generation", body.Event)
			}
			if body.APIKey != "hi_key" {
				t.Errorf("api_key = %q", body.APIKey)
			}
		})
	}
}

// Unconfigured must be silent rather than posting to nowhere on every LLM call.
func TestGenAIEventOffWhenUnconfigured(t *testing.T) {
	t.Setenv("INSIGHTS_INGEST_URL", "")
	t.Setenv("INSIGHTS_INGEST_KEY", "")
	url, _ := insightsIngest()
	if url != "" {
		t.Errorf("resolved a destination from nothing: %q", url)
	}
	emitGenAIEvent(t.Context(), &usageRecord{Model: "m"}, time.Now()) // must not panic
}
