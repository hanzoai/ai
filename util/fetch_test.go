// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package util

import "testing"

func TestAnAddressARequestNamed(t *testing.T) {
	for _, raw := range []string{
		"/etc/passwd", "../../secrets.yaml", "etc/shadow",
		"file:///etc/passwd", "gopher://x/", "ftp://host/f",
		"http://127.0.0.1/", "https://localhost/", "http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/", "http://192.168.1.1/", "http://172.16.0.1/",
		"http://[::1]/", "http://0.0.0.0/", "http://",
	} {
		if err := Fetchable(raw); err == nil {
			t.Errorf("Fetchable(%q) allowed it", raw)
		}
	}
	for _, raw := range []string{
		"https://example.com/a.pdf", "http://example.com/doc.txt",
		"https://example.com:8443/x?y=1",
	} {
		if err := Fetchable(raw); err != nil {
			t.Errorf("Fetchable(%q) refused a public address: %v", raw, err)
		}
	}
}
