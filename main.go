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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/beego/beego"
	"github.com/beego/beego/logs"
	_ "github.com/beego/beego/session/redis"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/controllers"
	"github.com/hanzoai/cloud/object"
	"github.com/hanzoai/cloud/proxy"
	"github.com/hanzoai/cloud/routers"
	"github.com/hanzoai/cloud/util"
)

func main() {
	object.InitFlag()
	object.InitAdapter()
	object.CreateTables()

	object.InitDb()

	// Load model routing/pricing config from YAML. Non-fatal: falls back to static maps.
	configPath := conf.GetConfigString("modelConfigPath")
	if configPath == "" {
		configPath = "conf/models.yaml"
	}
	if err := controllers.InitModelConfig(configPath); err != nil {
		logs.Warn("Model config: %v (using static fallback)", err)
	}

	proxy.InitHttpClient()
	util.InitMaxmindFiles()
	util.InitIpDb()
	util.InitParser()
	object.InitCleanupChats()
	object.InitStoreCount()
	object.InitCommitRecordsTask()
	object.InitScanJobProcessor()
	object.InitMessageTransactionRetry()

	// Initialize per-key rate limiting. Tier resolution is env-driven via
	// RATE_LIMIT_TIERS (e.g. "hk-0d2eb=enterprise,hk-feb5b=pro").
	rlInstance := routers.InitRateLimiter(routers.DefaultTierFunc)
	logs.Info("Per-key rate limiter initialized (tiers: free=10/min, starter=60/min, pro=300/min, enterprise=1000/min)")

	beego.SetStaticPath("/swagger", "swagger")
	beego.InsertFilter("*", beego.BeforeRouter, routers.CorsFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.HstsFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.CacheControlFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.RateLimitFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.AutoSigninFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.StaticFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.TenantContextFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.AuthzFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.PrometheusFilter)
	beego.InsertFilter("*", beego.BeforeRouter, routers.RecordMessage)
	beego.InsertFilter("*", beego.AfterExec, routers.AfterRecordMessage, false)
	beego.InsertFilter("*", beego.AfterExec, routers.SecureCookieFilter, false)

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

	// Set session cookie security attributes
	// SameSite=Lax provides CSRF protection while maintaining compatibility
	beego.BConfig.WebConfig.Session.SessionCookieSameSite = http.SameSiteLaxMode

	var logAdapter string
	logConfigMap := make(map[string]interface{})
	err := json.Unmarshal([]byte(conf.GetConfigString("logConfig")), &logConfigMap)
	if err != nil {
		panic(err)
	}
	_, ok := logConfigMap["adapter"]
	if !ok {
		logAdapter = "file"
	} else {
		logAdapter = logConfigMap["adapter"].(string)
	}
	if logAdapter == "console" {
		logs.Reset()
	}
	err = logs.SetLogger(logAdapter, conf.GetConfigString("logConfig"))
	if err != nil {
		panic(err)
	}

	port := beego.AppConfig.DefaultInt("httpport", 14000)

	err = util.StopOldInstance(port)
	if err != nil {
		panic(err)
	}

	// Initialize the billing usage queue. Records are retried with exponential
	// backoff instead of being silently dropped on transient Commerce failures.
	bq := controllers.InitBillingQueue()
	if bq != nil {
		logs.Info("Billing queue started (Commerce endpoint configured)")
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

		os.Exit(0)
	}()

	go object.ClearThroughputPerSecond()

	beego.Run(fmt.Sprintf(":%v", port))
}
