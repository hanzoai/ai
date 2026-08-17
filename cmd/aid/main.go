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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanzoai/ai"
	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
)

func main() {
	// The app, and with it the shared runtime boot (DB, model config,
	// balance/tier/rate-limit, billing queue) — the SAME sequence the unified cloud
	// binary runs via ai.Mount, because ai.App is what runs it. A failure is fatal
	// for the standalone server.
	//
	// No secret store: standalone resolves a provider's kms:// reference from the
	// environment, which is what it did when boot never set one.
	app, err := ai.App(nil)
	if err != nil {
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
	// ai.Handler is the same app this process serves, as an http.Handler. The MsgType
	// 200 gateway handler is BUILT around it, so every path the ad-hoc registry does
	// not claim reaches the same route table the :8000 surface serves — with its verb
	// and its path parameters intact. It is an argument, not a follow-up call:
	// without it the gateway 404s everything and says nothing.
	object.InitZap()
	controllers.InitZapHandlers(ai.Handler())

	// Inter-service ZAP transport for cloud operations (deploy, status, logs).
	// Listens on CLOUD_ZAP_PORT (default 9320), separate from inference node.
	controllers.InitInterserviceZap()

	// Register the canonical HIP-0110 HTTP-over-ZAP terminal (luxfi/zap/forward)
	// on the inference node so the ZAP gateway can route any HTTP request to the
	// full HTTP surface. ai.Handler is the app itself, so
	// every filter in the chain — including
	// the balance gate (BalanceGateFilter) and all auth/tenant filters — runs on
	// the bridged request before the route dispatches. Purely additive; the
	// :8000 HTTP path and the existing ZAP handlers (MsgType 100/200) are
	// untouched. Gated by ZAP_ENABLED via object.GetZapNode() returning nil.
	controllers.InitForwardBridge(ai.Handler())

	go object.ClearThroughputPerSecond()

	// The Enso router flywheel (probe + trainer) is launched inside ai.Bootstrap (run
	// above), the SINGLE shared boot sequence — so it is the ONE launch site and boots
	// identically in this standalone and the embedded unified binary. Both self-gate on
	// env and default OFF (ROUTER_TRAIN_ENABLED; ROUTER_PROBE_RPH+ROUTER_PROBE_TOKEN).
	// See ai.Bootstrap (HIP-510).

	// Serve the app on its own listener. The embedded cloud binary mounts the same
	// app; here the standalone owns the socket — and it is zip's listener, not a
	// net/http server wrapping it, so there is one server for one app.
	if err := app.Listen(fmt.Sprintf(":%v", port)); err != nil {
		log.Error("http server on :%v exited: %v", port, err)
	}
}
