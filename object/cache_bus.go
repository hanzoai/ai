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

// cache_bus.go — cross-pod cache-invalidation broadcast.
//
// ai's per-org hot-path caches (org_settings, …) are in-process with a short TTL
// and a LOCAL invalidate on write, so the pod that served a console change — e.g.
// a Router → Policy savings↔quality dial move — applies it instantly, but sibling
// pods lag by one TTL window. When KV_URL points at a Hanzo KV (Valkey) instance,
// this bus broadcasts every invalidation over pub/sub so EVERY pod drops the stale
// entry within the round-trip — sub-second fleet-wide — with the TTL kept as the
// fail-safe backstop: a missed message, a restarting subscriber, or a down bus
// still converges within the TTL. It is opt-in and a NO-OP when KV_URL is unset,
// exactly like InitDatastore / InitTelemetry, so ai runs unchanged without Valkey.

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	kv "github.com/hanzokv/go/v9"

	"github.com/hanzoai/ai/log"
)

// cacheBusChannel is the single fan-out channel; the payload names the cache to
// flush ("org_settings", or "*" for all). New caches join by adding a case to
// applyInvalidation — one channel, one subscriber goroutine.
const cacheBusChannel = "ai:cache:invalidate"

var (
	cacheBusMu     sync.RWMutex
	cacheBusClient *kv.Client // nil = bus disabled (KV_URL unset / not connected)
)

// InitCacheBus enables the fleet-wide invalidation bus when KV_URL is set,
// connecting + subscribing in the background so boot never blocks on Valkey. When
// unset it is a no-op and every pod relies on the per-pod TTL (unchanged behavior).
// Mirrors InitDatastore/InitTelemetry; called once from Bootstrap.
func InitCacheBus() {
	url := strings.TrimSpace(os.Getenv("KV_URL"))
	if url == "" {
		log.Info("cache bus: disabled (set KV_URL to broadcast cache invalidations fleet-wide; per-pod TTL still applies)")
		return
	}
	go runCacheBus(url)
}

// runCacheBus latches the publish client, then subscribes and flushes on every
// broadcast. The kv-go PubSub channel reconnects internally, so the range ends
// only when the client is closed (process shutdown).
func runCacheBus(url string) {
	opts, err := kv.ParseURL(url)
	if err != nil {
		log.Error("cache bus: invalid KV_URL, staying on per-pod TTL: %v", err)
		return
	}
	client := kv.NewClient(opts)
	cacheBusMu.Lock()
	cacheBusClient = client
	cacheBusMu.Unlock()
	log.Info("cache bus: enabled — broadcasting cache invalidations over KV pub/sub (%s)", cacheBusChannel)

	sub := client.Subscribe(context.Background(), cacheBusChannel)
	for msg := range sub.Channel() {
		applyInvalidation(msg.Payload)
	}
}

// applyInvalidation flushes the LOCAL cache named by a broadcast payload. It never
// re-publishes (the sender already broadcast), so there is no fan-out loop — the
// pod that originated the write simply re-flushes its already-empty cache. Unknown
// kinds are ignored so an older pod tolerates a newer sender.
func applyInvalidation(kind string) {
	switch kind {
	case "org_settings", "*":
		invalidateOrgSettingsCacheLocal()
	}
}

// broadcastInvalidate publishes a local invalidation to the fleet (best-effort). A
// disabled or unreachable bus is a NO-OP: the originating pod already flushed
// locally and the TTL backstops every other pod, so a config write never fails on
// the bus.
func broadcastInvalidate(kind string) {
	cacheBusMu.RLock()
	client := cacheBusClient
	cacheBusMu.RUnlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Publish(ctx, cacheBusChannel, kind).Err(); err != nil {
		log.Warn("cache bus: publish %s failed (fleet still converges via TTL): %v", kind, err)
	}
}
