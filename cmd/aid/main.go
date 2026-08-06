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

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanzoai/ai"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/routers"
	"github.com/hanzoai/ai/util"
)

func main() {
	// Shared AI runtime bootstrap (DB, model config, balance/tier/rate-limit,
	// router filter chain, billing queue) — the SAME sequence the unified
	// cloud binary runs via ai.Mount, defined once in ai.Bootstrap. It ends
	// by publishing routers.App via ai.SetHandler. A bootstrap
	// failure is fatal for the standalone server.
	if err := ai.Bootstrap(); err != nil {
		panic(err)
	}
	rlInstance := ai.RateLimiter()
	bq := ai.BillingQueue()

	port := conf.AppConfig.DefaultInt("httpport", 8000)

	// Standalone-only: free the legacy HTTP port before binding it. The
	// embedded binary serves routers.App through zip and never listens here, so
	// this lives in main(), not Bootstrap.
	if err := util.StopOldInstance(port); err != nil {
		panic(err)
	}

	// Graceful shutdown: drain billing queue and stop rate limiter.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info("Received %v, shutting down...", sig)

		if rlInstance != nil {
			rlInstance.Stop()
			allowed, denied := rlInstance.Metrics()
			log.Info("Rate limiter stopped (total_allowed=%d total_denied=%d)", allowed, denied)
		}

		if bq != nil {
			remaining := bq.Shutdown()
			if remaining > 0 {
				log.Error("Billing queue shutdown: %d records could not be delivered", remaining)
			} else {
				log.Info("Billing queue drained successfully")
			}
		}

		controllers.StopInterserviceZap()
		object.StopZap()

		// Flush any buffered OTel GenAI spans before exit (no-op when telemetry
		// is disabled). Bounded so shutdown never hangs on a slow collector.
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		object.ShutdownTelemetry(flushCtx)
		flushCancel()

		os.Exit(0)
	}()

	// Initialize ZAP node for native binary protocol.
	// Listens on port 9999, connects to KV/SQL peers.
	//
	// routers.App is the fully-wrapped native router (its filters were installed by
	// ai.Bootstrap above). The MsgType 200 gateway handler is BUILT around it, so
	// every path the ad-hoc registry does not claim reaches the same route table the
	// :8000 surface serves — with its verb and its path parameters intact. It is an
	// argument, not a follow-up call: without it the gateway 404s everything and
	// says nothing.
	object.InitZap()
	controllers.InitZapHandlers(routers.App)

	// Inter-service ZAP transport for cloud operations (deploy, status, logs).
	// Listens on CLOUD_ZAP_PORT (default 9320), separate from inference node.
	controllers.InitInterserviceZap()

	// (ai.SetHandler(routers.App) already ran inside ai.Bootstrap —
	// the native router is published once, there.)

	// Register the canonical HIP-0110 HTTP-over-ZAP terminal (luxfi/zap/forward)
	// on the inference node so the ZAP gateway can route any HTTP request to the
	// full HTTP surface. routers.App is the fully-wrapped native router:
	// every BeforeRouter filter inserted above — including
	// the balance gate (BalanceGateFilter) and all auth/tenant filters — runs on
	// the bridged request before the route dispatches. Purely additive; the
	// :8000 HTTP path and the existing ZAP handlers (MsgType 100/200) are
	// untouched. Gated by ZAP_ENABLED via object.GetZapNode() returning nil.
	controllers.InitForwardBridge(routers.App)

	go object.ClearThroughputPerSecond()

	// The Enso router flywheel (probe + trainer) is launched inside ai.Bootstrap (run
	// above), the SINGLE shared boot sequence — so it is the ONE launch site and boots
	// identically in this standalone and the embedded unified binary. Both self-gate on
	// env and default OFF (ROUTER_TRAIN_ENABLED; ROUTER_PROBE_RPH+ROUTER_PROBE_TOKEN).
	// See ai.Bootstrap (HIP-510).

	// Serve the native router directly. The embedded cloud binary serves the
	// same routers.App through zip; here the standalone owns the listener.
	srv := &http.Server{Addr: fmt.Sprintf(":%v", port), Handler: routers.App}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("http server on :%v exited: %v", port, err)
	}
}
