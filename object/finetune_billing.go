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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/ai/util"
)

// GpuHourlyCents maps a GPU SKU to its metered price in cents per GPU-hour. This
// is the ONE rate table — the same map drives the cost ESTIMATE shown in the
// console (finetune_runtime.go EstimateCost) and the ACTUAL GPU-hours charge
// posted to commerce here, so a quote and a bill never diverge.
var GpuHourlyCents = map[string]int64{
	"nvidia-rtx-4090":  50,
	"nvidia-l40s":      110,
	"nvidia-a100-40gb": 150,
	"nvidia-a100-80gb": 200,
	"nvidia-h100-80gb": 350,
	"nvidia-h200":      450,
}

// gpuRateCents returns the per-GPU-hour price for a SKU, defaulting to the
// A100-80GB rate for an unknown SKU so an unpriced GPU never bills at zero.
func gpuRateCents(gpuType string) int64 {
	if c, ok := GpuHourlyCents[gpuType]; ok {
		return c
	}
	return GpuHourlyCents["nvidia-a100-80gb"]
}

// gpuSecondsCostCents converts metered GPU-seconds (wall-clock × GPUs × nodes)
// to a cent amount at the SKU rate. Rounds up to the nearest cent so sub-cent
// usage is never silently free.
func gpuSecondsCostCents(gpuSeconds int64, gpuType string) int64 {
	rate := gpuRateCents(gpuType)
	// cents = gpuSeconds * rate / 3600, rounded up.
	cents := (gpuSeconds*rate + 3599) / 3600
	if cents < 0 {
		return 0
	}
	return cents
}

// MeterFinetuneGpuHours posts a GPU-hours charge for a finetune run to commerce,
// mirroring object/transaction.go AddTransactionForMessage (the ONE metering
// path → `POST {commerceEndpoint}/v1/billing/usage`, amount in cents). Returns
// the charged amount in cents. A no-op (0, nil) when commerce is unconfigured or
// the amount rounds to zero, so an un-metered cluster still runs jobs.
//
// subject is the canonical billing subject ("owner/name"); gpuSeconds is the
// total GPU-seconds consumed (wall-clock × gpuCount × numNodes).
func MeterFinetuneGpuHours(subject string, gpuSeconds int64, gpuType string) (int64, error) {
	amountCents := gpuSecondsCostCents(gpuSeconds, gpuType)
	if amountCents <= 0 {
		return 0, nil
	}
	endpoint, token, client := commerceClient()
	if endpoint == "" {
		return 0, fmt.Errorf("commerceEndpoint is not configured")
	}
	payload := map[string]interface{}{
		"user":      subject,
		"currency":  "usd",
		"amount":    amountCents,
		"model":     "finetune:" + gpuType,
		"provider":  "finetune",
		"requestId": util.GetRandomName(),
		"premium":   true,
		"stream":    false,
		"status":    "success",
		"metadata": map[string]interface{}{
			"kind":       "gpu-hours",
			"gpuSeconds": gpuSeconds,
			"gpuType":    gpuType,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal gpu-hours usage: %w", err)
	}
	url := endpoint + "/v1/billing/usage"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("failed to build gpu-hours usage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to post gpu-hours usage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("commerce returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return amountCents, nil
}

// GpuSecondsForRun computes total GPU-seconds for a run from its Started/Finished
// timestamps and parallelism (gpuCount × numNodes). Returns 0 when the run never
// started or the timestamps are unparseable, so metering fails safe (no phantom
// charge). finishedRFC3339 may be empty to mean "now".
func GpuSecondsForRun(startedRFC3339, finishedRFC3339 string, gpuCount, numNodes int) int64 {
	if startedRFC3339 == "" {
		return 0
	}
	start, err := time.Parse(time.RFC3339, startedRFC3339)
	if err != nil {
		return 0
	}
	end := time.Now()
	if finishedRFC3339 != "" {
		if t, perr := time.Parse(time.RFC3339, finishedRFC3339); perr == nil {
			end = t
		}
	}
	wall := int64(end.Sub(start).Seconds())
	if wall <= 0 {
		return 0
	}
	parallel := gpuCount * numNodes
	if parallel < 1 {
		parallel = 1
	}
	return wall * int64(parallel)
}
