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
	"fmt"

	"github.com/hanzoai/ai/util"

	"github.com/hanzoai/dbx"
)

// FinetuneJob is one fine-tuning / training run brokered to the cluster training
// operator (a `trainer.kubeflow.org` TrainJob). One row per job, scoped by org
// (Owner). The DB row is the durable record of intent + last-known status; the
// live truth is the TrainJob CR in the cluster, which finetune_k8s.go reads back
// into Status/Progress on every GetFinetuneJob.
//
// Mirrors object/model_route.go exactly (composite (Owner, Name) PK, RFC3339
// string timestamps, the db.go helpers) so the entity stays in the one
// established shape.
type FinetuneJob struct {
	Owner       string `db:"pk" json:"owner"` // org id (X-Org-Id / user.Owner)
	Name        string `db:"pk" json:"name"`  // job slug, unique within the org
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
	CreatedBy   string `json:"createdBy"` // user who created the job (billing subject = owner/createdBy)

	DisplayName string `json:"displayName"`
	BaseModel   string `json:"baseModel"`   // HF repo id, e.g. "meta-llama/Llama-3.1-8B"
	Method      string `json:"method"`      // "lora" | "qlora" | "full"
	Task        string `json:"task"`        // "instruct" | "chat" | "completion"
	Dataset     string `json:"dataset"`     // HF dataset id, or hf://… / s3://… URI
	Preset      string `json:"preset"`      // "recommended" | "balanced" | "aggressive" | "custom"
	Hyperparams string `json:"hyperparams"` // JSON blob (see Hyperparams in finetune_runtime.go)

	Runtime   string `json:"runtime"`   // ClusterTrainingRuntime name referenced by the CR
	GpuType   string `json:"gpuType"`   // e.g. "nvidia-a100-80gb"
	GpuCount  int    `json:"gpuCount"`  // GPUs per node
	NumNodes  int    `json:"numNodes"`  // training nodes
	Namespace string `json:"namespace"` // k8s namespace the TrainJob lives in
	CrName    string `json:"crName"`    // TrainJob CR name in-cluster

	Status        string `json:"status"`        // pending|queued|running|succeeded|failed|cancelled
	Progress      int    `json:"progress"`      // 0..100 (best-effort)
	Message       string `json:"message"`       // last status message / condition reason
	OutputUri     string `json:"outputUri"`     // s3://…/finetunes/<name>/ — checkpoint location
	DeployedModel string `json:"deployedModel"` // public model id once deployed to inference
	DeployUrl     string `json:"deployUrl"`     // InferenceService status.url once serving
	GpuSeconds    int64  `json:"gpuSeconds"`    // accumulated GPU-seconds metered to commerce
	CostCents     int64  `json:"costCents"`     // accumulated metered cost (cents)
	StartedTime   string `json:"startedTime"`   // when the run entered Running (for GPU-hour metering)
	FinishedTime  string `json:"finishedTime"`  // when the run reached a terminal state
	Metered       bool   `json:"metered"`       // true once terminal GPU-hours have been billed
	Error         string `json:"error"`
}

func (j *FinetuneJob) GetId() string {
	return fmt.Sprintf("%s/%s", j.Owner, j.Name)
}

// IsTerminal reports whether the job has reached a state with no further work.
func (j *FinetuneJob) IsTerminal() bool {
	switch j.Status {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

func GetFinetuneJobs(owner string) ([]*FinetuneJob, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	jobs := []*FinetuneJob{}
	err := findAll(adapter.db, "finetune_job", &jobs, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return jobs, err
	}
	return jobs, nil
}

func GetFinetuneJob(owner string, name string) (*FinetuneJob, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	job := FinetuneJob{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "finetune_job", &job, dbx.HashExp{"owner": owner, "name": name})
	if err != nil {
		return &job, err
	}
	if existed {
		return &job, nil
	}
	return nil, nil
}

func AddFinetuneJob(job *FinetuneJob) (bool, error) {
	job.CreatedTime = util.GetCurrentTime()
	job.UpdatedTime = job.CreatedTime
	err := insertRow(adapter.db, job)
	if err != nil {
		return false, err
	}
	return true, nil
}

func UpdateFinetuneJob(owner string, name string, job *FinetuneJob) (bool, error) {
	job.UpdatedTime = util.GetCurrentTime()
	job.Owner = owner
	job.Name = name
	err := adapter.db.Model(job).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}
