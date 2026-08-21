package object

import (
	"strings"
	"testing"
)

// An upstream that refuses a call often quotes the key back, in whole or masked,
// with no field name to key on. What is relayed to a caller must carry neither.
func TestAnUpstreamMessageCarriesNoKey(t *testing.T) {
	for _, msg := range []string{
		"Incorrect API key provided: sk-proj-abcdefghijklmnopqrstuvwxyz012345",
		"Invalid API key: sk-or-v1-9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c",
		"authentication_error: sk-ant-api03-AAAABBBBCCCCDDDDEEEEFFFFGGGG",
		"bad token hf_abcdefghijklmnopqrstuvwxyzABCD",
		"key gsk_abcdefghijklmnopqrstuvwxyz0123456789 rejected",
	} {
		got := RedactKeys(msg)
		if got == msg {
			t.Fatalf("nothing was redacted: %s", got)
		}
		for _, frag := range []string{"sk-proj-abc", "sk-or-v1-9f8", "sk-ant-api03-AAAA", "hf_abcdef", "gsk_abcdef"} {
			if strings.Contains(got, frag) {
				t.Fatalf("a key fragment survived: %s", got)
			}
		}
	}
}

// The paired control: prose that merely looks technical is left intact, so the
// caller still learns what went wrong.
func TestOrdinaryMessagesAreLeftAlone(t *testing.T) {
	for _, msg := range []string{
		"rate limit exceeded, retry after 20 seconds",
		"the model sk-1 does not exist",
		"context length exceeded: 200000 tokens",
	} {
		if got := RedactKeys(msg); got != msg {
			t.Fatalf("an ordinary message was altered:\n got %s\nwant %s", got, msg)
		}
	}
}
