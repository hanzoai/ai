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
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/luxfi/zap"
)

// The cloud-ops message carries an auth field. Undeploy deletes an entire
// namespace, so an unnamed caller does not get one, and a named caller reaches
// only their own organization's application.
func TestCloudOpsNeedsAPrincipalItReaches(t *testing.T) {
	withStore(t)
	people := withIAM(t)

	if _, err := object.AddApplication(&object.Application{
		Owner: "victim", Name: "app", Namespace: "victim-prod",
	}); err != nil {
		t.Fatal(err)
	}

	ask := func(auth string) uint32 {
		t.Helper()
		body := []byte(`{"owner":"victim","name":"app","namespace":"victim-prod"}`)
		b := zap.NewBuilder(len(body) + len(auth) + 128)
		obj := b.StartObject(20)
		obj.SetText(object.CloudReqMethod, "undeploy")
		obj.SetText(object.CloudReqAuth, auth)
		obj.SetBytes(object.CloudReqBody, body)
		obj.FinishAsRoot()
		msg, err := zap.Parse(b.FinishWithFlags(MsgTypeCloudOps << 8))
		if err != nil {
			t.Fatal(err)
		}
		out, err := handleCloudOps(context.Background(), "peer", msg)
		if err != nil {
			t.Fatal(err)
		}
		return out.Root().Uint32(object.CloudRespStatus)
	}

	if got := ask(""); got != 401 {
		t.Errorf("a caller with no credential got %d, want 401", got)
	}

	mallory := people.signedIn(t, &iam.User{Owner: "acme", Name: "mallory", IsAdmin: true})
	if got := ask(mallory); got != 404 {
		t.Errorf("another organization's admin got %d, want 404", got)
	}
}
