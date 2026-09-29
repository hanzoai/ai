// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/hanzoai/account"
	"github.com/hanzoai/ai/internal/authtest"
	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/luxfi/zap"
)

// cloudCall builds a MsgType-100 native cloud request: method, auth, body.
func cloudCall(t *testing.T, method, auth, body string) *zap.Message {
	t.Helper()
	b := zap.NewBuilder(len(auth) + len(body) + 256)
	obj := b.StartObject(24)
	obj.SetText(object.CloudReqMethod, method)
	obj.SetText(object.CloudReqAuth, auth)
	obj.SetBytes(object.CloudReqBody, []byte(body))
	obj.FinishAsRoot()
	msg, err := zap.Parse(b.FinishWithFlags(object.MsgTypeCloud << 8))
	if err != nil {
		t.Fatalf("build cloud request: %v", err)
	}
	return msg
}

// gatewayGet builds a MsgType-200 gateway request carrying headers, body and a
// query string: method(0) path(8) headers(16) body(24) query(32).
func gatewayGet(t *testing.T, path, query string, headers map[string]string, body string) *zap.Message {
	t.Helper()
	h, _ := json.Marshal(headers)
	b := zap.NewBuilder(len(body) + len(h) + len(query) + 256)
	obj := b.StartObject(40)
	obj.SetText(0, http.MethodGet)
	obj.SetText(8, path)
	obj.SetBytes(16, h)
	obj.SetBytes(24, []byte(body))
	obj.SetText(32, query)
	obj.FinishAsRoot()
	msg, err := zap.Parse(b.FinishWithFlags(object.MsgTypeHTTPRequest << 8))
	if err != nil {
		t.Fatalf("build gateway request: %v", err)
	}
	return msg
}

// A balance answers the caller's own payer, whatever the request names. Both ways
// in (native "balance" and the gateway's /v1/balance) read the ledger once, as the
// caller, and the answer names the caller's subject. The signup org is the case
// where two members of one org hold separate wallets.
func TestBalanceIsTheCallersOwn(t *testing.T) {
	type read struct{ subject, namespace string }
	var mu sync.Mutex
	var reads []read
	object.SetBalanceReader(func(_ context.Context, subject, namespace, _ string) (int64, error) {
		mu.Lock()
		reads = append(reads, read{subject, namespace})
		mu.Unlock()
		return 1200, nil
	})
	t.Cleanup(func() { object.SetBalanceReader(nil) })

	for _, who := range []struct{ caller, target iam.User }{
		{iam.User{Owner: "acme", Name: "alice"}, iam.User{Owner: "victim", Name: "bob"}},
		{iam.User{Owner: account.SignupOrg, Name: "alice"}, iam.User{Owner: account.SignupOrg, Name: "bob"}},
	} {
		auth := authtest.Bearer(t, who.caller)
		own := account.Payer(account.Credential{Owner: who.caller.Owner, Name: who.caller.Name}).Subject()
		target := who.target.Owner + "/" + who.target.Name
		theirs := account.Payer(account.Credential{Owner: who.target.Owner, Name: who.target.Name}).Subject()
		if own == theirs {
			t.Fatalf("%s and %s share payer %q; the case proves nothing", who.caller.Name, target, own)
		}
		body := `{"user":"` + target + `"}`

		cases := []struct {
			name string
			call func() (*zap.Message, error)
		}{
			{"native balance", func() (*zap.Message, error) {
				return handleCloudService(context.Background(), "peer", cloudCall(t, "balance", auth, body))
			}},
			{"gateway /v1/balance", func() (*zap.Message, error) {
				return gateway(nil)(context.Background(), "gw", gatewayGet(t, "/v1/balance",
					url.Values{"user": {target}}.Encode(),
					map[string]string{"Authorization": auth, "X-User-Id": target, "X-Org-Id": who.target.Owner},
					body))
			}},
		}
		for _, tc := range cases {
			mu.Lock()
			reads = nil
			mu.Unlock()

			msg, err := tc.call()
			if err != nil {
				t.Fatalf("%s as %s: %v", tc.name, own, err)
			}
			status, data, errText := antDecodeCloud(t, msg)
			if status != 200 {
				t.Fatalf("%s as %s: status %d (%s), want 200", tc.name, own, status, errText)
			}
			var got struct {
				User string `json:"user"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("%s as %s: decode %q: %v", tc.name, own, data, err)
			}
			if got.User != own {
				t.Errorf("%s as %s naming %s: answered user=%q, want %q", tc.name, own, target, got.User, own)
			}
			mu.Lock()
			if len(reads) != 1 || reads[0] != (read{own, who.caller.Owner}) {
				t.Errorf("%s as %s naming %s: ledger reads %+v, want exactly [{%s %s}]",
					tc.name, own, target, reads, own, who.caller.Owner)
			}
			mu.Unlock()
		}
	}
}
