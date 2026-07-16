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
	"math"
	"strings"
)

// finetune_runtime.go is the "Unsloth-class defaults" brain: given a base model,
// a method (lora/qlora/full), a task and a preset level, it produces a complete,
// sane training configuration — the ClusterTrainingRuntime to reference,
// efficient recommended hyperparameters, a GPU SKU + count that actually fits the
// model, and a time/cost estimate. The console's "Recommended" preset is just
// this function: it should always produce something that runs well.
//
// One source of truth: the GPU rate table lives in finetune_billing.go and is
// used both for the ESTIMATE here and the real GPU-hours charge, so a quote and a
// bill never drift.

// Hyperparams is the efficient-default training configuration. It serializes to
// FinetuneJob.Hyperparams (JSON) and is rendered into the TrainJob trainer env in
// finetune_k8s.go.
type Hyperparams struct {
	Epochs                float64 `json:"epochs"`
	LearningRate          float64 `json:"learningRate"`
	BatchSize             int     `json:"batchSize"`
	GradAccum             int     `json:"gradAccum"`
	MaxSeqLen             int     `json:"maxSeqLen"`
	LoraRank              int     `json:"loraRank"`
	LoraAlpha             int     `json:"loraAlpha"`
	LoraDropout           float64 `json:"loraDropout"`
	Quant4bit             bool    `json:"quant4bit"`
	GradientCheckpointing bool    `json:"gradientCheckpointing"`
	WarmupRatio           float64 `json:"warmupRatio"`
	WeightDecay           float64 `json:"weightDecay"`
}

// BaseModelInfo is a suggested base model in the catalog.
type BaseModelInfo struct {
	Id          string  `json:"id"`          // canonical HF repo id
	DisplayName string  `json:"displayName"` // human label
	Family      string  `json:"family"`      // runtime family key
	ParamsB     float64 `json:"paramsB"`     // parameter count in billions
	Note        string  `json:"note,omitempty"`
}

// MethodInfo describes a fine-tuning method for the picker.
type MethodInfo struct {
	Id          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

// GpuOption is a selectable GPU SKU with its metered hourly price.
type GpuOption struct {
	Type        string `json:"type"`
	DisplayName string `json:"displayName"`
	Memory      string `json:"memory"`
	HourlyCents int64  `json:"hourlyCents"`
}

// FinetuneCatalog is the full option set the console renders in the new-job panel.
type FinetuneCatalog struct {
	BaseModels []BaseModelInfo `json:"baseModels"`
	Methods    []MethodInfo    `json:"methods"`
	Tasks      []string        `json:"tasks"`
	Presets    []string        `json:"presets"`
	Gpus       []GpuOption     `json:"gpus"`
}

// FinetuneRecommendation is the resolved configuration for a (base model, method,
// task, preset) selection — what "Recommended" fills in.
type FinetuneRecommendation struct {
	Runtime      string      `json:"runtime"`
	Hyperparams  Hyperparams `json:"hyperparams"`
	GpuType      string      `json:"gpuType"`
	GpuCount     int         `json:"gpuCount"`
	NumNodes     int         `json:"numNodes"`
	ParamsB      float64     `json:"paramsB"`
	EstMinutes   int         `json:"estMinutes"`
	EstCostCents int64       `json:"estCostCents"`
}

// curatedBaseModels are the flagship starter picks. Any HF repo id also works
// (ResolveBaseModel falls back to a generic runtime), but these have tuned,
// optimized ClusterTrainingRuntimes.
var curatedBaseModels = []BaseModelInfo{
	{Id: "meta-llama/Llama-3.2-1B-Instruct", DisplayName: "Llama 3.2 1B Instruct", Family: "llama3.2-1b", ParamsB: 1.2, Note: "Fast, cheap — great for a first run"},
	{Id: "meta-llama/Llama-3.2-3B-Instruct", DisplayName: "Llama 3.2 3B Instruct", Family: "llama3.2-3b", ParamsB: 3.2},
	{Id: "meta-llama/Llama-3.1-8B-Instruct", DisplayName: "Llama 3.1 8B Instruct", Family: "llama3.1-8b", ParamsB: 8.0, Note: "Best quality/cost for most tasks"},
	{Id: "Qwen/Qwen2.5-1.5B-Instruct", DisplayName: "Qwen2.5 1.5B Instruct", Family: "qwen2.5-1.5b", ParamsB: 1.5},
	{Id: "Qwen/Qwen2.5-7B-Instruct", DisplayName: "Qwen2.5 7B Instruct", Family: "qwen2.5-7b", ParamsB: 7.6},
	{Id: "mistralai/Mistral-7B-Instruct-v0.3", DisplayName: "Mistral 7B Instruct v0.3", Family: "mistral-7b", ParamsB: 7.2},
	{Id: "google/gemma-2-2b-it", DisplayName: "Gemma 2 2B IT", Family: "gemma2-2b", ParamsB: 2.6},
	{Id: "google/gemma-2-9b-it", DisplayName: "Gemma 2 9B IT", Family: "gemma2-9b", ParamsB: 9.2},
}

// FinetuneMethods are the supported methods.
func FinetuneMethods() []MethodInfo {
	return []MethodInfo{
		{Id: "qlora", DisplayName: "QLoRA (4-bit)", Description: "Most memory-efficient — fine-tune large models on a single GPU. Recommended default."},
		{Id: "lora", DisplayName: "LoRA", Description: "Low-rank adapters in 16-bit — fast, small artifacts, near-full quality."},
		{Id: "full", DisplayName: "Full fine-tune", Description: "Update all weights — highest quality, most compute."},
	}
}

// GpuOptions is the selectable GPU catalog, priced from the ONE rate table.
func GpuOptions() []GpuOption {
	return []GpuOption{
		{Type: "nvidia-rtx-4090", DisplayName: "RTX 4090", Memory: "24 GB", HourlyCents: GpuHourlyCents["nvidia-rtx-4090"]},
		{Type: "nvidia-l40s", DisplayName: "L40S", Memory: "48 GB", HourlyCents: GpuHourlyCents["nvidia-l40s"]},
		{Type: "nvidia-a100-40gb", DisplayName: "A100 40GB", Memory: "40 GB", HourlyCents: GpuHourlyCents["nvidia-a100-40gb"]},
		{Type: "nvidia-a100-80gb", DisplayName: "A100 80GB", Memory: "80 GB", HourlyCents: GpuHourlyCents["nvidia-a100-80gb"]},
		{Type: "nvidia-h100-80gb", DisplayName: "H100 80GB", Memory: "80 GB", HourlyCents: GpuHourlyCents["nvidia-h100-80gb"]},
		{Type: "nvidia-h200", DisplayName: "H200", Memory: "141 GB", HourlyCents: GpuHourlyCents["nvidia-h200"]},
	}
}

// GetFinetuneCatalog returns the full option set for the console new-job panel.
func GetFinetuneCatalog() FinetuneCatalog {
	return FinetuneCatalog{
		BaseModels: curatedBaseModels,
		Methods:    FinetuneMethods(),
		Tasks:      []string{"instruct", "chat", "completion"},
		Presets:    []string{"recommended", "balanced", "aggressive"},
		Gpus:       GpuOptions(),
	}
}

// normalizeMethod coerces a method to one of lora|qlora|full (default qlora — the
// most approachable, memory-efficient choice).
func normalizeMethod(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "lora":
		return "lora"
	case "full":
		return "full"
	default:
		return "qlora"
	}
}

// RuntimeFor returns the ClusterTrainingRuntime name for a method. There is ONE
// env-driven transformers+PEFT+TRL runtime per method (hanzo-ft-lora /
// hanzo-ft-qlora / hanzo-ft-full), defined in hanzo-ml/trainer
// (manifests/overlays/runtimes/hanzo). One image, configured entirely by the
// trainer env this broker sets (MODEL_DIR/DATASET_DIR/EPOCHS/LR/LORA_RANK/…), so
// it fine-tunes ANY HuggingFace causal LM — the base model + dataset are supplied
// by the TrainJob's initializers, not baked into the runtime. (familyForBaseModel
// is retained for GPU sizing + the catalog, not runtime selection.)
func RuntimeFor(baseModel, method string) string {
	return "hanzo-ft-" + normalizeMethod(method)
}

// paramsForBaseModel estimates the parameter count (billions) of an HF model from
// the catalog, else from a `NNb`/`NNB` token in the id, defaulting to 8B.
func paramsForBaseModel(baseModel string) float64 {
	for _, m := range curatedBaseModels {
		if strings.EqualFold(m.Id, baseModel) {
			return m.ParamsB
		}
	}
	id := strings.ToLower(baseModel)
	// Look for a "<num>b" token, e.g. "llama-3.1-8b", "qwen2.5-72b".
	for _, sz := range []struct {
		tok string
		b   float64
	}{
		{"70b", 70},
		{"72b", 72},
		{"34b", 34},
		{"32b", 32},
		{"14b", 14},
		{"13b", 13},
		{"9b", 9},
		{"8b", 8},
		{"7b", 7},
		{"3b", 3},
		{"2b", 2},
		{"1.5b", 1.5},
		{"1b", 1},
	} {
		if strings.Contains(id, sz.tok) {
			return sz.b
		}
	}
	return 8
}

// RecommendGpu picks a GPU SKU + count that fits the model for the given method.
// QLoRA (4-bit) fits far more per GPU than LoRA/full; full needs the most.
func RecommendGpu(paramsB float64, method string) (gpuType string, gpuCount, numNodes int) {
	method = normalizeMethod(method)
	switch method {
	case "qlora":
		switch {
		case paramsB <= 9:
			return "nvidia-a100-40gb", 1, 1
		case paramsB <= 34:
			return "nvidia-a100-80gb", 1, 1
		case paramsB <= 72:
			return "nvidia-h100-80gb", 2, 1
		default:
			return "nvidia-h100-80gb", 4, 1
		}
	case "lora":
		switch {
		case paramsB <= 3:
			return "nvidia-a100-40gb", 1, 1
		case paramsB <= 9:
			return "nvidia-a100-80gb", 1, 1
		case paramsB <= 34:
			return "nvidia-a100-80gb", 2, 1
		case paramsB <= 72:
			return "nvidia-h100-80gb", 4, 1
		default:
			return "nvidia-h100-80gb", 8, 1
		}
	default: // full
		switch {
		case paramsB <= 3:
			return "nvidia-a100-80gb", 1, 1
		case paramsB <= 9:
			return "nvidia-a100-80gb", 4, 1
		case paramsB <= 34:
			return "nvidia-h100-80gb", 8, 1
		default:
			return "nvidia-h100-80gb", 8, 2
		}
	}
}

// RecommendHyperparams returns efficient default hyperparameters for a method +
// preset level. "recommended" is the safe default; "balanced" trains a bit
// longer/hotter; "aggressive" pushes epochs + LR for small datasets.
func RecommendHyperparams(method, preset string) Hyperparams {
	method = normalizeMethod(method)
	hp := Hyperparams{
		Epochs:                3,
		LearningRate:          2e-4,
		BatchSize:             2,
		GradAccum:             8,
		MaxSeqLen:             2048,
		LoraRank:              16,
		LoraAlpha:             32,
		LoraDropout:           0.05,
		Quant4bit:             method == "qlora",
		GradientCheckpointing: true,
		WarmupRatio:           0.03,
		WeightDecay:           0.01,
	}
	if method == "full" {
		hp.LearningRate = 2e-5
		hp.BatchSize = 1
		hp.GradAccum = 16
		hp.Epochs = 2
		hp.LoraRank = 0
		hp.LoraAlpha = 0
		hp.LoraDropout = 0
	}
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "balanced":
		hp.Epochs += 1
	case "aggressive":
		hp.Epochs += 2
		hp.LearningRate *= 1.5
		if method != "full" {
			hp.LoraRank = 32
			hp.LoraAlpha = 64
		}
	}
	return hp
}

// EstimateMinutes is a coarse wall-clock estimate (minutes) for a run. It is a
// quote, not a promise: tokens ≈ examples × seqLen × epochs, divided by a
// per-GPU throughput that scales down with model size and up with 4-bit + GPU
// count. examples defaults to a typical instruction dataset when unknown.
func EstimateMinutes(paramsB float64, method string, hp Hyperparams, examples, gpuCount int) int {
	method = normalizeMethod(method)
	if examples <= 0 {
		examples = 10000
	}
	if gpuCount <= 0 {
		gpuCount = 1
	}
	seq := hp.MaxSeqLen
	if seq <= 0 {
		seq = 2048
	}
	epochs := hp.Epochs
	if epochs <= 0 {
		epochs = 1
	}
	tokens := float64(examples) * float64(seq) * epochs
	// Per-GPU tokens/sec: ~3M tok/s for a 1B model on an A100, scaling ~1/params.
	perGpu := 3.0e6 / math.Max(paramsB, 0.5)
	if hp.Quant4bit {
		perGpu *= 0.6 // 4-bit compute is slower per step but fits more
	}
	if method == "full" {
		perGpu *= 0.4 // full backward is much heavier than adapter-only
	}
	throughput := perGpu * float64(gpuCount) * 0.85 // 85% scaling efficiency
	seconds := tokens / math.Max(throughput, 1)
	minutes := int(math.Ceil(seconds/60)) + 3 // +3 for init/download/checkpointing
	if minutes < 5 {
		minutes = 5
	}
	return minutes
}

// Recommend resolves a complete configuration for a selection — the heart of the
// "Recommended" preset. examples is an optional dataset-size hint for the time
// estimate (0 = use a default).
func Recommend(baseModel, method, task, preset string, examples int) FinetuneRecommendation {
	method = normalizeMethod(method)
	paramsB := paramsForBaseModel(baseModel)
	hp := RecommendHyperparams(method, preset)
	gpuType, gpuCount, numNodes := RecommendGpu(paramsB, method)
	estMin := EstimateMinutes(paramsB, method, hp, examples, gpuCount*numNodes)
	estCost := gpuSecondsCostCents(int64(estMin)*60*int64(gpuCount*numNodes), gpuType)
	return FinetuneRecommendation{
		Runtime:      RuntimeFor(baseModel, method),
		Hyperparams:  hp,
		GpuType:      gpuType,
		GpuCount:     gpuCount,
		NumNodes:     numNodes,
		ParamsB:      paramsB,
		EstMinutes:   estMin,
		EstCostCents: estCost,
	}
}
