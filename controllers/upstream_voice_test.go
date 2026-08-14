package controllers

import (
	"strings"
	"testing"
)

// Every upstream failure in this package — chat, media, audio, video, transcript
// and messages — reaches a caller through upstreamErrorMessage. A served answer
// does not say which upstream produced it; a refusal carries the same obligation.

// The shape a provider writes for its own billing: it says who they are and links
// their console, and its remedy is one the caller cannot perform.
func TestABillingRefusalNamesNoProvider(t *testing.T) {
	body := []byte(`{"error":{"message":"Insufficient credits. Add more using https://openrouter.ai/settings/credits",` +
		`"code":402,"metadata":{"limit_source":"openrouter_credits"}}}`)
	got := strings.ToLower(upstreamErrorMessage(body))
	for _, leak := range []string{"openrouter", "http"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%q survived: %q", leak, got)
		}
	}
}

func TestNoVendorNameOrLinkSurvives(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"You exceeded your quota, check your plan at https://platform.openai.com/account"}}`,
		`{"error":{"message":"anthropic: overloaded_error"}}`,
		`{"error":"together.ai is at capacity"}`,
		`{"detail":"see https://console.groq.com/settings/billing"}`,
		`{"message":"fireworks account suspended"}`,
	} {
		got := strings.ToLower(upstreamErrorMessage([]byte(body)))
		for _, leak := range []string{"openai", "anthropic", "together", "groq", "fireworks", "http"} {
			if strings.Contains(got, leak) {
				t.Fatalf("%q leaked from %s -> %q", leak, body, got)
			}
		}
	}
}

// A complaint about the REQUEST is the caller's to act on, so it keeps its words
// in every shape upstreams write. Dropping these would make failures opaque.
func TestARequestComplaintKeepsItsWordsInEveryShape(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"error":{"message":"max_tokens must be a positive integer"}}`, "max_tokens must be a positive integer"},
		{`{"error":"context length exceeded"}`, "context length exceeded"},
		{`{"message":"model is overloaded"}`, "model is overloaded"},
		{`{"detail":"invalid role: assistant2"}`, "invalid role: assistant2"},
	} {
		if got := upstreamErrorMessage([]byte(tc.body)); got != tc.want {
			t.Fatalf("actionable message changed: got %q, want %q", got, tc.want)
		}
	}
}

// The raw body is never the message — it is the upstream's envelope, not a
// sentence, and it carries fields no answer of ours discloses.
func TestTheRawBodyIsNeverTheMessage(t *testing.T) {
	for _, body := range []string{
		`{"limit_source":"openrouter_credits","remedy_hint":"Add credits at https://openrouter.ai"}`,
		`<html><body>502 Bad Gateway</body></html>`,
		`not json at all`,
		``,
	} {
		got := upstreamErrorMessage([]byte(body))
		if strings.Contains(got, "{") || strings.Contains(got, "<") || strings.Contains(got, "openrouter") {
			t.Fatalf("raw body surfaced for %q -> %q", body, got)
		}
		if strings.TrimSpace(got) == "" {
			t.Fatalf("empty message for %q — a caller gets no sentence", body)
		}
	}
}
