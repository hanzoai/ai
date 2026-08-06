// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package controllers

import (
	"testing"
)

// TestBrandFromHost proves the Host->brand resolution mirrors the console
// (brandFromHost) and defaults to hanzo for unknown/empty hosts (fail-safe).
func TestBrandFromHost(t *testing.T) {
	cases := map[string]string{
		// hanzo (default + explicit)
		"console.hanzo.ai":  "hanzo",
		"cloud.hanzo.ai":    "hanzo",
		"api.hanzo.ai":      "hanzo",
		"hanzo.id":          "hanzo",
		"hanzo.ai":          "hanzo",
		"CLOUD.HANZO.AI":    "hanzo", // case-insensitive
		"cloud.hanzo.ai:80": "hanzo", // port stripped
		// lux
		"console.lux.cloud": "lux",
		"cloud.lux.network": "lux",
		"lux.cloud":         "lux",
		"lux.id":            "lux",
		// zoo
		"console.zoo.cloud": "zoo",
		"cloud.zoo.network": "zoo",
		"zoo.ngo":           "zoo",
		"zoolabs.id":        "zoo",
		// pars
		"console.pars.cloud": "pars",
		"pars.network":       "pars",
		"pars.id":            "pars",
		// unknown / empty -> hanzo default (fail-safe)
		"":                 "hanzo",
		"evil.example.com": "hanzo",
		"localhost:4000":   "hanzo",
	}
	for host, want := range cases {
		if got := brandFromHost(host); got != want {
			t.Errorf("brandFromHost(%q) = %q, want %q", host, got, want)
		}
	}
}

// TestResolveBrandIAM proves each brand resolves to its own client_id/issuer/org,
// hanzo is the default, and the client_secret is read from the brand's env var.
func TestResolveBrandIAM(t *testing.T) {
	// Per-brand secrets from env (KMS-synced in prod).
	t.Setenv("IAM_CLIENT_SECRET", "hanzo-secret")
	t.Setenv("LUX_CLOUD_CLIENT_SECRET", "lux-secret")
	t.Setenv("ZOO_CLOUD_CLIENT_SECRET", "zoo-secret")
	t.Setenv("PARS_CLOUD_CLIENT_SECRET", "")
	// Ensure the hanzo default is not overridden by a stray env in this process.
	t.Setenv("IAM_CLIENT_ID", "")
	t.Setenv("IAM_APP_NAME", "")
	t.Setenv("IAM_ORG", "")
	t.Setenv("IAM_URL", "")

	type want struct {
		clientID, issuer, org, secret string
	}
	cases := map[string]want{
		"cloud.hanzo.ai":     {"hanzo-cloud", "https://hanzo.id", "hanzo", "hanzo-secret"},
		"console.lux.cloud":  {"lux-cloud", "https://lux.id", "lux", "lux-secret"},
		"console.zoo.cloud":  {"zoo-cloud", "https://zoolabs.id", "zoo", "zoo-secret"},
		"console.pars.cloud": {"pars-cloud", "https://pars.id", "pars", ""},
		"unknown.example":    {"hanzo-cloud", "https://hanzo.id", "hanzo", "hanzo-secret"}, // default
	}
	for host, w := range cases {
		b := resolveBrandIAM(host)
		if b.ClientID != w.clientID {
			t.Errorf("%s: clientID=%q want %q", host, b.ClientID, w.clientID)
		}
		if b.Issuer != w.issuer {
			t.Errorf("%s: issuer=%q want %q", host, b.Issuer, w.issuer)
		}
		if b.Org != w.org {
			t.Errorf("%s: org=%q want %q", host, b.Org, w.org)
		}
		if b.ClientSecret != w.secret {
			t.Errorf("%s: secret=%q want %q", host, b.ClientSecret, w.secret)
		}
		// Endpoint falls back to the issuer host when IAM_URL is unset.
		if b.Endpoint != w.issuer {
			t.Errorf("%s: endpoint=%q want %q (issuer fallback)", host, b.Endpoint, w.issuer)
		}
	}

	// IAM_URL wins as the endpoint for every brand (the in-cluster Casdoor).
	t.Setenv("IAM_URL", "http://iam.hanzo.svc")
	if got := resolveBrandIAM("console.lux.cloud").Endpoint; got != "http://iam.hanzo.svc" {
		t.Errorf("lux endpoint with IAM_URL set = %q, want http://iam.hanzo.svc", got)
	}
}

// TestResolveBrandIAM_HanzoEnvPin proves a single-tenant hanzo deployment can still
// PIN the client/org via the legacy IAM_CLIENT_ID/IAM_APP_NAME/IAM_ORG env (the
// exact values InitAuthConfig read), so hanzo stays byte-unchanged.
func TestResolveBrandIAM_HanzoEnvPin(t *testing.T) {
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_ORG", "hanzo")
	t.Setenv("IAM_CLIENT_SECRET", "s3cr3t")
	b := resolveBrandIAM("cloud.hanzo.ai")
	if b.ClientID != "hanzo-cloud" || b.Org != "hanzo" || b.ClientSecret != "s3cr3t" {
		t.Errorf("hanzo env pin: got clientID=%q org=%q secret=%q", b.ClientID, b.Org, b.ClientSecret)
	}
	// A lux host is NOT affected by the hanzo env pin.
	t.Setenv("LUX_CLOUD_CLIENT_SECRET", "lux-secret")
	l := resolveBrandIAM("console.lux.cloud")
	if l.ClientID != "lux-cloud" || l.Org != "lux" {
		t.Errorf("lux must not inherit hanzo env pin: got clientID=%q org=%q", l.ClientID, l.Org)
	}
}

// TestBrandAuthClient_FailClosed proves a brand with NO provisioned secret fails
// closed with a clear error and NEVER falls back to another brand's client -- the
// exact "wrong application" bug this closes. hanzo (default) reuses the package
// global.
func TestBrandAuthClient_FailClosed(t *testing.T) {
	t.Setenv("PARS_CLOUD_CLIENT_SECRET", "")
	if _, err := brandAuthClient(resolveBrandIAM("console.pars.cloud")); err == nil {
		t.Fatal("pars with no secret must fail closed, got nil error")
	}
	// hanzo default returns the package-global adapter (no per-brand secret needed
	// here; the global was set by InitAuthConfig).
	if c, err := brandAuthClient(resolveBrandIAM("cloud.hanzo.ai")); err != nil || c == nil {
		t.Fatalf("hanzo default must return the global client, got client=%v err=%v", c, err)
	}
	// A configured lux brand returns a non-nil per-brand client.
	t.Setenv("LUX_CLOUD_CLIENT_SECRET", "lux-secret")
	if c, err := brandAuthClient(resolveBrandIAM("console.lux.cloud")); err != nil || c == nil {
		t.Fatalf("configured lux must return a client, got client=%v err=%v", c, err)
	}
}

// TestBrandIssuers proves the issuer set derived from brandDefs covers all brands.
func TestBrandIssuers(t *testing.T) {
	got := brandIssuers()
	want := map[string]bool{"https://hanzo.id": true, "https://lux.id": true, "https://zoolabs.id": true, "https://pars.id": true}
	if len(got) != len(want) {
		t.Fatalf("brandIssuers()=%v, want %d entries", got, len(want))
	}
	for _, iss := range got {
		if !want[iss] {
			t.Errorf("unexpected issuer %q", iss)
		}
	}
}
