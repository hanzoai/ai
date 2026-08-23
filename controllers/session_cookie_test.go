// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"strings"
	"testing"
	"time"
)

// setCookie drives one of the cookie writers and returns the Set-Cookie the
// browser would receive.
func setCookie(t *testing.T, write func(*ApiController)) string {
	t.Helper()
	c := visit("GET", "/v1/ai/get-account")
	write(c)
	got := string(c.Fiber().Response().Header.Peek("Set-Cookie"))
	if got == "" {
		t.Fatal("no Set-Cookie was written")
	}
	return got
}

// The cookie carries a verified access token, so what protects it is its
// attributes and nothing else.
//
// Each of these is load-bearing on its own: without HttpOnly a script that gets
// onto the page can read the token; without Secure the browser will send it over
// plaintext; SameSite=Lax is what keeps a third-party page from spending it; and
// setting no Domain keeps it host-only, so a sibling subdomain never sees it.
//
// None of them was asserted. Any one could be dropped in an edit that looked
// like tidying, and nothing would have said so.
func TestTheSessionCookieIsProtectedByItsAttributes(t *testing.T) {
	got := setCookie(t, func(c *ApiController) {
		c.setIamTokenCookie("a-verified-token", time.Now().Add(time.Hour))
	})

	for _, want := range []string{"HttpOnly", "secure", "SameSite=Lax", "path=/"} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("Set-Cookie = %q, want it to carry %s", got, want)
		}
	}
	// Host-only: naming a domain would widen it to every sibling subdomain.
	if strings.Contains(strings.ToLower(got), "domain=") {
		t.Errorf("Set-Cookie names a domain, which widens it beyond this host: %q", got)
	}
	if !strings.Contains(got, iamTokenCookieName+"=a-verified-token") {
		t.Errorf("Set-Cookie = %q, want it to carry the token under %q", got, iamTokenCookieName)
	}
}

// The cookie lives as long as the token it carries, and no longer.
func TestTheCookieOutlivesNothing(t *testing.T) {
	t.Run("a future expiry becomes the cookie's own", func(t *testing.T) {
		got := setCookie(t, func(c *ApiController) {
			c.setIamTokenCookie("t", time.Now().Add(2*time.Hour))
		})
		if !strings.Contains(strings.ToLower(got), "max-age=") {
			t.Errorf("Set-Cookie = %q, want a max-age from the token's expiry", got)
		}
	})

	t.Run("no expiry is a session cookie", func(t *testing.T) {
		got := setCookie(t, func(c *ApiController) {
			c.setIamTokenCookie("t", time.Time{})
		})
		if strings.Contains(strings.ToLower(got), "max-age=") {
			t.Errorf("Set-Cookie = %q, want no max-age so it ends with the browser", got)
		}
	})

	t.Run("an expiry already past does not become a lifetime", func(t *testing.T) {
		got := setCookie(t, func(c *ApiController) {
			c.setIamTokenCookie("t", time.Now().Add(-time.Hour))
		})
		if strings.Contains(strings.ToLower(got), "max-age=-") {
			t.Errorf("Set-Cookie = %q, a past expiry must not become a negative age", got)
		}
	})
}

// Clearing is half of signing out, and it must not be a way to put a weaker
// cookie in place of the strong one.
func TestClearingKeepsEveryProtectionItHad(t *testing.T) {
	got := setCookie(t, func(c *ApiController) { c.clearIamTokenCookie() })

	for _, want := range []string{"HttpOnly", "secure", "SameSite=Lax"} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("the cleared cookie = %q, want it to keep %s", got, want)
		}
	}
	if !strings.Contains(got, iamTokenCookieName+"=") {
		t.Errorf("the cleared cookie = %q, want it to name %q", got, iamTokenCookieName)
	}
	// Emptied and expired, so the browser stops presenting it at once.
	if strings.Contains(got, iamTokenCookieName+"=a") {
		t.Errorf("the cleared cookie still carries a value: %q", got)
	}
}
