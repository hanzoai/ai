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
	"testing"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

// decodeCloudApp pulls the (status, envelope) out of a native ZAP cloud response.
func decodeCloudApp(t *testing.T, msg *zap.Message, err error) (uint32, Response) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned err: %v (a ZAP handler encodes policy denials in the message, never as err)", err)
	}
	status := msg.Root().Uint32(object.CloudRespStatus)
	var env Response
	if body := msg.Root().Bytes(object.CloudRespBody); len(body) > 0 {
		if e := json.Unmarshal(body, &env); e != nil {
			t.Fatalf("decode envelope: %v (body=%s)", e, string(body))
		}
	}
	return status, env
}

// TestZapApplicationDeployRegistered proves the group exposes every native cloud
// method and gateway path from its own dispatch tables (the strangler wiring
// seam Integrate ranges over), and that a route's cloud + gateway entry point at
// the SAME handler.
func TestZapApplicationDeployRegistered(t *testing.T) {
	cloud := []string{
		"application.list", "application.get", "application.update",
		"application.add", "application.delete", "application.deploy",
		"application.undeploy", "k8s.status",
	}
	for _, m := range cloud {
		if zapApplicationDeployCloud[m] == nil {
			t.Errorf("cloud method %q not registered", m)
		}
	}
	gw := []string{
		"/v1/get-applications", "/v1/get-application", "/v1/update-application",
		"/v1/add-application", "/v1/delete-application", "/v1/deploy-application",
		"/v1/undeploy-application", "/v1/get-k8s-status",
	}
	for _, p := range gw {
		if zapApplicationDeployGateway[p] == nil {
			t.Errorf("gateway path %q not registered", p)
		}
	}
	if len(zapApplicationDeployCloud) != len(cloud) || len(zapApplicationDeployGateway) != len(gw) {
		t.Errorf("table size mismatch: cloud=%d gateway=%d, want %d/%d",
			len(zapApplicationDeployCloud), len(zapApplicationDeployGateway), len(cloud), len(gw))
	}
}

// TestZapApplicationDeployAuthRejection asserts the ONE identity seam refuses an
// unauthenticated caller on EVERY handler, before any object/DB call — an empty
// credential resolves to a nil principal, so each handler returns 401 with the
// {status:"error"} envelope the beego ResponseUnauthorized returns. The k8s.status
// super-admin gate also fails closed at 401 for no principal (403 is reserved for
// an authenticated non-super-admin).
func TestZapApplicationDeployAuthRejection(t *testing.T) {
	cases := []struct {
		name string
		h    zapApplicationDeployFn
		body string
	}{
		{"application.list", zapGetApplicationsHandler, `{}`},
		{"application.get", zapGetApplicationHandler, `{"id":"admin/x"}`},
		{"application.update", zapUpdateApplicationHandler, `{"owner":"admin","name":"x"}`},
		{"application.add", zapAddApplicationHandler, `{"owner":"admin","name":"x","template":"t"}`},
		{"application.delete", zapDeleteApplicationHandler, `{"owner":"admin","name":"x"}`},
		{"application.deploy", zapDeployApplicationHandler, `{"owner":"admin","name":"x"}`},
		{"application.undeploy", zapUndeployApplicationHandler, `{"id":"admin/x"}`},
		{"k8s.status", zapGetK8sStatusHandler, ``},
	}
	for _, tc := range cases {
		msg, err := tc.h(context.Background(), "", []byte(tc.body))
		st, env := decodeCloudApp(t, msg, err)
		if st != 401 {
			t.Errorf("%s empty auth: status = %d, want 401", tc.name, st)
		}
		if env.Status != "error" {
			t.Errorf("%s empty auth: envelope status = %q, want error", tc.name, env.Status)
		}
	}
}

// TestZapAppOkEnvelope pins the happy-path response shape to ResponseOk EXACTLY:
// a single data arg populates Data (Data2 nil); a second arg populates Data2 (the
// GetApplications pagination count). This is the response-encoding contract every
// handler's success path returns — asserted without a DB.
func TestZapAppOkEnvelope(t *testing.T) {
	apps := []*object.Application{{Owner: "admin", Name: "a"}, {Owner: "admin", Name: "b"}}

	msg, err := zapAppOk(apps)
	st, env := decodeCloudApp(t, msg, err)
	if st != 200 || env.Status != "ok" {
		t.Fatalf("single-arg: status=%d envelope=%q, want 200/ok", st, env.Status)
	}
	if env.Data2 != nil {
		t.Errorf("single-arg: Data2 = %v, want nil", env.Data2)
	}

	msg, err = zapAppOk(apps, 7)
	st, env = decodeCloudApp(t, msg, err)
	if st != 200 || env.Status != "ok" {
		t.Fatalf("two-arg: status=%d envelope=%q, want 200/ok", st, env.Status)
	}
	// JSON round-trips the count through interface{} as a float64.
	if got, ok := env.Data2.(float64); !ok || got != 7 {
		t.Errorf("two-arg: Data2 = %v (%T), want 7", env.Data2, env.Data2)
	}
}
