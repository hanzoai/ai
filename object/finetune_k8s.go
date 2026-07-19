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
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// finetune_k8s.go is the data plane: it renders a schema-valid `trainer.kubeflow.org`
// TrainJob CR for a FinetuneJob and submits it through the SAME generic apply path
// every other Hanzo k8s resource uses (object/template_deploy.go deployResource via
// the default Kubernetes provider's kubeconfig). It provisions an in-namespace
// HF-token Secret so the operator's model/dataset initializers can pull PRIVATE or
// gated `hf://…` repos, passes the efficient hyperparameters to the trainer as env,
// and reads the live CR status back into the FinetuneJob.
//
// The TrainJob references a ClusterTrainingRuntime (RuntimeFor) that must be
// installed in the cluster (hanzo-ml/trainer chart) and a GPU pool that can
// satisfy resourcesPerNode — those are the cluster prerequisites; the CR itself is
// complete and correct here.

// trainJobGVR is the GroupVersionResource for TrainJob (trainer.kubeflow.org/v1alpha1).
var trainJobGVR = schema.GroupVersionResource{
	Group:    "trainer.kubeflow.org",
	Version:  "v1alpha1",
	Resource: "trainjobs",
}

const hfTokenSecretName = "hf-token"

// FinetuneNamespace is the per-org namespace TrainJobs (and their HF-token Secret)
// live in, isolating one tenant's runs from another's.
func FinetuneNamespace(owner string) string {
	slug := strings.ToLower(owner)
	slug = strings.NewReplacer("/", "-", "_", "-", " ", "-", ".", "-").Replace(slug)
	if slug == "" {
		slug = "default"
	}
	return "ft-" + slug
}

// normalizeStorageUri turns a bare HuggingFace repo id into an `hf://…` URI and
// leaves an explicit scheme URI (hf://, s3://, gs://, pvc://) untouched. Empty
// stays empty.
func normalizeStorageUri(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.Contains(ref, "://") {
		return ref
	}
	return "hf://" + strings.Trim(ref, "/")
}

// ensureHfTokenSecret creates/updates the in-namespace HF-token Secret (key
// HF_TOKEN) the initializers reference to pull private/gated repos. No-op (returns
// "", nil) when no token is configured — public repos still pull.
func ensureHfTokenSecret(namespace, token, lang string) (string, error) {
	if token == "" {
		return "", nil
	}
	if err := ensureK8sClient(lang); err != nil {
		return "", err
	}
	secret := map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]interface{}{
			"name":      hfTokenSecretName,
			"namespace": namespace,
			"labels":    map[string]interface{}{"managed-by": "hanzo-cloud"},
		},
		"type": "Opaque",
		"stringData": map[string]interface{}{
			"HF_TOKEN":               token,
			"HUGGING_FACE_HUB_TOKEN": token,
			"HUGGINGFACE_HUB_TOKEN":  token,
		},
	}
	b, err := json.Marshal(secret)
	if err != nil {
		return "", err
	}
	if err := k8sClient.deployResource(string(b), namespace, lang); err != nil {
		return "", err
	}
	return hfTokenSecretName, nil
}

// trainerEnv renders the efficient hyperparameters + IO paths as TrainJob trainer
// env vars. This is the universal config channel the Hanzo runtimes read (works
// for the generic transformers+PEFT runtime and the optimized per-family runtimes
// alike), so it is robust to a runtime's exact entrypoint.
func trainerEnv(job *FinetuneJob, hp Hyperparams, hasToken bool) []interface{} {
	outputDir := job.OutputUri
	if outputDir == "" {
		outputDir = "/workspace/output"
	}
	kv := func(name, value string) map[string]interface{} {
		return map[string]interface{}{"name": name, "value": value}
	}
	env := []interface{}{
		kv("BASE_MODEL", job.BaseModel),
		kv("METHOD", job.Method),
		kv("TASK", job.Task),
		kv("MODEL_DIR", "/workspace/model"),
		kv("DATASET_DIR", "/workspace/dataset"),
		kv("OUTPUT_DIR", outputDir),
		kv("EPOCHS", strconv.FormatFloat(hp.Epochs, 'f', -1, 64)),
		kv("LEARNING_RATE", strconv.FormatFloat(hp.LearningRate, 'g', -1, 64)),
		kv("BATCH_SIZE", strconv.Itoa(hp.BatchSize)),
		kv("GRAD_ACCUM", strconv.Itoa(hp.GradAccum)),
		kv("MAX_SEQ_LEN", strconv.Itoa(hp.MaxSeqLen)),
		kv("LORA_RANK", strconv.Itoa(hp.LoraRank)),
		kv("LORA_ALPHA", strconv.Itoa(hp.LoraAlpha)),
		kv("LORA_DROPOUT", strconv.FormatFloat(hp.LoraDropout, 'g', -1, 64)),
		kv("QUANT_4BIT", strconv.FormatBool(hp.Quant4bit)),
		kv("GRADIENT_CHECKPOINTING", strconv.FormatBool(hp.GradientCheckpointing)),
		kv("WARMUP_RATIO", strconv.FormatFloat(hp.WarmupRatio, 'g', -1, 64)),
		kv("WEIGHT_DECAY", strconv.FormatFloat(hp.WeightDecay, 'g', -1, 64)),
	}
	if hasToken {
		env = append(env, map[string]interface{}{
			"name": "HF_TOKEN",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{"name": hfTokenSecretName, "key": "HF_TOKEN"},
			},
		})
	}
	return env
}

// buildTrainJobObject renders the TrainJob CR as a JSON object (valid YAML for the
// deployResource decoder). It references the runtime, overrides the model/dataset
// initializers (with secretRef for private repos), and sets the trainer GPU
// resources + hyperparameter env.
func buildTrainJobObject(job *FinetuneJob, hp Hyperparams, secretName string) map[string]interface{} {
	modelUri := normalizeStorageUri(job.BaseModel)
	datasetUri := normalizeStorageUri(job.Dataset)

	modelInit := map[string]interface{}{"storageUri": modelUri}
	datasetInit := map[string]interface{}{"storageUri": datasetUri}
	if secretName != "" {
		ref := map[string]interface{}{"name": secretName}
		modelInit["secretRef"] = ref
		datasetInit["secretRef"] = ref
	}

	gpuCount := job.GpuCount
	if gpuCount < 1 {
		gpuCount = 1
	}
	numNodes := job.NumNodes
	if numNodes < 1 {
		numNodes = 1
	}

	trainer := map[string]interface{}{
		"numNodes": numNodes,
		"resourcesPerNode": map[string]interface{}{
			"limits": map[string]interface{}{
				"nvidia.com/gpu": strconv.Itoa(gpuCount),
			},
		},
		"env": trainerEnv(job, hp, secretName != ""),
	}

	return map[string]interface{}{
		"apiVersion": "trainer.kubeflow.org/v1alpha1",
		"kind":       "TrainJob",
		"metadata": map[string]interface{}{
			"name":      job.CrName,
			"namespace": job.Namespace,
			"labels": map[string]interface{}{
				"managed-by":            "hanzo-cloud",
				"hanzo.ai/finetune-job": job.Name,
				"hanzo.ai/org":          strings.ToLower(strings.NewReplacer("/", "-").Replace(job.Owner)),
			},
		},
		"spec": map[string]interface{}{
			"runtimeRef": map[string]interface{}{
				"name":     job.Runtime,
				"apiGroup": "trainer.kubeflow.org",
				"kind":     "ClusterTrainingRuntime",
			},
			"initializer": map[string]interface{}{
				"dataset": datasetInit,
				"model":   modelInit,
			},
			"trainer": trainer,
		},
	}
}

// SubmitFinetuneTrainJob provisions the namespace + HF-token Secret and applies the
// TrainJob CR. token may be "" (public repos only). Returns the namespace + CR name
// it used (already set on job before the call).
func SubmitFinetuneTrainJob(job *FinetuneJob, hp Hyperparams, token, lang string) error {
	if err := ensureK8sClient(lang); err != nil {
		return err
	}
	if err := k8sClient.createNamespaceIfNotExists(job.Namespace); err != nil {
		return fmt.Errorf("failed to ensure namespace %s: %w", job.Namespace, err)
	}
	secretName, err := ensureHfTokenSecret(job.Namespace, token, lang)
	if err != nil {
		return fmt.Errorf("failed to provision HF token secret: %w", err)
	}
	obj := buildTrainJobObject(job, hp, secretName)
	b, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to render TrainJob: %w", err)
	}
	if err := k8sClient.deployResource(string(b), job.Namespace, lang); err != nil {
		return fmt.Errorf("failed to submit TrainJob: %w", err)
	}
	return nil
}

// FinetuneTrainJobStatus is the live status read back from a TrainJob CR.
type FinetuneTrainJobStatus struct {
	Status   string // pending|queued|running|succeeded|failed|cancelled
	Progress int    // 0..100 (best-effort)
	Message  string
	Started  string // RFC3339, when the run entered Running (best-effort)
	Finished string // RFC3339, when it reached a terminal state (best-effort)
	Found    bool   // false when the CR no longer exists in the cluster
}

// GetFinetuneTrainJobStatus reads the TrainJob CR and maps its conditions to a
// FinetuneJob status. Found=false (no error) when the CR is gone.
func GetFinetuneTrainJobStatus(job *FinetuneJob, lang string) (*FinetuneTrainJobStatus, error) {
	if err := ensureK8sClient(lang); err != nil {
		return nil, err
	}
	u, err := k8sClient.dynamicClient.Resource(trainJobGVR).Namespace(job.Namespace).
		Get(context.TODO(), job.CrName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &FinetuneTrainJobStatus{Found: false}, nil
		}
		return nil, err
	}
	return interpretTrainJobStatus(u), nil
}

// interpretTrainJobStatus maps a TrainJob's status.conditions to a FinetuneJob
// status. TrainJob conditions use types Created / Complete / Failed / Suspended
// with metav1.Condition shape.
func interpretTrainJobStatus(u *unstructured.Unstructured) *FinetuneTrainJobStatus {
	out := &FinetuneTrainJobStatus{Status: "pending", Found: true}
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")

	condTrue := func(name string) (bool, string, string) {
		for _, c := range conditions {
			m, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			t, _, _ := unstructured.NestedString(m, "type")
			s, _, _ := unstructured.NestedString(m, "status")
			if strings.EqualFold(t, name) {
				msg, _, _ := unstructured.NestedString(m, "message")
				lt, _, _ := unstructured.NestedString(m, "lastTransitionTime")
				return strings.EqualFold(s, "True"), msg, lt
			}
		}
		return false, "", ""
	}

	if ok, msg, lt := condTrue("Failed"); ok {
		// Progress is deliberately left unchanged on failure (unlike Complete,
		// which pins 100) — so it is simply not reassigned here.
		out.Status, out.Message, out.Finished = "failed", msg, lt
		return out
	}
	if ok, msg, lt := condTrue("Complete"); ok {
		out.Status, out.Message, out.Finished, out.Progress = "succeeded", msg, lt, 100
		return out
	}
	if ok, _, _ := condTrue("Suspended"); ok {
		out.Status = "queued"
		return out
	}
	if ok, msg, lt := condTrue("Created"); ok {
		// Created but not yet complete/failed → running (or starting).
		out.Status, out.Message, out.Started = "running", msg, lt
		out.Progress = 50
		return out
	}
	// Fall back to the creationTimestamp as a "started" hint.
	if ct := u.GetCreationTimestamp(); !ct.IsZero() {
		out.Started = ct.UTC().Format(time.RFC3339)
	}
	return out
}

// DeleteFinetuneTrainJob deletes the TrainJob CR (used for cancel). NotFound is
// not an error — the desired end state (gone) is already met.
func DeleteFinetuneTrainJob(job *FinetuneJob, lang string) error {
	if err := ensureK8sClient(lang); err != nil {
		return err
	}
	err := k8sClient.dynamicClient.Resource(trainJobGVR).Namespace(job.Namespace).
		Delete(context.TODO(), job.CrName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
