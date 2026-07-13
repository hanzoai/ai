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
package object

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// finetune_deploy.go closes the loop: it serves a completed fine-tune's weights as
// a KServe InferenceService (HuggingFace/vLLM runtime over the org's S3 checkpoint
// dir) and REGISTERS it as a first-class model on api.hanzo.ai — an `object.Provider`
// (Category "Model", Type "Local" = OpenAI-compatible custom endpoint) plus a
// per-org `ModelRoute` mapping the public model id → that provider. After this, the
// fine-tuned model is callable through the SAME `/v1/chat/completions` surface and
// billed through the SAME commerce ledger as every other model (it routes via
// ResolveModelRouteFromDB). The training runtime writes a SERVABLE HuggingFace-format
// directory (LoRA adapters merged into the base) to OutputUri, so one serving path
// works for lora/qlora/full alike.

// inferenceServiceGVR is the GVR for KServe InferenceService (serving.kserve.io/v1beta1).
var inferenceServiceGVR = schema.GroupVersionResource{
	Group:    "serving.kserve.io",
	Version:  "v1beta1",
	Resource: "inferenceservices",
}

// FinetuneDeployResult is what a deploy returns to the controller.
type FinetuneDeployResult struct {
	ModelId   string `json:"modelId"`   // public model id (use in /v1/chat/completions)
	Provider  string `json:"provider"`  // the registered provider name
	Service   string `json:"service"`   // InferenceService name
	Namespace string `json:"namespace"` // its namespace
	Url       string `json:"url"`       // status.url, when already available
}

// finetuneServiceName is the InferenceService (and registered-model id) name for a job.
func finetuneServiceName(job *FinetuneJob) string {
	n := strings.ToLower(job.Name)
	n = strings.NewReplacer("/", "-", "_", "-", " ", "-", ".", "-").Replace(n)
	return "ft-" + n
}

// buildInferenceServiceObject renders the InferenceService CR. The HuggingFace
// model format auto-selects the vLLM/transformers serving runtime; storageUri is
// the org's S3 checkpoint dir.
func buildInferenceServiceObject(job *FinetuneJob, name string) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "serving.kserve.io/v1beta1",
		"kind":       "InferenceService",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": job.Namespace,
			"labels": map[string]interface{}{
				"managed-by":            "hanzo-cloud",
				"hanzo.ai/finetune-job": job.Name,
			},
		},
		"spec": map[string]interface{}{
			"predictor": map[string]interface{}{
				"model": map[string]interface{}{
					"modelFormat": map[string]interface{}{"name": "huggingface"},
					"storageUri":  job.OutputUri,
					"resources": map[string]interface{}{
						"limits": map[string]interface{}{"nvidia.com/gpu": "1"},
					},
				},
			},
		},
	}
}

// DeployFinetuneJob serves a completed job's checkpoints and registers the result
// as a routable model. Requires a succeeded job with an OutputUri. Idempotent:
// re-deploying updates the InferenceService + model records in place.
func DeployFinetuneJob(job *FinetuneJob, lang string) (*FinetuneDeployResult, error) {
	if job.Status != "succeeded" {
		return nil, fmt.Errorf("job %s is not complete (status %q) — only succeeded jobs can be deployed", job.Name, job.Status)
	}
	if strings.TrimSpace(job.OutputUri) == "" {
		return nil, fmt.Errorf("job %s has no checkpoint output URI to serve", job.Name)
	}
	if err := ensureK8sClient(lang); err != nil {
		return nil, err
	}
	if err := k8sClient.createNamespaceIfNotExists(job.Namespace); err != nil {
		return nil, fmt.Errorf("failed to ensure namespace %s: %w", job.Namespace, err)
	}

	name := finetuneServiceName(job)
	obj := buildInferenceServiceObject(job, name)
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to render InferenceService: %w", err)
	}
	if err := k8sClient.deployResource(string(b), job.Namespace, lang); err != nil {
		return nil, fmt.Errorf("failed to deploy InferenceService: %w", err)
	}

	url := getInferenceServiceUrl(job.Namespace, name)
	modelId := job.Name // public model id, keyed per-org by the ModelRoute PK

	if err := registerServedModel(job, name, modelId, url); err != nil {
		return nil, fmt.Errorf("served the model but failed to register it: %w", err)
	}

	return &FinetuneDeployResult{
		ModelId:   modelId,
		Provider:  name,
		Service:   name,
		Namespace: job.Namespace,
		Url:       url,
	}, nil
}

// getInferenceServiceUrl best-effort reads status.url from a freshly-created
// InferenceService (usually empty until the predictor is Ready).
func getInferenceServiceUrl(namespace, name string) string {
	u, err := k8sClient.dynamicClient.Resource(inferenceServiceGVR).Namespace(namespace).
		Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	url, _, _ := unstructured.NestedString(u.Object, "status", "url")
	return url
}

// registerServedModel writes the Provider + ModelRoute so the fine-tune is callable
// via /v1/chat/completions and routed per-org. The in-cluster predictor exposes an
// OpenAI-compatible API (KServe HuggingFace runtime → `/openai/v1`), so the provider
// is Type "Local" with compatibleProvider "openai". Idempotent upsert.
func registerServedModel(job *FinetuneJob, serviceName, modelId, statusUrl string) error {
	// Predictor OpenAI base. Prefer the resolved status.url; otherwise the stable
	// in-cluster predictor DNS (the actual host suffix is cluster-resolved).
	base := strings.TrimRight(statusUrl, "/")
	if base == "" {
		base = fmt.Sprintf("http://%s-predictor.%s.svc.cluster.local", serviceName, job.Namespace)
	}
	providerUrl := base + "/openai/v1"

	provider := &Provider{
		Owner:              job.Owner,
		Name:               serviceName,
		CreatedTime:        time.Now().Format(time.RFC3339),
		DisplayName:        firstNonEmpty(job.DisplayName, job.Name) + " (fine-tune)",
		Category:           "Model",
		Type:               "Local",
		SubType:            serviceName, // the served-model name the predictor answers to
		CompatibleProvider: "openai",
		ProviderUrl:        providerUrl,
		ClientSecret:       "", // in-cluster, unauthenticated predictor
		State:              "Active",
	}
	if err := upsertProvider(provider); err != nil {
		return err
	}

	route := &ModelRoute{
		Owner:     job.Owner,
		ModelName: modelId,
		Provider:  serviceName,
		Upstream:  serviceName,
		OwnedBy:   job.Owner,
		Enabled:   true,
	}
	return upsertModelRoute(route)
}

func upsertProvider(p *Provider) error {
	existing, _ := GetModelProviderByNameForOrg(p.Owner, p.Name)
	if existing != nil {
		return adapter.db.Model(p).Update()
	}
	_, err := AddProvider(p)
	return err
}

func upsertModelRoute(r *ModelRoute) error {
	existing, _ := GetModelRoute(r.Owner, r.ModelName)
	if existing != nil {
		_, err := UpdateModelRoute(r.Owner, r.ModelName, r)
		return err
	}
	_, err := AddModelRoute(r)
	return err
}

// UndeployFinetuneModel deletes the InferenceService and the model registration.
func UndeployFinetuneModel(job *FinetuneJob, lang string) error {
	if err := ensureK8sClient(lang); err != nil {
		return err
	}
	name := finetuneServiceName(job)
	err := k8sClient.dynamicClient.Resource(inferenceServiceGVR).Namespace(job.Namespace).
		Delete(context.TODO(), name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if job.DeployedModel != "" {
		if r, _ := GetModelRoute(job.Owner, job.DeployedModel); r != nil {
			_, _ = DeleteModelRoute(r)
		}
	}
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
