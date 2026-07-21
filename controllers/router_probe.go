// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/log"
)

// ── Router self-probe ────────────────────────────────────────────────────────
//
// The enso training flywheel needs a steady trickle of labeled routing data
// even when organic `auto` traffic is quiet. The probe SLOWLY exercises the
// gateway's own public path end-to-end — a real HTTP POST /v1/chat/completions
// with model=auto against our own listen address — so every layer that real
// traffic crosses (authz, routing, provider, billing, usage row, o11y span,
// RoutingEvent) fires exactly as in production, then scores the outcome via
// POST /v1/feedback. Nothing is simulated; the data is real by
// construction and HONESTLY TAGGED so it can be filtered anywhere:
//   - the credential is a dedicated service key (the probe's own identity —
//     spend lands on the internal org that owns the key, never a customer),
//   - User-Agent: hanzo-router-probe/1.
//
// OFF unless configured (both are required):
//
//	ROUTER_PROBE_RPH   — requests per HOUR (float; clamped to [1, 120]; the
//	                     default posture is "slowly": 12 ≈ one every 5 min)
//	ROUTER_PROBE_TOKEN — the service bearer the probe presents
//	ROUTER_PROBE_URL   — optional; default http://127.0.0.1:8000 (self)
//
// Cost is bounded by construction: max_tokens 64, X-Max-Cost $0.005/1k, and
// the hourly clamp — worst case ~14¢/day at the ceiling rate on default rates.
const probeUserAgent = "hanzo-router-probe/1"

// probeCorpus spans the router task tags (classify.go) so the ledger
// accumulates decisions across the whole task space, not one lane. Synthetic,
// content-free prompts — safe to store, safe to export.
var probeCorpus = []string{
	"Write a Go function that reverses a linked list in place.",                            // code
	"If a train leaves at 3pm at 80km/h and another at 4pm at 100km/h, when do they meet?", // math
	"Explain step by step why the sky appears blue at noon but red at sunset.",             // reasoning
	"Write a four-line poem about the sea in the voice of a lighthouse keeper.",            // creative
	"hi! how are you today?", // cheap_chat
	"Summarize the trade-offs between event sourcing and CRUD persistence for a ledger.", // reasoning
	"Refactor this Python: for i in range(len(xs)): print(xs[i]) — make it idiomatic.",   // code
	"What is 17% of 2,340, and what is the result rounded to the nearest ten?",           // math
	"Draft a two-sentence product blurb for a solar-powered e-ink weather display.",      // creative
	"List three practical differences between TCP and QUIC for a mobile chat app.",       // cheap_chat/reasoning
}

// StartRouterProbe launches the background self-probe loop when configured.
// Called once at boot (cmd/aid). Never fails boot: misconfiguration logs and
// disables. The loop jitters ±20% so probes never synchronize across replicas.
func StartRouterProbe() {
	rph, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv("ROUTER_PROBE_RPH")), 64)
	token := strings.TrimSpace(os.Getenv("ROUTER_PROBE_TOKEN"))
	if rph <= 0 || token == "" {
		log.Info("router probe: disabled (set ROUTER_PROBE_RPH + ROUTER_PROBE_TOKEN to enable)")
		return
	}
	if rph < 1 {
		rph = 1
	}
	if rph > 120 {
		rph = 120
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("ROUTER_PROBE_URL")), "/")
	if base == "" {
		base = "http://127.0.0.1:8000"
	}
	interval := time.Duration(float64(time.Hour) / rph)
	log.Info("router probe: enabled — %.1f req/h (every ~%s) against %s", rph, interval.Round(time.Second), base)
	go probeLoop(base, token, interval)
}

func probeLoop(base, token string, interval time.Duration) {
	hc := &http.Client{Timeout: 90 * time.Second}
	for i := 0; ; i++ {
		// ±20% jitter so multi-replica deployments spread out.
		j := time.Duration((rand.Float64()*0.4 - 0.2) * float64(interval))
		time.Sleep(interval + j)
		prompt := probeCorpus[i%len(probeCorpus)]
		if err := probeOnce(hc, base, token, prompt); err != nil {
			log.Warning("router probe: %v", err)
		}
	}
}

// probeOnce sends one auto-routed chat turn and scores it. The reward is the
// honest boolean the probe can actually observe — the request succeeded and
// produced non-empty content (1.0) or it did not (0.0) — never a fabricated
// quality judgment. Latency, cost, task, and model already ride the
// RoutingEvent + usage row; richer rewards come from real feedback signals.
func probeOnce(hc *http.Client, base, token, prompt string) error {
	body, _ := json.Marshal(map[string]any{
		"model":      "auto",
		"max_tokens": 64,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", probeUserAgent)
	req.Header.Set("X-Max-Cost", "0.005")

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(raw, &out)
	reward := probeReward(resp.StatusCode, &out)
	requestID := strings.TrimPrefix(out.ID, "chatcmpl-")
	if requestID == "" {
		// No id → nothing to key the reward to; the error itself was still
		// recorded by the gateway's own error metering.
		return fmt.Errorf("chat status %d, no response id", resp.StatusCode)
	}
	rb, _ := json.Marshal(map[string]any{"requestId": requestID, "outcome": reward})
	rreq, err := http.NewRequest(http.MethodPost, base+"/v1/feedback", bytes.NewReader(rb))
	if err != nil {
		return err
	}
	rreq.Header.Set("Authorization", "Bearer "+token)
	rreq.Header.Set("Content-Type", "application/json")
	rreq.Header.Set("User-Agent", probeUserAgent)
	rresp, err := hc.Do(rreq)
	if err != nil {
		return fmt.Errorf("reward: %w", err)
	}
	defer rresp.Body.Close()
	_, _ = io.Copy(io.Discard, rresp.Body)
	return nil
}

// probeReward is the pure scoring rule: 1 for a 2xx with non-empty content,
// 0 otherwise. Deliberately boolean — the only judgment the probe can honestly
// make about its own synthetic turn.
func probeReward(status int, out *struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
},
) float64 {
	if status >= 200 && status < 300 && out != nil && len(out.Choices) > 0 &&
		strings.TrimSpace(out.Choices[0].Message.Content) != "" {
		return 1.0
	}
	return 0.0
}
