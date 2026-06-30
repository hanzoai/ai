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
package controllers

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

// finetune.go is the `/v1/finetune/*` broker surface — the ONE API the console's
// Training page drives. It owns the whole lifecycle against the cluster training
// operator (hanzo-ml/trainer) and inference (hanzo-ml/kserve): browse/search
// HuggingFace, recommend efficient defaults, submit a real TrainJob, stream status,
// meter GPU-hours to commerce, and deploy a finished checkpoint to inference. All
// business logic lives in object/finetune_*.go; these handlers are the thin Beego
// MVC layer (mirror controllers/model_route.go): read the request, resolve the
// authed org, delegate, return the {status,msg,data} envelope.
//
// Org scoping is authoritative: GetEffectiveOrg() honors X-Org-Id only for the
// principal's own org or a global admin, so a caller can only ever touch their own
// org's jobs.

// createFinetuneRequest is the new-training-job payload from the console.
type createFinetuneRequest struct {
	DisplayName     string              `json:"displayName"`
	BaseModel       string              `json:"baseModel"` // HF repo id or hf://… / s3://…
	Method          string              `json:"method"`    // lora | qlora | full
	Task            string              `json:"task"`      // instruct | chat | completion
	Dataset         string              `json:"dataset"`   // HF dataset id or hf://… / s3://…
	Preset          string              `json:"preset"`    // recommended | balanced | aggressive | custom
	Hyperparams     *object.Hyperparams `json:"hyperparams"`
	GpuType         string              `json:"gpuType"`
	GpuCount        int                 `json:"gpuCount"`
	NumNodes        int                 `json:"numNodes"`
	DatasetExamples int                 `json:"datasetExamples"` // optional size hint for the time estimate
}

// finetuneOutputBase is the S3 root for checkpoint output (org's Space). Override
// with FINETUNE_OUTPUT_BASE.
func finetuneOutputBase() string {
	if v := strings.TrimRight(os.Getenv("FINETUNE_OUTPUT_BASE"), "/"); v != "" {
		return v
	}
	return "s3://hanzo-finetunes"
}

// slugify makes a DNS-1123-safe label segment from arbitrary text.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '/', r == '_', r == '.', r == ' ':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

// lastSegment returns the trailing path segment of an id (the model/repo name).
func lastSegment(id string) string {
	id = strings.TrimRight(id, "/")
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// orgKMSProject returns the org's KMS project id for org-scoped HF tokens, or ""
// to fall back to env + system KMS. The mapping convention is the org-scoped KMS
// project; absent a per-org mapping this stays empty (system token still resolves).
func (c *ApiController) orgKMSProject() string {
	return strings.TrimSpace(os.Getenv("FINETUNE_HF_KMS_PROJECT"))
}

// ── HuggingFace discovery ────────────────────────────────────────────────────

// SearchHfModels proxies a HuggingFace model search (base-model picker).
func (c *ApiController) SearchHfModels() {
	if _, ok := c.RequireSignedInUser(); !ok {
		return
	}
	query := c.Input().Get("q")
	task := c.Input().Get("task")
	limit, _ := strconv.Atoi(c.Input().Get("limit"))
	token := object.ResolveHfToken(c.orgKMSProject())
	models, err := object.SearchHfModels(query, task, limit, token)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(models)
}

// SearchHfDatasets proxies a HuggingFace dataset search (dataset picker).
func (c *ApiController) SearchHfDatasets() {
	if _, ok := c.RequireSignedInUser(); !ok {
		return
	}
	query := c.Input().Get("q")
	limit, _ := strconv.Atoi(c.Input().Get("limit"))
	token := object.ResolveHfToken(c.orgKMSProject())
	datasets, err := object.SearchHfDatasets(query, limit, token)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(datasets)
}

// GetHfRepo returns a repo's detail (files, gated/private state). ?id=&kind=model|dataset
func (c *ApiController) GetHfRepo() {
	if _, ok := c.RequireSignedInUser(); !ok {
		return
	}
	id := c.Input().Get("id")
	kind := c.Input().Get("kind")
	if kind == "" {
		kind = "model"
	}
	token := object.ResolveHfToken(c.orgKMSProject())
	info, err := object.GetHfRepoInfo(id, kind, token)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(info)
}

// ── Presets / recommendations ────────────────────────────────────────────────

// GetFinetunePresets returns the new-job catalog plus, when a selection is passed
// (?baseModel&method&task&preset[&datasetExamples]), the recommended config so the
// console can render "Recommended" as a one-click, ready-to-run default.
func (c *ApiController) GetFinetunePresets() {
	if _, ok := c.RequireSignedInUser(); !ok {
		return
	}
	catalog := object.GetFinetuneCatalog()
	baseModel := c.Input().Get("baseModel")
	if baseModel == "" {
		c.ResponseOk(map[string]any{"catalog": catalog})
		return
	}
	method := c.Input().Get("method")
	task := c.Input().Get("task")
	preset := c.Input().Get("preset")
	examples, _ := strconv.Atoi(c.Input().Get("datasetExamples"))
	rec := object.Recommend(baseModel, method, task, preset, examples)
	c.ResponseOk(map[string]any{"catalog": catalog, "recommendation": rec})
}

// ── Job lifecycle ────────────────────────────────────────────────────────────

// ListFinetuneJobs returns the org's jobs, refreshing live status for active ones.
func (c *ApiController) ListFinetuneJobs() {
	if _, ok := c.RequireSignedInUser(); !ok {
		return
	}
	org := c.GetEffectiveOrg()
	jobs, err := object.GetFinetuneJobs(org)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	lang := c.GetAcceptLanguage()
	for _, j := range jobs {
		if !j.IsTerminal() && j.CrName != "" {
			c.refreshFinetuneJob(j, lang)
		}
	}
	c.ResponseOk(jobs)
}

// GetFinetuneJob returns one job with refreshed live status. ?id=owner/name or ?name=
func (c *ApiController) GetFinetuneJob() {
	if _, ok := c.RequireSignedInUser(); !ok {
		return
	}
	org := c.GetEffectiveOrg()
	name := c.jobNameParam(org)
	job, err := object.GetFinetuneJob(org, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if job == nil {
		c.ResponseError(fmt.Sprintf("fine-tune job %q not found", name))
		return
	}
	if !job.IsTerminal() && job.CrName != "" {
		c.refreshFinetuneJob(job, c.GetAcceptLanguage())
	}
	c.ResponseOk(job)
}

// jobNameParam pulls the job name from ?name= or the owner/name form of ?id=.
func (c *ApiController) jobNameParam(org string) string {
	if n := c.Input().Get("name"); n != "" {
		return n
	}
	id := c.Input().Get("id")
	if strings.Contains(id, "/") {
		return id[strings.LastIndex(id, "/")+1:]
	}
	return id
}

// CreateFinetuneJob validates the request, resolves efficient defaults, persists the
// job, and submits a real TrainJob CR. A submit failure (e.g. no cluster wired) is
// surfaced honestly: the job is saved with status "failed" + the reason, never faked.
func (c *ApiController) CreateFinetuneJob() {
	user, ok := c.RequireSignedInUser()
	if !ok {
		return
	}
	org := c.GetEffectiveOrg()

	var req createFinetuneRequest
	if err := json.Unmarshal(c.Ctx.Input.RequestBody, &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	req.BaseModel = strings.TrimSpace(req.BaseModel)
	req.Dataset = strings.TrimSpace(req.Dataset)
	if req.BaseModel == "" {
		c.ResponseError("a base model is required")
		return
	}
	if req.Dataset == "" {
		c.ResponseError("a dataset is required")
		return
	}

	// Resolve efficient defaults (the "Recommended" brain), then apply any explicit
	// overrides from the request.
	rec := object.Recommend(req.BaseModel, req.Method, req.Task, req.Preset, req.DatasetExamples)
	hp := rec.Hyperparams
	if req.Hyperparams != nil {
		hp = *req.Hyperparams
	}
	hpJSON, _ := json.Marshal(hp)

	gpuType := firstNonEmptyStr(req.GpuType, rec.GpuType)
	gpuCount := req.GpuCount
	if gpuCount <= 0 {
		gpuCount = rec.GpuCount
	}
	numNodes := req.NumNodes
	if numNodes <= 0 {
		numNodes = rec.NumNodes
	}
	method := req.Method
	if method == "" {
		method = "qlora"
	}

	name := slugify(lastSegment(req.BaseModel)) + "-" + strings.ToLower(util.GetRandomName())
	name = strings.Trim(name, "-")
	ns := object.FinetuneNamespace(org)
	outputUri := fmt.Sprintf("%s/%s/%s/", finetuneOutputBase(), slugify(org), name)

	job := &object.FinetuneJob{
		Owner:       org,
		Name:        name,
		CreatedBy:   user.Name,
		DisplayName: firstNonEmptyStr(req.DisplayName, req.BaseModel),
		BaseModel:   req.BaseModel,
		Method:      method,
		Task:        firstNonEmptyStr(req.Task, "instruct"),
		Dataset:     req.Dataset,
		Preset:      firstNonEmptyStr(req.Preset, "recommended"),
		Hyperparams: string(hpJSON),
		Runtime:     rec.Runtime,
		GpuType:     gpuType,
		GpuCount:    gpuCount,
		NumNodes:    numNodes,
		Namespace:   ns,
		CrName:      name,
		OutputUri:   outputUri,
		Status:      "pending",
	}

	if _, err := object.AddFinetuneJob(job); err != nil {
		c.ResponseError(err.Error())
		return
	}

	// Submit the real CR. Resolve an HF token (KMS, never the browser) so private
	// base models/datasets pull.
	token := object.ResolveHfToken(c.orgKMSProject())
	lang := c.GetAcceptLanguage()
	if err := object.SubmitFinetuneTrainJob(job, hp, token, lang); err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		_, _ = object.UpdateFinetuneJob(job.Owner, job.Name, job)
		// Return the saved job (with the honest failure) rather than a bare error,
		// so the console shows the run row + the precise reason.
		c.ResponseOk(job)
		return
	}
	// Queued — StartedTime is set later from the CR's Running condition (in
	// refreshFinetuneJob), so GPU-hour metering never counts queue time.
	job.Status = "queued"
	_, _ = object.UpdateFinetuneJob(job.Owner, job.Name, job)
	c.ResponseOk(job)
}

// CancelFinetuneJob deletes the TrainJob CR, meters the GPU-hours used so far, and
// marks the job cancelled. ?id= or ?name=
func (c *ApiController) CancelFinetuneJob() {
	if _, ok := c.RequireSignedInUser(); !ok {
		return
	}
	org := c.GetEffectiveOrg()
	name := c.jobNameParam(org)
	job, err := object.GetFinetuneJob(org, name)
	if err != nil || job == nil {
		c.ResponseError(fmt.Sprintf("fine-tune job %q not found", name))
		return
	}
	lang := c.GetAcceptLanguage()
	if job.CrName != "" {
		if derr := object.DeleteFinetuneTrainJob(job, lang); derr != nil {
			c.ResponseError(derr.Error())
			return
		}
	}
	job.FinishedTime = time.Now().Format(time.RFC3339)
	job.Status = "cancelled"
	c.meterFinetuneTerminal(job)
	if _, err := object.UpdateFinetuneJob(job.Owner, job.Name, job); err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(job)
}

// DeployFinetuneJob serves a completed job's checkpoints and registers the result
// as a routable model on api.hanzo.ai. ?id= or ?name=
func (c *ApiController) DeployFinetuneJob() {
	if _, ok := c.RequireSignedInUser(); !ok {
		return
	}
	org := c.GetEffectiveOrg()
	name := c.jobNameParam(org)
	job, err := object.GetFinetuneJob(org, name)
	if err != nil || job == nil {
		c.ResponseError(fmt.Sprintf("fine-tune job %q not found", name))
		return
	}
	res, derr := object.DeployFinetuneJob(job, c.GetAcceptLanguage())
	if derr != nil {
		c.ResponseError(derr.Error())
		return
	}
	job.DeployedModel = res.ModelId
	job.DeployUrl = res.Url
	if _, err := object.UpdateFinetuneJob(job.Owner, job.Name, job); err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(res)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// refreshFinetuneJob reads live TrainJob status into the job, meters GPU-hours on
// the first transition to a terminal state, and persists any change. Best-effort:
// a cluster error leaves the last-known status untouched (no fabrication).
func (c *ApiController) refreshFinetuneJob(job *object.FinetuneJob, lang string) {
	st, err := object.GetFinetuneTrainJobStatus(job, lang)
	if err != nil || st == nil {
		return
	}
	changed := false
	if !st.Found {
		// CR gone but our record is still active → it was deleted out-of-band.
		if !job.IsTerminal() {
			job.Status = "failed"
			job.Error = firstNonEmptyStr(job.Error, "training job no longer exists in the cluster")
			changed = true
		}
	} else {
		if st.Status != "" && st.Status != job.Status {
			job.Status = st.Status
			changed = true
		}
		if st.Progress > job.Progress {
			job.Progress = st.Progress
			changed = true
		}
		if st.Message != "" && st.Message != job.Message {
			job.Message = st.Message
			changed = true
		}
		if st.Started != "" && job.StartedTime == "" {
			job.StartedTime = st.Started
			changed = true
		}
		if st.Finished != "" && job.FinishedTime == "" {
			job.FinishedTime = st.Finished
			changed = true
		}
	}
	if job.IsTerminal() && !job.Metered {
		if c.meterFinetuneTerminal(job) {
			changed = true
		}
	}
	if changed {
		_, _ = object.UpdateFinetuneJob(job.Owner, job.Name, job)
	}
}

// meterFinetuneTerminal bills the GPU-hours a finished run consumed, once. Returns
// true if it mutated the job. A commerce error leaves Metered=false so a later
// refresh retries (mirrors transaction.go's retry posture).
func (c *ApiController) meterFinetuneTerminal(job *object.FinetuneJob) bool {
	if job.Metered {
		return false
	}
	start := firstNonEmptyStr(job.StartedTime, job.CreatedTime)
	finish := firstNonEmptyStr(job.FinishedTime, time.Now().Format(time.RFC3339))
	gpuSeconds := object.GpuSecondsForRun(start, finish, job.GpuCount, job.NumNodes)
	if gpuSeconds <= 0 {
		job.Metered = true
		return true
	}
	subject := job.Owner + "/" + firstNonEmptyStr(job.CreatedBy, "admin")
	cents, err := object.MeterFinetuneGpuHours(subject, gpuSeconds, job.GpuType)
	if err != nil {
		// Leave Metered=false to retry on a later refresh.
		return false
	}
	job.GpuSeconds += gpuSeconds
	job.CostCents += cents
	job.Metered = true
	return true
}

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
