// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A rendered manifest is many documents; each becomes one object, and the empty
// and comment-only documents a template legitimately produces are not objects.
func TestSplitManifest_SeparatesDocumentsAndSkipsEmptyOnes(t *testing.T) {
	manifest := `---
apiVersion: v1
kind: Service
metadata:
  name: web
---
# just a comment, no object here
---

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
`
	docs, err := splitManifest(manifest)
	if err != nil {
		t.Fatalf("splitManifest: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d objects, want 2 (Service + Deployment)", len(docs))
	}
	if kind := docs[0]["kind"]; kind != "Service" {
		t.Errorf("first kind = %v, want Service", kind)
	}
	if kind := docs[1]["kind"]; kind != "Deployment" {
		t.Errorf("second kind = %v, want Deployment", kind)
	}
}

// Every applied object carries the management label, and stamping it must not
// drop the labels the manifest already declared.
func TestSplitManifest_StampsManagedByWithoutDroppingLabels(t *testing.T) {
	docs, err := splitManifest(`
apiVersion: v1
kind: Service
metadata:
  name: web
  labels:
    app: web
`)
	if err != nil {
		t.Fatalf("splitManifest: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d objects, want 1", len(docs))
	}
	meta := docs[0]["metadata"].(map[string]any)
	labels := meta["labels"].(map[string]any)
	if labels["managed-by"] != "hanzo-cloud" {
		t.Errorf("managed-by = %v, want hanzo-cloud", labels["managed-by"])
	}
	if labels["app"] != "web" {
		t.Errorf("pre-existing label app = %v, want web (must not be dropped)", labels["app"])
	}
}

// A manifest's integers must reach the seam as int64. The dynamic client
// deep-copies through a JSON-shaped conversion that PANICS on a plain int and
// silently mangles a float64, so this is the property that keeps a rendered
// replica count from taking down the deploy path.
func TestSplitManifest_IntegersDecodeAsInt64(t *testing.T) {
	docs, err := splitManifest(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 3
`)
	if err != nil {
		t.Fatalf("splitManifest: %v", err)
	}
	spec := docs[0]["spec"].(map[string]any)
	replicas, ok := spec["replicas"].(int64)
	if !ok {
		t.Fatalf("replicas decoded as %T, want int64", spec["replicas"])
	}
	if replicas != 3 {
		t.Errorf("replicas = %d, want 3", replicas)
	}
}

// A manifest that is not YAML is an error the deploy path must surface, not a
// silently empty object list that would report a successful deploy of nothing.
func TestSplitManifest_MalformedYAMLIsAnError(t *testing.T) {
	if _, err := splitManifest("\tnot: [valid yaml"); err == nil {
		t.Fatal("splitManifest accepted malformed YAML; want an error")
	}
}

// With no cluster implementation registered, the client is still usable and
// still refuses — never nil, so no call site can panic on it.
func TestBuildK8sClient_UnavailableWithoutAFactory(t *testing.T) {
	RegisterK8sClientFactory(nil)
	c := BuildK8sClient("some-kubeconfig")
	if c == nil {
		t.Fatal("BuildK8sClient returned nil; every call site would need a nil check")
	}
	ok, reason := c.Ready()
	if ok {
		t.Error("Ready reported true with no cluster linked")
	}
	if reason == "" {
		t.Error("Ready reported false with no reason; an operator cannot act on silence")
	}
	if _, err := c.Get(context.Background(), "", "v1", "namespaces", "", "default"); err == nil {
		t.Error("Get succeeded against no cluster")
	}
}

// The unavailable error must NOT read as NotFound. Confusing them would let an
// absent cluster masquerade as an absent namespace, and Phase would report an
// application undeployed when the truth is that nothing could be reached.
func TestUnavailableK8s_IsNotMistakenForNotFound(t *testing.T) {
	RegisterK8sClientFactory(nil)
	c := BuildK8sClient("")

	_, err := c.Get(context.Background(), "", "v1", "namespaces", "", "default")
	if err == nil {
		t.Fatal("Get succeeded against no cluster")
	}
	if errors.Is(err, ErrK8sNotFound) {
		t.Error("unavailable reads as ErrK8sNotFound; an outage would be reported as 'not deployed'")
	}
	if errors.Is(err, ErrK8sAlreadyExists) {
		t.Error("unavailable reads as ErrK8sAlreadyExists")
	}

	_, reason := c.Ready()
	if got := err.Error(); !strings.Contains(got, reason) {
		t.Errorf("error %q does not carry the Ready reason %q; the two must agree", got, reason)
	}
}

// A registered factory that fails must surface ITS reason, not a generic one —
// a bad kubeconfig should say so.
func TestBuildK8sClient_FactoryErrorBecomesTheReason(t *testing.T) {
	t.Cleanup(func() { RegisterK8sClientFactory(nil) })
	RegisterK8sClientFactory(func(string) (K8sClient, error) {
		return nil, errors.New("kubeconfig has no current-context")
	})
	c := BuildK8sClient("bad")
	ok, reason := c.Ready()
	if ok {
		t.Fatal("Ready reported true after the factory failed")
	}
	if reason != "kubeconfig has no current-context" {
		t.Errorf("reason = %q, want the factory's own error", reason)
	}
}

// A factory that returns (nil, nil) must not yield a nil client.
func TestBuildK8sClient_NilFromFactoryStillRefuses(t *testing.T) {
	t.Cleanup(func() { RegisterK8sClientFactory(nil) })
	RegisterK8sClientFactory(func(string) (K8sClient, error) { return nil, nil })
	c := BuildK8sClient("anything")
	if c == nil {
		t.Fatal("BuildK8sClient returned nil")
	}
	if ok, _ := c.Ready(); ok {
		t.Error("Ready reported true for a factory that produced no client")
	}
}
