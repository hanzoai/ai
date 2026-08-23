// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"strings"
	"testing"
)

// A credential in a URL is already the worst place for one; an audit record makes
// it permanent, and more surfaces read a record than wrote it.
func TestAQueryKeepsItsShapeAndNotItsSecrets(t *testing.T) {
	secret := "sk-ant-abcdefghijklmnopqrstuvwxyz012345"
	for _, param := range []string{
		"accessToken", "access_token", "access-token", "refreshToken", "idToken",
		"api_key", "apiKey", "api-key", "clientSecret", "client_secret",
		"privateKey", "token", "secret", "password", "passwd", "otp", "codeVerifier",
	} {
		raw := "/v1/ai/get-chats?store=main&" + param + "=" + secret + "&p=1"
		got := RedactQuery(raw)
		if strings.Contains(got, secret) {
			t.Errorf("%s survived: %s", param, got)
		}
		// The shape is what an audit trail is for, so the rest must still be there.
		if !strings.Contains(got, "store=main") || !strings.Contains(got, "p=1") {
			t.Errorf("%s took the rest of the query with it: %s", param, got)
		}
		if !strings.Contains(got, param+"=") {
			t.Errorf("%s lost the parameter name, so the record no longer says one was sent: %s", param, got)
		}
	}

	// An ordinary parameter that merely reads like one is left alone.
	if got := RedactQuery("/v1/x?tokenizer=cl100k&p=1"); !strings.Contains(got, "cl100k") {
		t.Errorf("redacted a parameter that is not a credential: %s", got)
	}
}
