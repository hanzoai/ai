package object

import (
	"strings"
	"testing"
)

// A denial names the wallet of the brand the caller is USING, not Hanzo's.
func TestPayURLFollowsTheBrandHost(t *testing.T) {
	for _, c := range []struct{ host, want string }{
		{"api.hanzo.ai", "https://pay.hanzo.ai"},
		{"api.lux.cloud", "https://pay.lux.cloud"},
		{"lux.cloud", "https://pay.lux.cloud"},
		{"api.zoo.cloud", "https://pay.zoo.cloud"},
		{"console.zoo.ngo:443", "https://pay.zoo.cloud"},
		{"API.LUX.CLOUD", "https://pay.lux.cloud"},
		{"api.pars.network", "https://pay.hanzo.ai"}, // pars has no wallet host; fall back
		{"", "https://pay.hanzo.ai"},                 // ZAP wire carries no Host
	} {
		if got := PayURL(c.host, "acme"); got != c.want {
			t.Errorf("PayURL(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

func TestDenialEmbedsTheBrandsWallet(t *testing.T) {
	n := InsufficientBalance("api.lux.cloud", "acme", "request cost")
	if !strings.Contains(n.Message, "https://pay.lux.cloud") {
		t.Fatalf("lux denial named the wrong wallet: %s", n.Message)
	}
	if strings.Contains(n.Message, "pay.hanzo.ai") {
		t.Fatalf("cross-brand leak: %s", n.Message)
	}
}
