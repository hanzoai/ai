// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/hanzoai/ai/i18n"
	"github.com/hanzoai/ai/object"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// The active cluster, keyed by the kubeconfig it was built from. A tenant edits
// its Kubernetes provider and the next call rebuilds against the new config;
// nothing else in the package holds a connection.
var (
	clientMu     sync.Mutex
	client       K8sClient
	clientConfig string
)

// ensure returns a ready cluster client for the default Kubernetes provider,
// rebuilding it when the provider's kubeconfig has changed. It fails when no
// cluster is reachable, so every caller below can assume a live cluster instead
// of re-checking a connected flag.
func ensure(lang string) (K8sClient, error) {
	provider, err := object.GetDefaultKubernetesProvider(lang)
	if err != nil {
		return nil, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to get default Kubernetes provider: %v"), err))
	}
	if provider.ConfigText == "" {
		return nil, fmt.Errorf("%s", i18n.Translate(lang, "object:provider kubeconfig content is empty"))
	}

	clientMu.Lock()
	defer clientMu.Unlock()
	if client != nil && clientConfig == provider.ConfigText {
		return client, nil
	}
	c := BuildK8sClient(provider.ConfigText)
	if ok, reason := c.Ready(); !ok {
		return nil, errors.New(reason)
	}
	client, clientConfig = c, provider.ConfigText
	return client, nil
}

// Status reports whether the configured cluster is reachable.
func Status(lang string) (string, error) {
	c, err := ensure(lang)
	if err != nil {
		return "Disconnected", err
	}
	if ok, reason := c.Ready(); !ok {
		clientMu.Lock()
		client, clientConfig = nil, ""
		clientMu.Unlock()
		return "Disconnected", errors.New(reason)
	}
	return "Connected", nil
}

// createNamespaceIfNotExists is the idempotent namespace create every deploy
// path starts from.
func createNamespaceIfNotExists(ctx context.Context, c K8sClient, name string) error {
	_, err := c.Get(ctx, namespacesGVR.group, namespacesGVR.version, namespacesGVR.resource, "", name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrK8sNotFound) {
		return err
	}
	ns := map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":   name,
			"labels": map[string]any{"managed-by": "hanzo-cloud"},
		},
	}
	err = c.Create(ctx, namespacesGVR.group, namespacesGVR.version, namespacesGVR.resource, "", ns)
	if err != nil && !errors.Is(err, ErrK8sAlreadyExists) {
		return err
	}
	return nil
}

// applyManifest applies a rendered multi-document YAML manifest, defaulting
// namespaced objects into namespace and stamping the management label. Kind →
// resource resolution belongs to the cluster (it owns discovery), so each
// document goes over the seam whole.
func applyManifest(ctx context.Context, c K8sClient, manifest, namespace, lang string) error {
	docs, err := splitManifest(manifest)
	if err != nil {
		return fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to decode YAML: %v"), err))
	}
	for _, doc := range docs {
		if err := c.Apply(ctx, namespace, doc); err != nil {
			return fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to deploy resource: %v"), err))
		}
	}
	return nil
}

// splitManifest decodes a multi-document YAML manifest into objects, skipping
// empty documents and stamping the management label on each. Kept separate from
// the apply loop so it is a pure, testable function.
func splitManifest(manifest string) ([]map[string]any, error) {
	var out []map[string]any
	for _, doc := range strings.Split(manifest, "\n---") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		obj, err := decodeYAMLObject(doc)
		if err != nil {
			return nil, err
		}
		if obj == nil {
			continue
		}
		if kind, _ := obj["kind"].(string); kind == "" {
			continue
		}
		labelObject(obj, "managed-by", "hanzo-cloud")
		out = append(out, obj)
	}
	return out, nil
}

// decodeYAMLObject decodes one YAML document into a cluster object. It returns
// (nil, nil) for a document that carries no kind — an empty or comment-only
// document is a normal part of a rendered manifest, not an error. Decoding goes
// through unstructured's JSON path so integers land as int64, which is what the
// dynamic client requires.
func decodeYAMLObject(doc string) (map[string]any, error) {
	jsonBytes, err := utilyaml.ToJSON([]byte(doc))
	if err != nil {
		return nil, err
	}
	var u unstructured.Unstructured
	if err := u.UnmarshalJSON(jsonBytes); err != nil {
		if runtime.IsMissingKind(err) || errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	return u.Object, nil
}

// labelObject sets one metadata label, creating the maps it needs.
func labelObject(obj map[string]any, key, value string) {
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		meta = map[string]any{}
		obj["metadata"] = meta
	}
	labels, ok := meta["labels"].(map[string]any)
	if !ok {
		labels = map[string]any{}
		meta["labels"] = labels
	}
	labels[key] = value
}
