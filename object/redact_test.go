// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"encoding/json"
	"strings"
	"testing"

	iam "github.com/hanzoai/iam-v1"
)

// TestRedactUserSecrets asserts every credential field is zeroed and that none
// of the secret VALUES survive JSON serialization to a client.
func TestRedactUserSecrets(t *testing.T) {
	u := &iam.User{
		Owner:                "hanzo",
		Name:                 "z",
		Password:             "argon2id$hashbits",
		PasswordSalt:         "saltsaltsalt",
		PasswordType:         "argon2id",
		AccessKey:            "hk-accesskey",
		AccessSecret:         "accesssecretvalue",
		AccessToken:          "access.token.jwt",
		OriginalToken:        "orig.token.jwt",
		OriginalRefreshToken: "orig.refresh.jwt",
		TotpSecret:           "TOTPSEED1234",
		RecoveryCodes:        []string{"rc-1", "rc-2"},
		MultiFactorAuths: []*iam.MfaProps{
			{Secret: "mfa-secret", RecoveryCodes: []string{"mrc-1"}},
		},
	}

	RedactUserSecrets(u)

	// Field-level: each secret must be empty.
	checks := map[string]string{
		"Password": u.Password, "PasswordSalt": u.PasswordSalt, "PasswordType": u.PasswordType,
		"AccessKey": u.AccessKey, "AccessSecret": u.AccessSecret,
		"AccessToken": u.AccessToken, "OriginalToken": u.OriginalToken,
		"OriginalRefreshToken": u.OriginalRefreshToken, "TotpSecret": u.TotpSecret,
	}
	for field, val := range checks {
		if val != "" {
			t.Errorf("%s not redacted: %q", field, val)
		}
	}
	if u.RecoveryCodes != nil {
		t.Errorf("RecoveryCodes not redacted: %v", u.RecoveryCodes)
	}
	if len(u.MultiFactorAuths) > 0 && (u.MultiFactorAuths[0].Secret != "" || u.MultiFactorAuths[0].RecoveryCodes != nil) {
		t.Errorf("MFA secret/recovery not redacted: %+v", u.MultiFactorAuths[0])
	}

	// Identity fields must survive.
	if u.Owner != "hanzo" || u.Name != "z" {
		t.Errorf("identity fields clobbered: owner=%q name=%q", u.Owner, u.Name)
	}

	// Serialization-level: no secret VALUE may appear anywhere in the JSON.
	blob, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{
		"argon2id$hashbits", "saltsaltsalt", "hk-accesskey",
		"accesssecretvalue", "access.token.jwt", "orig.token.jwt",
		"orig.refresh.jwt", "TOTPSEED1234", "rc-1", "rc-2", "mfa-secret", "mrc-1",
	} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("redacted JSON still leaks secret %q", secret)
		}
	}
}

// TestRedactUserSecretsAllowlistFailSecure proves the allowlist projection: any
// string/[]string field NOT in exposableUserFields is zeroed (so a NEW iam.User
// secret field is fail-secure and never leaks), allowlisted display fields survive
// (so the cloud account UI is unbroken), and credential-bearing struct slices the
// string sweep cannot reach are nilled (closing the prior denylist gap on
// MfaAccounts/ManagedAccounts).
func TestRedactUserSecretsAllowlistFailSecure(t *testing.T) {
	u := &iam.User{
		Owner: "hanzo", Name: "z",
		// Allowlisted display fields the cloud account UI reads.
		DisplayName: "Z User", Email: "z@hanzo.ai", CreatedIp: "1.2.3.4", Tag: "vip",
		// NON-allowlisted string fields: existing/secret/link/internal — must be zeroed.
		GitHub: "ghuser", Custom: "customval", Hash: "sessionhash", PreHash: "preh",
		IdCard: "ID123", Ldap: "cn=z", AccessSecret: "sekret", TotpSecret: "TOTP",
		Google: "g-oauth-id", Custom3: "c3", IpWhitelist: "10.0.0.0/8",
		// Credential-bearing struct slices (string sweep can't reach these).
		ManagedAccounts: []iam.ManagedAccount{{}},
		MfaAccounts:     []iam.MfaAccount{{}},
		MfaItems:        []*iam.MfaItem{{}},
		FaceIds:         []*iam.FaceId{{}},
	}

	RedactUserSecrets(u)

	// Allowlisted display fields survive.
	if u.Owner != "hanzo" || u.Name != "z" || u.DisplayName != "Z User" || u.Email != "z@hanzo.ai" || u.CreatedIp != "1.2.3.4" || u.Tag != "vip" {
		t.Errorf("allowlisted display fields must survive: %+v", u)
	}

	// Non-allowlisted string fields are zeroed (fail-secure).
	zeroed := map[string]string{
		"GitHub": u.GitHub, "Custom": u.Custom, "Custom3": u.Custom3, "Hash": u.Hash,
		"PreHash": u.PreHash, "IdCard": u.IdCard, "Ldap": u.Ldap, "Google": u.Google,
		"AccessSecret": u.AccessSecret, "TotpSecret": u.TotpSecret, "IpWhitelist": u.IpWhitelist,
	}
	for name, val := range zeroed {
		if val != "" {
			t.Errorf("non-allowlisted field %s must be zeroed (fail-secure), got %q", name, val)
		}
	}

	// Credential-bearing struct slices are nilled — closes the denylist gap where
	// MfaAccount.SecretKey / ManagedAccount.Password would have leaked.
	if u.ManagedAccounts != nil || u.MfaAccounts != nil || u.MfaItems != nil || u.FaceIds != nil {
		t.Errorf("credential struct slices must be nilled: managed=%v mfaAcct=%v mfaItems=%v faceIds=%v",
			u.ManagedAccounts, u.MfaAccounts, u.MfaItems, u.FaceIds)
	}
}

// TestRedactClaimsSecrets asserts the claims-level access token is scrubbed too.
func TestRedactClaimsSecrets(t *testing.T) {
	c := &iam.Claims{AccessToken: "claims.access.token"}
	c.User = iam.User{Password: "hash", AccessToken: "user.access.token"}
	RedactClaimsSecrets(c)
	if c.AccessToken != "" {
		t.Errorf("claims.AccessToken not redacted: %q", c.AccessToken)
	}
	if c.User.Password != "" || c.User.AccessToken != "" {
		t.Errorf("embedded user not redacted: %+v", c.User)
	}
}

// TestRedactNilSafe ensures the redactors never panic on nil input.
func TestRedactNilSafe(t *testing.T) {
	RedactUserSecrets(nil)
	RedactClaimsSecrets(nil)
}
