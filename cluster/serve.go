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
	"fmt"
	"strings"

	"github.com/hanzoai/ai/object"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// Serving is what a deploy returns to the controller.
type Serving struct {
	ModelId   string `json:"modelId"`   // public model id (use in /v1/chat/completions)
	Provider  string `json:"provider"`  // the registered provider name
	Service   string `json:"service"`   // InferenceService name
	Namespace string `json:"namespace"` // its namespace
	Url       string `json:"url"`       // status.url, when already available
}

// finetuneServiceName is the InferenceService (and registered-model id) name for a job.
func finetuneServiceName(job *object.FinetuneJob) string {
	n := strings.ToLower(job.Name)
	n = strings.NewReplacer("/", "-", "_", "-", " ", "-", ".", "-").Replace(n)
	return "ft-" + n
}

// servingAccelerators is how many accelerators ONE served replica asks for.
//
// It is a per-replica default, not a fact about the finished job: a training
// topology (GpuCount × NumNodes, chosen for gradient and optimizer memory) does not
// tell you what the merged weights need in order to SERVE, so deriving one from the
// other would be a guess dressed as arithmetic. One is the KServe norm; a model that
// needs more needs a serving requirement of its own on the job, stated by whoever
// knows it.
const servingAccelerators = 1

// buildInferenceServiceObject renders the InferenceService CR. The HuggingFace
// model format auto-selects the vLLM/transformers serving runtime; storageUri is
// the org's S3 checkpoint dir. limits carries the accelerator request the CLUSTER
// can satisfy (see accelerator.go) — this renderer never names a vendor.
func buildInferenceServiceObject(job *object.FinetuneJob, name string, limits map[string]interface{}) map[string]interface{} {
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
						"limits": limits,
					},
				},
			},
		},
	}
}

// DeployFinetune serves a completed job's checkpoints and registers the result
// as a routable model. Requires a succeeded job with an OutputUri. Idempotent:
// re-deploying updates the InferenceService + model records in place.
func DeployFinetune(job *object.FinetuneJob, lang string) (*Serving, error) {
	if job.Status != "succeeded" {
		return nil, fmt.Errorf("job %s is not complete (status %q) — only succeeded jobs can be deployed", job.Name, job.Status)
	}
	if strings.TrimSpace(job.OutputUri) == "" {
		return nil, fmt.Errorf("job %s has no checkpoint output URI to serve", job.Name)
	}
	c, err := ensure(lang)
	if err != nil {
		return nil, err
	}
	ctx := context.TODO()
	// Ask for accelerators the CLUSTER advertises, and refuse before creating
	// anything when it advertises none. The InferenceService CRD exists and its
	// controller runs, so a create with a resource name no node offers SUCCEEDS and
	// leaves a predictor that can never be scheduled — plus a model registered on
	// api.hanzo.ai whose every inference call fails. Refusing here is the loud
	// failure that silence was hiding.
	limits, err := acceleratorRequest(ctx, c, servingAccelerators)
	if err != nil {
		return nil, fmt.Errorf("cannot serve %s: %w", job.Name, err)
	}
	if err := createNamespaceIfNotExists(ctx, c, job.Namespace); err != nil {
		return nil, fmt.Errorf("failed to ensure namespace %s: %w", job.Namespace, err)
	}

	name := finetuneServiceName(job)
	if err := c.Apply(ctx, job.Namespace, buildInferenceServiceObject(job, name, limits)); err != nil {
		return nil, fmt.Errorf("failed to deploy InferenceService: %w", err)
	}

	url := getInferenceServiceUrl(ctx, c, job.Namespace, name)
	modelId := job.Name // public model id, keyed per-org by the ModelRoute PK

	if err := object.RegisterServedModel(job, name, modelId, url); err != nil {
		return nil, fmt.Errorf("served the model but failed to register it: %w", err)
	}

	return &Serving{
		ModelId:   modelId,
		Provider:  name,
		Service:   name,
		Namespace: job.Namespace,
		Url:       url,
	}, nil
}

// getInferenceServiceUrl best-effort reads status.url from a freshly-created
// InferenceService (usually empty until the predictor is Ready).
func getInferenceServiceUrl(ctx context.Context, c K8sClient, namespace, name string) string {
	obj, err := c.Get(ctx, inferenceServiceGVR.group, inferenceServiceGVR.version,
		inferenceServiceGVR.resource, namespace, name)
	if err != nil {
		return ""
	}
	url, _, _ := unstructured.NestedString(obj, "status", "url")
	return url
}

// UndeployFinetune deletes the InferenceService and the model registration.
func UndeployFinetune(job *object.FinetuneJob, lang string) error {
	c, err := ensure(lang)
	if err != nil {
		return err
	}
	name := finetuneServiceName(job)
	err = c.Delete(context.TODO(), inferenceServiceGVR.group, inferenceServiceGVR.version,
		inferenceServiceGVR.resource, job.Namespace, name)
	if err != nil && !errors.Is(err, ErrK8sNotFound) {
		return err
	}
	if job.DeployedModel != "" {
		if r, _ := object.GetModelRoute(job.Owner, job.DeployedModel); r != nil {
			_, _ = object.DeleteModelRoute(r)
		}
	}
	return nil
}
