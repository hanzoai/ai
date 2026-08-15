package model

import "testing"

// A provider client carries an API key to its endpoint. If it accepts any
// certificate, whoever stands between the two ends is handed that key — and the
// call still succeeds, so nothing downstream notices. isLoopbackURL is the whole
// decision: true keeps the self-signed exemption, false verifies.

// The vendors reached across the internet must verify. DigitalOcean, Custom and
// Custom-think all resolve through this one constructor.
func TestAVendorEndpointIsVerified(t *testing.T) {
	for _, url := range []string{
		"https://inference.do-ai.run/v1",
		"https://api.fireworks.ai/inference/v1",
		"https://api.openai.com/v1",
		"https://engine.hanzo.ai/v1",
		"https://openrouter.ai/api/v1",
	} {
		if hc := localHTTPClient(url); hc != nil {
			t.Fatalf("%s would accept ANY certificate — the key goes to whoever presents one", url)
		}
		if getLocalClientFromUrl("sk-secret", url) == nil {
			t.Fatalf("no client for %s", url)
		}
	}
}

// A model server on this machine keeps the exemption: nobody issues a
// certificate for 127.0.0.1, and no attacker can stand in the loopback path.
func TestLoopbackKeepsTheExemption(t *testing.T) {
	for _, url := range []string{
		"https://127.0.0.1:1234/v1",
		"https://localhost:8080/v1",
		"https://[::1]:1234/v1",
		"http://LocalHost:11434/v1",
	} {
		if localHTTPClient(url) == nil {
			t.Fatalf("%s should keep the self-signed exemption", url)
		}
	}
}

// Anything not clearly this machine is remote, so a look-alike host or an
// unparseable URL gets the verifying client rather than the permissive one.
func TestALookalikeHostIsRemote(t *testing.T) {
	for _, url := range []string{
		"", "://bad", "not a url",
		"https://localhost.evil.com/v1",
		"https://127.0.0.1.evil.com/v1",
		"https://10.0.0.5/v1",
		"https://example.com/v1",
	} {
		if localHTTPClient(url) != nil {
			t.Fatalf("%q was treated as this machine — it would skip verification", url)
		}
	}
}
