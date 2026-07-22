package object

import (
	"testing"
	"time"
)

// TestApplyInvalidation_FlushesOrgSettings proves a received broadcast drops this
// pod's cached settings (the subscriber's flush path), and that an unknown kind is
// a safe no-op (an older pod tolerates a newer sender).
func TestApplyInvalidation_FlushesOrgSettings(t *testing.T) {
	orgSettingsCacheMu.Lock()
	orgSettingsCache["acme"] = &orgSettingsCacheEntry{settings: &OrgSettings{Owner: "acme"}, fetchedAt: time.Now()}
	orgSettingsCacheMu.Unlock()

	// Unknown kind must not touch the cache.
	applyInvalidation("something-else")
	orgSettingsCacheMu.RLock()
	n := len(orgSettingsCache)
	orgSettingsCacheMu.RUnlock()
	if n == 0 {
		t.Fatal("unknown invalidation kind flushed the cache — should be a no-op")
	}

	// The org_settings broadcast flushes it.
	applyInvalidation("org_settings")
	orgSettingsCacheMu.RLock()
	n = len(orgSettingsCache)
	orgSettingsCacheMu.RUnlock()
	if n != 0 {
		t.Fatalf("org_settings broadcast left %d cached entries, want 0 (flushed)", n)
	}
}

// TestBroadcastInvalidate_NoBusIsNoop proves a write never fails when the KV_URL
// bus is disabled: broadcastInvalidate with no client is a silent no-op — the
// local flush already happened and the TTL backstops every other pod.
func TestBroadcastInvalidate_NoBusIsNoop(t *testing.T) {
	cacheBusMu.Lock()
	cacheBusClient = nil
	cacheBusMu.Unlock()
	broadcastInvalidate("org_settings") // must not panic or block
}

// TestOrgSettingsTTL_EnvOverride proves the cache TTL honors
// ROUTER_SETTINGS_CACHE_TTL_SECONDS and falls back to the 5s near-real-time
// default on unset/garbage.
func TestOrgSettingsTTL_EnvOverride(t *testing.T) {
	t.Setenv("ROUTER_SETTINGS_CACHE_TTL_SECONDS", "12")
	if got := orgSettingsTTL(); got != 12*time.Second {
		t.Fatalf("TTL with env=12 = %v, want 12s", got)
	}
	t.Setenv("ROUTER_SETTINGS_CACHE_TTL_SECONDS", "")
	if got := orgSettingsTTL(); got != 5*time.Second {
		t.Fatalf("default TTL = %v, want 5s", got)
	}
	t.Setenv("ROUTER_SETTINGS_CACHE_TTL_SECONDS", "garbage")
	if got := orgSettingsTTL(); got != 5*time.Second {
		t.Fatalf("bad-env TTL = %v, want 5s default", got)
	}
}
