package controllers

import (
	"strings"
	"testing"
)

// An upstream refusal often quotes the key back, and such a message carries no
// vendor name and no URL — so the vendor check that gates repeating it says yes.
// What reaches the caller must still carry no credential.
func TestARelayedUpstreamMessageCarriesNoKey(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"Incorrect API key provided: sk-proj-abcdefghijklmnopqrstuvwxyz012345"}}`,
		`{"error":"invalid key sk-or-v1-9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c"}`,
		`{"message":"bad token hf_abcdefghijklmnopqrstuvwxyzABCD"}`,
	} {
		got := upstreamErrorMessage([]byte(body))
		for _, frag := range []string{"sk-proj-abc", "sk-or-v1-9f8", "hf_abcdef"} {
			if strings.Contains(got, frag) {
				t.Fatalf("a key reached the caller: %q", got)
			}
		}
	}
}

// The paired control: an ordinary refusal is still repeated, so the caller learns
// why the call failed rather than getting a blank.
func TestAnOrdinaryUpstreamMessageIsStillRepeated(t *testing.T) {
	got := upstreamErrorMessage([]byte(`{"error":{"message":"rate limit exceeded, retry after 20 seconds"}}`))
	if got != "rate limit exceeded, retry after 20 seconds" {
		t.Fatalf("an ordinary message was not repeated: %q", got)
	}
}
