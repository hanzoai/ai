// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/util"
)

func loaded() *Provider {
	return &Provider{
		Owner: "admin", Name: "openai", Category: "Model", Type: "OpenAI",
		ClientSecret: "sk-tenant-secret",
		ProviderKey:  "sk-platform-upstream",
		UserKey:      "uk-live",
		ConfigText:   "{\"key\":\"live\"}",
		SignKey:      "sign-live",
	}
}

// Four of these fields are what buys a call from an upstream vendor: whoever
// reads one can spend it directly, off our meter. So the predicate that unmasks
// them is the platform one — membership of the reserved org — and not the tenant
// one every customer's own admin satisfies.
func TestWhoSeesAnUpstreamCredential(t *testing.T) {
	platform := []struct {
		name string
		read func(*Provider) string
	}{
		{"ProviderKey", func(p *Provider) string { return p.ProviderKey }},
		{"UserKey", func(p *Provider) string { return p.UserKey }},
		{"ConfigText", func(p *Provider) string { return p.ConfigText }},
		{"SignKey", func(p *Provider) string { return p.SignKey }},
	}

	// A tenant's own admin — isAdmin of their org, and not the platform.
	for _, user := range []*iam.User{
		nil,
		{Owner: "acme", Name: "dave", IsAdmin: true},
		{Owner: "hanzo", Name: "z", IsAdmin: true},
	} {
		got := GetMaskedProvider(loaded(), user)
		for _, f := range platform {
			if v := f.read(got); v != SecretMask {
				t.Errorf("%+v read %s as %q", user, f.name, v)
			}
		}
		if got.ClientSecret != SecretMask {
			t.Errorf("%+v read ClientSecret as %q", user, got.ClientSecret)
		}
	}

	// The reserved org is the platform.
	sudo := &iam.User{Owner: util.AdminOrg, Name: "z"}
	if !util.IsSuperAdmin(sudo) {
		t.Fatal("the test's own idea of a platform admin is wrong")
	}
	got := GetMaskedProvider(loaded(), sudo)
	for _, f := range platform {
		if v := f.read(got); v == SecretMask {
			t.Errorf("the platform could not read its own %s", f.name)
		}
	}
	// Its own secret is masked for everyone; an edit form sends the mask back and
	// the stored value is restored behind it.
	if got.ClientSecret != SecretMask {
		t.Errorf("ClientSecret came back as %q", got.ClientSecret)
	}
}

// A field that holds nothing is left holding nothing. Masking it would put "***"
// in the request an edit form sends back, and the merge treats that as "unchanged"
// — so a mask over emptiness becomes a literal three asterisks in the row.
func TestMaskingLeavesAnEmptyFieldEmpty(t *testing.T) {
	got := GetMaskedProvider(&Provider{Owner: "admin", Name: "p"}, &iam.User{Owner: "acme"})
	for name, v := range map[string]string{
		"ClientSecret": got.ClientSecret, "ProviderKey": got.ProviderKey,
		"UserKey": got.UserKey, "ConfigText": got.ConfigText, "SignKey": got.SignKey,
	} {
		if v != "" {
			t.Errorf("%s was empty and came back as %q", name, v)
		}
	}
	if GetMaskedProvider(nil, nil) != nil {
		t.Error("masking nothing produced something")
	}
}

// The masked value a form sends back means "unchanged", so the stored secret
// survives an edit that did not touch it.
func TestAnEditThatDidNotTouchTheSecretKeepsIt(t *testing.T) {
	stored := loaded()
	edited := &Provider{
		Owner: "admin", Name: "openai", Category: "Model", Type: "OpenAI",
		DisplayName:  "OpenAI (renamed)",
		ClientSecret: SecretMask,
		UserKey:      SecretMask,
		SignKey:      SecretMask,
	}
	edited.processProviderParams(stored)

	if edited.ClientSecret != stored.ClientSecret {
		t.Errorf("ClientSecret became %q", edited.ClientSecret)
	}
	if edited.UserKey != stored.UserKey {
		t.Errorf("UserKey became %q", edited.UserKey)
	}
	if edited.SignKey != stored.SignKey {
		t.Errorf("SignKey became %q", edited.SignKey)
	}
	if edited.DisplayName != "OpenAI (renamed)" {
		t.Errorf("the edit itself was lost: %q", edited.DisplayName)
	}
}
