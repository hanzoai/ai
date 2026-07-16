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
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/util"
	"github.com/hanzoai/beego"
	"github.com/hanzoai/beego/logs"
	_ "github.com/hanzoai/beego/session/redis"
)

func main() {
	// Shared AI runtime bootstrap (DB, model config, balance/tier/rate-limit,
	// beego filter chain, billing queue) — the SAME sequence the unified
	// cloud binary runs via ai.Mount, defined once in ai.Bootstrap. It ends
	// by publishing beego.BeeApp.Handlers via ai.SetHandler. A bootstrap
	// failure is fatal for the standalone server.
	if err := ai.Bootstrap(); err != nil {
		panic(err)
	}
	rlInstance := ai.RateLimiter()
	bq := ai.BillingQueue()

	port := beego.AppConfig.DefaultInt("httpport", 8000)

	// Standalone-only: free the legacy beego port before binding it. The
	// embedded binary serves beego through zip and never listens here, so
	// this lives in main(), not Bootstrap.
	if err := util.StopOldInstance(port); err != nil {
		panic(err)
	}

	// Graceful shutdown: drain billing queue and stop rate limiter.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		logs.Info("Received %v, shutting down...", sig)

		if rlInstance != nil {
			rlInstance.Stop()
			allowed, denied := rlInstance.Metrics()
			logs.Info("Rate limiter stopped (total_allowed=%d total_denied=%d)", allowed, denied)
		}

		if bq != nil {
			remaining := bq.Shutdown()
			if remaining > 0 {
				logs.Error("Billing queue shutdown: %d records could not be delivered", remaining)
			} else {
				logs.Info("Billing queue drained successfully")
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
	object.InitZap()
	controllers.InitZapHandlers()

	// Inter-service ZAP transport for cloud operations (deploy, status, logs).
	// Listens on CLOUD_ZAP_PORT (default 9320), separate from inference node.
	controllers.InitInterserviceZap()

	// (ai.SetHandler(beego.BeeApp.Handlers) already ran inside ai.Bootstrap —
	// the beego ControllerRegister is published once, there.)

	// Register the canonical HIP-0110 HTTP-over-ZAP terminal (luxfi/zap/forward)
	// on the inference node so the ZAP gateway can route any HTTP request to the
	// full beego surface. beego.BeeApp.Handlers is the fully-wrapped
	// ControllerRegister: every BeforeRouter filter inserted above — including
	// the balance gate (BalanceGateFilter) and all auth/tenant filters — runs on
	// the bridged request before the route dispatches. Purely additive; the
	// :8000 HTTP path and the existing ZAP handlers (MsgType 100/200) are
	// untouched. Gated by ZAP_ENABLED via object.GetZapNode() returning nil.
	controllers.InitForwardBridge(beego.BeeApp.Handlers)

	go object.ClearThroughputPerSecond()

	// Router self-probe: a slow, tagged trickle of real auto-routed requests
	// against our own /v1 so the enso training ledger accumulates continuously.
	// Off unless ROUTER_PROBE_RPH + ROUTER_PROBE_TOKEN are set.
	controllers.StartRouterProbe()

	beego.Run(fmt.Sprintf(":%v", port))
}
