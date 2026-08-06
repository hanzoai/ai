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
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
)

// This file is the whole Kubernetes story for this service: a registration seam
// and a default that refuses honestly. There is no client-go here, and that is
// the point — `go list -deps` on this binary finds no Kubernetes client, so
// "the AI control plane runs without a cluster" is a property of the build
// rather than a configuration you have to get right.
//
// Kubernetes objects belong to the k8s layer (hanzoai/operator and friends), or
// to visor for other clouds. This package states WHAT it wants — a namespace, a
// rendered manifest, a TrainJob, an InferenceService — and hands it to whatever
// cluster implementation the deployment registered.
//
// SHAPES. Objects cross as decoded-JSON maps — exactly what
// unstructured.Unstructured wraps, so neither side marshals. A
// GroupVersionResource crosses as three strings; a cluster-scoped resource
// (Namespace, Node) passes namespace "".
//
// NUMBERS. A map destined for the cluster must use int64 for integers, not
// plain int: the conversion the dynamic client deep-copies through panics on
// int. Callers keep their int64(...) casts; an implementation may normalise.

// K8sClient is the cluster seam.
type K8sClient interface {
	// Get returns one object. Missing objects yield ErrK8sNotFound.
	Get(ctx context.Context, group, version, resource, namespace, name string) (map[string]any, error)
	// List returns the objects in a namespace ("" for cluster-scoped). A missing
	// CRD, API group, or namespace yields ErrK8sNotFound — callers rely on being
	// able to tell "no cluster state yet" from "the request failed".
	List(ctx context.Context, group, version, resource, namespace string) ([]map[string]any, error)
	// Create creates one object. A losing race yields ErrK8sAlreadyExists, which
	// callers treat as success.
	Create(ctx context.Context, group, version, resource, namespace string, obj map[string]any) error
	// Delete removes one object. A missing object yields ErrK8sNotFound, which
	// callers treat as success (teardown is idempotent).
	Delete(ctx context.Context, group, version, resource, namespace, name string) error
	// Apply upserts ONE arbitrary object, resolving its apiVersion+kind to a
	// resource through the cluster's own discovery and defaulting a namespaced
	// object into namespace. This is the seam for rendered manifests: only
	// something holding a cluster connection can map a Kind to a resource, so
	// asking the caller to do it would drag discovery back across the boundary.
	Apply(ctx context.Context, namespace string, obj map[string]any) error
	// Host is the cluster API server's hostname, used to build the externally
	// reachable URL for a NodePort/ClusterIP service. Empty when unknown.
	Host() string
	// Ready reports cluster reachability and, when false, WHY. The reason is
	// surfaced verbatim to operators, so "unavailable" is never silent — the
	// whole point of failing closed is that somebody can find out what is missing.
	Ready() (ok bool, reason string)
}

// The cluster-error sentinels. They stand in for apierrors.IsNotFound and
// friends so no caller needs the k8s error package. An implementation MUST wrap
// the underlying error (%w) rather than replacing it: callers print the original
// text into failure reasons, and losing it would turn a precise RBAC message
// into a shrug.
var (
	ErrK8sNotFound      = errors.New("k8s: resource not found")
	ErrK8sAlreadyExists = errors.New("k8s: resource already exists")
	ErrK8sForbidden     = errors.New("k8s: forbidden (RBAC)")
)

var (
	k8sFactoryMu sync.RWMutex
	k8sFactory   func(kubeconfig string) (K8sClient, error)
)

// RegisterK8sClientFactory installs the cluster implementation. The kubeconfig
// is the ConfigText of the tenant's Kubernetes provider, so one process can
// serve many clusters. Called from the build that has a cluster to talk to;
// this service never calls it and therefore never links a Kubernetes client.
func RegisterK8sClientFactory(f func(kubeconfig string) (K8sClient, error)) {
	k8sFactoryMu.Lock()
	defer k8sFactoryMu.Unlock()
	k8sFactory = f
}

// BuildK8sClient returns the registered implementation for a kubeconfig, or the
// unavailable default. It never returns nil: a nil client would push a nil-check
// to every call site, and the one that got forgotten would panic in a request
// handler instead of returning an error that names what is missing.
func BuildK8sClient(kubeconfig string) K8sClient {
	k8sFactoryMu.RLock()
	f := k8sFactory
	k8sFactoryMu.RUnlock()
	if f == nil {
		return unavailableK8s{reason: "kubernetes support is not linked into this build; " +
			"application deploy and fine-tuning are cluster-facing and stay disabled"}
	}
	c, err := f(kubeconfig)
	if err != nil {
		return unavailableK8s{reason: err.Error()}
	}
	if c == nil {
		return unavailableK8s{reason: "kubernetes factory returned no client"}
	}
	return c
}

// unavailableK8s is the no-cluster default. Every method fails with the SAME
// reason Ready reports, so an operator reading a failed deploy and an operator
// reading the cluster status learn the same thing.
type unavailableK8s struct{ reason string }

func (u unavailableK8s) err() error { return &k8sUnavailableError{reason: u.reason} }

func (u unavailableK8s) Get(context.Context, string, string, string, string, string) (map[string]any, error) {
	return nil, u.err()
}

func (u unavailableK8s) List(context.Context, string, string, string, string) ([]map[string]any, error) {
	return nil, u.err()
}

func (u unavailableK8s) Create(context.Context, string, string, string, string, map[string]any) error {
	return u.err()
}

func (u unavailableK8s) Delete(context.Context, string, string, string, string, string) error {
	return u.err()
}

func (u unavailableK8s) Apply(context.Context, string, map[string]any) error { return u.err() }

func (u unavailableK8s) Host() string { return "" }

func (u unavailableK8s) Ready() (bool, string) { return false, u.reason }

// k8sUnavailableError is deliberately NOT one of the cluster sentinels. A caller
// asking "was this a NotFound?" must get "no" when the truth is "there is no
// cluster" — otherwise an absent cluster would read as an absent namespace and
// an application would be reported undeployed rather than unreachable.
type k8sUnavailableError struct{ reason string }

func (e *k8sUnavailableError) Error() string { return "k8s: unavailable: " + e.reason }

// Compile-time proof the default satisfies the seam.
var _ K8sClient = unavailableK8s{}

// gvr names one resource on the seam. The triples this package reads are listed
// once, here, so a typo is a compile error rather than a 404 at runtime.
type gvr struct{ group, version, resource string }

var (
	namespacesGVR   = gvr{"", "v1", "namespaces"}
	podsGVR         = gvr{"", "v1", "pods"}
	servicesGVR     = gvr{"", "v1", "services"}
	eventsGVR       = gvr{"", "v1", "events"}
	nodesGVR        = gvr{"", "v1", "nodes"}
	deploymentsGVR  = gvr{"apps", "v1", "deployments"}
	statefulSetsGVR = gvr{"apps", "v1", "statefulsets"}
	ingressesGVR    = gvr{"networking.k8s.io", "v1", "ingresses"}
	podMetricsGVR   = gvr{"metrics.k8s.io", "v1beta1", "pods"}

	// trainJobGVR is TrainJob (trainer.kubeflow.org/v1alpha1), the fine-tuning CR.
	trainJobGVR = gvr{"trainer.kubeflow.org", "v1alpha1", "trainjobs"}
	// inferenceServiceGVR is KServe InferenceService (serving.kserve.io/v1beta1).
	inferenceServiceGVR = gvr{"serving.kserve.io", "v1beta1", "inferenceservices"}
)

// listInto lists a resource and decodes every item into T. Decoding runs through
// apimachinery's converter rather than encoding/json so integers stay int64 and
// a quantity like "250m" parses the way the API server meant it.
func listInto[T any](ctx context.Context, c K8sClient, g gvr, namespace string) ([]T, error) {
	items, err := c.List(ctx, g.group, g.version, g.resource, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(items))
	for _, item := range items {
		var v T
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// getInto reads one object and decodes it into out.
func getInto[T any](ctx context.Context, c K8sClient, g gvr, namespace, name string, out *T) error {
	obj, err := c.Get(ctx, g.group, g.version, g.resource, namespace, name)
	if err != nil {
		return err
	}
	return runtime.DefaultUnstructuredConverter.FromUnstructured(obj, out)
}
