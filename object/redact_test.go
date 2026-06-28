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

	iam "github.com/hanzoai/iam"
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
