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

package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/beego/beego"
	"github.com/beego/beego/logs"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/proxy"
	"github.com/hanzoai/ai/routers"
	"github.com/hanzoai/ai/util"
)

// Bootstrap performs the AI runtime initialization shared by BOTH
// entrypoints — the standalone server (cmd/aid) and the embedded unified
// cloud binary (Mount, per HIP-0106). It is the SINGLE source of the
// runtime boot sequence: DB + adapter + tables, model/pricing config, the
// HTTP client, GeoIP/parser, the background maintenance tasks, the
// Commerce-backed balance gate + tier cache + per-key rate limiter, the
// full beego BeforeRouter/AfterExec filter chain, the session config, and
// the billing usage queue. After wiring those it publishes the
// fully-configured beego ControllerRegister via SetHandler so the unified
// binary's /v1/ai/* adapter stops returning 503 "ai runtime not
// initialized".
//
// What Bootstrap deliberately does NOT do (those are standalone-only
// concerns that the embedded binary owns differently, and several would
// break or collide when co-resident in the unified process):
//
//   - It binds NO listeners. The native ZAP inference node (port 9999),
//     the inter-service ZAP transport (CLOUD_ZAP_PORT, default 9320) and
//     beego.Run(:httpport) stay in cmd/aid. The unified binary serves the
//     beego handler through zip on its own :8080 and runs its own ZAP at
//     :9653; starting the legacy nodes here would collide.
//   - It calls NO util.StopOldInstance — that races on the legacy beego
//     port and is meaningless when beego never listens.
//   - It installs NO signal handler and never calls os.Exit. cloud.Serve
//     owns graceful shutdown for the unified binary; cmd/aid keeps its own
//     drain goroutine, wired to the handles returned here.
//
// Bootstrap is guarded by sync.Once: it is safe to call more than once, so
// Mount can call it at mount time and the standalone can call it explicitly
// without double-initializing global runtime state. The cached result
// (including the error) is returned on every call.
//
// It returns an error rather than panicking: several init steps (notably
// the DB open) panic deep inside on a missing/unreachable backend, which
// would take down the whole multi-subsystem cloud binary. Bootstrap
// recovers those into a precise error so Mount can surface
// "mount ai: bootstrap: ..." and the operator sees exactly what failed.
//
// The rate limiter and billing queue created here are exposed via
// RateLimiter() and BillingQueue() for the standalone's graceful-drain
// wiring. Either may be nil (e.g. the billing queue is nil when no Commerce
// endpoint is configured).
func Bootstrap() error {
	bootstrapOnce.Do(func() {
		bootErr = doBootstrap()
	})
	return bootErr
}

// RateLimiter returns the per-key rate limiter created by Bootstrap, or nil
// if Bootstrap has not run (or failed before creating it).
func RateLimiter() *routers.RateLimiter { return bootRateLimiter }

// BillingQueue returns the Commerce billing usage queue created by
// Bootstrap, or nil if none was configured / Bootstrap did not reach it.
func BillingQueue() *util.BillingQueue { return bootBillingQueue }

var (
	bootstrapOnce    sync.Once
	bootErr          error
	bootRateLimiter  *routers.RateLimiter
	bootBillingQueue *util.BillingQueue
)

func doBootstrap() (err error) {
	// Convert the deep DB-open panic (and any other init panic) into a
	// precise error so a missing backend fails the AI mount cleanly instead
	// of crashing the entire cloud process.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ai: bootstrap: %v", r)
		}
	}()

	// Persistence: flags, ORM adapter, schema, DB connection.
	object.InitFlag()
	object.InitAdapter()
	object.CreateTables()
	object.InitDb()

	// Model routing/pricing config from YAML. Non-fatal: static fallback.
	configPath := conf.GetConfigString("modelConfigPath")
	if configPath == "" {
		configPath = "conf/models.yaml"
	}
	if err := controllers.InitModelConfig(configPath); err != nil {
		logs.Warn("Model config: %v (using static fallback)", err)
	}

	// Provider HTTP client, GeoIP, request parsing, maintenance tasks.
	proxy.InitHttpClient()
	util.InitMaxmindFiles()
	util.InitIpDb()
	util.InitParser()
	object.InitCleanupChats()
	object.InitStoreCount()
	object.InitCommitRecordsTask()
	object.InitScanJobProcessor()
	object.InitMessageTransactionRetry()

	// Commerce balance gate (pre-request balance enforcement).
	routers.InitBalanceGate()

	// Commerce-backed tier cache — must precede InitRateLimiter so
	// DefaultTierFunc can resolve tiers through it.
	routers.InitTierCache()

	// Per-key rate limiting (env override → tier cache → zen-free).
	bootRateLimiter = routers.InitRateLimiter(routers.DefaultTierFunc)
	logs.Info("Per-key rate limiter initialized (tiers: free=10/min, starter=60/min, pro=300/min, enterprise=1000/min)")

	// beego filter chain — identical order to the standalone surface so
	// the embedded handler enforces the same auth/tenant/balance pipeline.
	beego.SetStaticPath("/swagger", "swagger")
	beego.InsertFilter("/v1/cloud/*", beego.BeforeRouter, routers.V1CloudRewriteFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.CorsFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.HstsFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.CacheControlFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.RateLimitFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.AutoSigninFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.BalanceGateFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.StaticFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.TenantContextFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.AuthzFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.PrometheusFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.RecordMessage)
	beego.InsertFilter("*", beego.AfterExec, routers.AfterRecordMessage, false)
	beego.InsertFilter("*", beego.AfterExec, routers.SecureCookieFilter, false)

	// Session config (file unless redisEndpoint set).
	beego.BConfig.WebConfig.Session.SessionOn = true
	beego.BConfig.WebConfig.Session.SessionName = "cloud_session_id"
	if conf.GetConfigString("redisEndpoint") == "" {
		beego.BConfig.WebConfig.Session.SessionProvider = "file"
		beego.BConfig.WebConfig.Session.SessionProviderConfig = "./tmp"
	} else {
		beego.BConfig.WebConfig.Session.SessionProvider = "redis"
		beego.BConfig.WebConfig.Session.SessionProviderConfig = conf.GetConfigString("redisEndpoint")
	}
	beego.BConfig.WebConfig.Session.SessionGCMaxLifetime = 3600 * 24 * 365
	// SameSite=Lax: CSRF protection while preserving compatibility.
	beego.BConfig.WebConfig.Session.SessionCookieSameSite = http.SameSiteLaxMode

	// Optional log adapter reconfig. Guarded: in the embedded binary there
	// is no conf/app.conf, so logConfig is empty — json.Unmarshal("") would
	// panic. Skip when unset and keep the default logger.
	if raw := conf.GetConfigString("logConfig"); raw != "" {
		logConfigMap := make(map[string]interface{})
		if err := json.Unmarshal([]byte(raw), &logConfigMap); err != nil {
			logs.Warn("logConfig parse failed: %v (keeping default logger)", err)
		} else {
			logAdapter := "file"
			if v, ok := logConfigMap["adapter"].(string); ok {
				logAdapter = v
			}
			if logAdapter == "console" {
				logs.Reset()
			}
			if err := logs.SetLogger(logAdapter, raw); err != nil {
				logs.Warn("logConfig SetLogger failed: %v (keeping default logger)", err)
			}
		}
	}

	// Commerce-backed billing usage queue (retried with backoff). nil when
	// no Commerce endpoint is configured.
	bootBillingQueue = controllers.InitBillingQueue()
	if bootBillingQueue != nil {
		logs.Info("Billing queue started (Commerce endpoint configured)")
	}

	// Publish the fully-wired beego ControllerRegister. Every BeforeRouter
	// filter inserted above — balance gate, auth, tenant — runs on each
	// request the unified binary forwards into /v1/ai/*. This is the call
	// whose absence made the unified binary 503 with
	// "ai runtime not initialized".
	SetHandler(beego.BeeApp.Handlers)

	return nil
}
