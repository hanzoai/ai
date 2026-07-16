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
	"path/filepath"
	"sync"

	"github.com/hanzoai/beego"
	"github.com/hanzoai/beego/logs"
	"github.com/hanzoai/beego/session"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/controllers"
	"github.com/hanzoai/ai/model"
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

	// Analytics datastore (datastore): the OLAP ledger behind the usage/spend
	// Overview (hanzo.cloud_usage) and the per-tenant trace ledger
	// (hanzo.observations). Opt-in via DATASTORE_ADDR; connects in the background
	// and is a no-op when unset, so boot never blocks on the warehouse. This runs
	// in BOTH cmd/aid and the unified cloud binary (the ai ZAP node does not, so
	// the datastore is reached directly, not via a ZAP peer). See object.InitDatastore.
	object.InitDatastore()

	// OTel GenAI telemetry: one gen_ai span per LLM call → o11y OTLP
	// collector. Opt-in via OTEL_EXPORTER_OTLP_ENDPOINT; connects in the
	// background and is a no-op when unset, so boot never blocks on the
	// collector. Runs in BOTH cmd/aid and the unified cloud binary, mirroring
	// InitDatastore. See object.InitTelemetry.
	object.InitTelemetry()

	// Model routing/pricing config from YAML. Non-fatal: static fallback.
	configPath := conf.GetConfigString("modelConfigPath")
	if configPath == "" {
		configPath = "conf/models.yaml"
	}
	if err := controllers.InitModelConfig(configPath); err != nil {
		logs.Warn("Model config: %v (using static fallback)", err)
	}

	// Wire the YAML-backed context-window resolver: models.yaml context_window
	// becomes the per-model source of truth for max context, overriding the
	// static getContextLength table. Without this, glm-5.2 (1M context) fell
	// through the table to a 16K fallback and dead-ended long prompts + /compact.
	// Nil-safe: if ModelConfig failed to load, the table-only path stays.
	if mc := controllers.GetModelConfig(); mc != nil {
		model.SetContextWindowResolver(mc.ContextWindow)
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

	// The in-pod balance ledger (object.GlobalBalanceLedger) reserves/settles in
	// process memory — correct ONLY at a SINGLE replica (deploy strategy=Recreate
	// + HPA min=max=1). If the operator stamped CLOUD_API_REPLICAS > 1, refuse to
	// start rather than silently double-spend across pods; scaling out requires a
	// Commerce-atomic conditional reserve first.
	if n, ok := object.SinglePodReplicaHint(); ok && n > 1 {
		logs.Error("FATAL: CLOUD_API_REPLICAS=%d — the in-pod balance ledger double-spends across pods. Refusing to start; implement a Commerce-atomic reserve before scaling out.", n)
		panic("cloud-api: in-pod balance ledger requires a single replica (CLOUD_API_REPLICAS>1)")
	}
	logs.Info("balance ledger: single-pod invariant active (reservation ledger requires replicas=1, strategy=Recreate, HPA min=max=1)")

	// Commerce balance gate (pre-request balance enforcement).
	routers.InitBalanceGate()

	// Commerce-backed tier cache — must precede InitRateLimiter so
	// DefaultTierFunc can resolve tiers through it.
	routers.InitTierCache()

	// Per-key rate limiting (env override → tier cache → zen-free).
	bootRateLimiter = routers.InitRateLimiter(routers.DefaultTierFunc)
	logs.Info("Per-key rate limiter initialized (tiers: free=10/min, starter=60/min, pro=300/min, enterprise=1000/min)")

	// Copy the request body into c.Ctx.Input.RequestBody. The OpenAI-compatible
	// controllers read the raw body via c.Ctx.Input.RequestBody (e.g.
	// json.Unmarshal in ChatCompletions); beego only populates it when
	// CopyRequestBody is true. The standalone gets this from conf/app.conf
	// (copyrequestbody = true), but the embedded unified binary has no app.conf,
	// so set it here — otherwise every POST /v1/chat/completions (and the other
	// body-reading routes) fails with "Failed to parse request: unexpected end
	// of JSON input". MaxMemory bounds the copied body (64 MB, matching the
	// standalone default) so large uploads still stream rather than buffer.
	beego.BConfig.CopyRequestBody = true
	if beego.BConfig.MaxMemory <= 0 {
		beego.BConfig.MaxMemory = 1 << 26 // 64 MB
	}

	// beego filter chain — identical order to the standalone surface so
	// the embedded handler enforces the same auth/tenant/balance pipeline.
	beego.SetStaticPath("/swagger", "swagger")
	beego.InsertFilter("/v1/cloud/*", beego.BeforeRouter, routers.V1CloudRewriteFilter)
	beego.InsertFilter("/v1/iam/*", beego.BeforeRouter, routers.V1IamRewriteFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.CorsFilter)
	// Live request-geo tap: folds each inbound /v1 API hit into the in-process
	// traffic aggregate (edge country/region + service class only — never an IP).
	// A pure side effect that never blocks/writes, so it rides high in the chain to
	// count LB hits honestly. Powers the public /v1/traffic/globe marketing surface.
	beego.InsertFilter("/v1/*", beego.BeforeRouter, routers.TrafficTapFilter)
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

	// Session config (memory unless redisEndpoint set).
	//
	// Provider is "memory", NOT "file": the unified cloud binary ships in a
	// scratch/distroless image with a read-only root and no working ./tmp, so
	// beego's file provider fails inside SessionStart (os.Create on the sid
	// path) and beego renders that as HTTP 503 on every request — the exact
	// failure observed on the canary after the nil-GlobalSessions panic was
	// fixed. The session contents here are incidental (API requests authenticate
	// per-request via Bearer key/JWT, not cookies; sessions back only the
	// web-admin cookie flow), so an in-process memory store is correct and needs
	// no filesystem. Multi-pod deployments that need shared sessions set
	// redisEndpoint and get the redis provider. This keeps the embedded binary
	// and the standalone (cmd/aid) identical — one selection, no CWD dependency.
	beego.BConfig.WebConfig.Session.SessionOn = true
	beego.BConfig.WebConfig.Session.SessionName = "cloud_session_id"
	if conf.GetConfigString("redisEndpoint") == "" {
		beego.BConfig.WebConfig.Session.SessionProvider = "memory"
		beego.BConfig.WebConfig.Session.SessionProviderConfig = ""
	} else {
		beego.BConfig.WebConfig.Session.SessionProvider = "redis"
		beego.BConfig.WebConfig.Session.SessionProviderConfig = conf.GetConfigString("redisEndpoint")
	}
	beego.BConfig.WebConfig.Session.SessionGCMaxLifetime = 3600 * 24 * 365
	// SameSite=Lax: CSRF protection while preserving compatibility.
	beego.BConfig.WebConfig.Session.SessionCookieSameSite = http.SameSiteLaxMode

	// Build the session manager NOW. In the standalone (cmd/aid) beego.Run()
	// triggers beego's private registerSession() app-start hook, which
	// constructs beego.GlobalSessions from the settings above. The unified
	// cloud binary never calls beego.Run() (it owns its own zip listener), so
	// that hook never fires and beego.GlobalSessions stays nil — then every
	// request the /v1/ai/* adapter forwards panics with a nil-pointer deref
	// inside SessionStart (router.go → session.go), which beego renders as a
	// 500 "application error". Controllers across the surface read sessions
	// (account claims, openai_api GetSessionUsername, chat, store, …), so the
	// session manager is required, not optional. Construct it here — in the
	// SINGLE shared Bootstrap — so the embedded handler behaves identically to
	// the standalone. Idempotent: skip if already built (the standalone's
	// later registerSession() hook would otherwise leak a second GC goroutine).
	if err := initSessionManager(); err != nil {
		return fmt.Errorf("ai: session manager: %w", err)
	}

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

// initSessionManager constructs beego.GlobalSessions from the session settings
// configured in doBootstrap, mirroring beego's own private registerSession()
// app-start hook (beego v1.12 hooks.go) one-for-one so the embedded handler and
// the standalone share identical session behavior. It is idempotent: when the
// manager already exists (e.g. the standalone's later beego.Run() ran the hook
// first) it is a no-op, avoiding a duplicate GC goroutine.
//
// SessionConfig (the JSON override) is honored when present, exactly as beego
// does, so any conf/app.conf "sessionConfig" continues to work unchanged.
func initSessionManager() error {
	if !beego.BConfig.WebConfig.Session.SessionOn {
		return nil
	}
	if beego.GlobalSessions != nil {
		return nil
	}

	sc := beego.BConfig.WebConfig.Session
	mc := new(session.ManagerConfig)
	if raw := conf.GetConfigString("sessionConfig"); raw != "" {
		if err := json.Unmarshal([]byte(raw), mc); err != nil {
			return err
		}
	} else {
		mc.CookieName = sc.SessionName
		mc.EnableSetCookie = sc.SessionAutoSetCookie
		mc.Gclifetime = sc.SessionGCMaxLifetime
		mc.Secure = beego.BConfig.Listen.EnableHTTPS
		mc.CookieLifeTime = sc.SessionCookieLifeTime
		mc.ProviderConfig = filepath.ToSlash(sc.SessionProviderConfig)
		mc.DisableHTTPOnly = sc.SessionDisableHTTPOnly
		mc.Domain = sc.SessionDomain
		mc.EnableSidInHTTPHeader = sc.SessionEnableSidInHTTPHeader
		mc.SessionNameInHTTPHeader = sc.SessionNameInHTTPHeader
		mc.EnableSidInURLQuery = sc.SessionEnableSidInURLQuery
		mc.CookieSameSite = sc.SessionCookieSameSite
	}

	mgr, err := session.NewManager(sc.SessionProvider, mc)
	if err != nil {
		return err
	}
	beego.GlobalSessions = mgr
	go mgr.GC()
	return nil
}
