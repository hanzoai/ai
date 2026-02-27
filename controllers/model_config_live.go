// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
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
	"net/http"
	"strings"
	"time"

	"github.com/beego/beego/logs"
)

// backgroundRefresh is a long-running goroutine that periodically refreshes
// pricing data from pricing.hanzo.ai. It runs only when live_mode is true.
func (mc *ModelConfig) backgroundRefresh() {
	mc.mu.RLock()
	ttl := mc.pricingTTL
	mc.mu.RUnlock()

	if ttl <= 0 {
		ttl = 6 * time.Hour
	}

	// Do an initial fetch immediately
	mc.fetchLivePricing()

	ticker := time.NewTicker(ttl)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			mc.fetchLivePricing()
		case <-mc.stopCh:
			return
		}
	}
}

// livePricingResponse is the expected response from pricing.hanzo.ai.
type livePricingResponse struct {
	Models []livePricingModel `json:"models"`
}

type livePricingModel struct {
	Name    string           `json:"name"`
	Pricing livePricingEntry `json:"pricing"`
}

type livePricingEntry struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// fetchLivePricing fetches current pricing from the pricing service and
// merges it into the runtime config. Only overwrites pricing for models
// that exist in the response — never removes existing entries.
func (mc *ModelConfig) fetchLivePricing() {
	mc.mu.RLock()
	url := mc.pricingURL
	mc.mu.RUnlock()

	if url == "" {
		return
	}

	url = strings.TrimRight(url, "/") + "/v1/pricing/models"

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		logs.Warn("Live pricing fetch failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logs.Warn("Live pricing returned status %d", resp.StatusCode)
		return
	}

	var result livePricingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logs.Warn("Live pricing parse failed: %v", err)
		return
	}

	if len(result.Models) == 0 {
		logs.Info("Live pricing: no models in response, keeping current data")
		return
	}

	// Merge live pricing into existing map
	mc.mu.Lock()
	updated := 0
	for _, m := range result.Models {
		key := strings.ToLower(m.Name)
		if m.Pricing.Input > 0 || m.Pricing.Output > 0 {
			mc.pricing[key] = modelPrice{
				InputPerMillion:  m.Pricing.Input,
				OutputPerMillion: m.Pricing.Output,
			}
			updated++
		}
	}
	mc.lastPricingAt = time.Now()
	mc.mu.Unlock()

	logs.Info("Live pricing refreshed: %d models updated from %s", updated, url)
}

// LastPricingRefresh returns when pricing was last refreshed from live source.
func (mc *ModelConfig) LastPricingRefresh() time.Time {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.lastPricingAt
}

// Stop signals the background refresh goroutine to exit.
func (mc *ModelConfig) Stop() {
	select {
	case mc.stopCh <- struct{}{}:
	default:
	}
}

// Status returns a human-readable status string for diagnostics.
func (mc *ModelConfig) Status() string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	liveStr := "disabled"
	if mc.features.LiveMode {
		if mc.lastPricingAt.IsZero() {
			liveStr = "enabled (never fetched)"
		} else {
			liveStr = fmt.Sprintf("enabled (last: %s)", mc.lastPricingAt.Format(time.RFC3339))
		}
	}

	return fmt.Sprintf("routes=%d pricing=%d prompts=%d live=%s",
		len(mc.routes), len(mc.pricing), len(mc.prompts), liveStr)
}
