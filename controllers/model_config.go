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
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/ai/router"
	"gopkg.in/yaml.v3"
)

// zapModelEntry is used by ListModelsWithUpstream for ZAP responses.
type zapModelEntry struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	OwnedBy  string `json:"owned_by"`
	Premium  bool   `json:"premium,omitempty"`
	Upstream string `json:"upstream,omitempty"`
}

// ── YAML config types ───────────────────────────────────────────────────

// ModelConfigFile is the top-level structure of conf/models.yaml.
type ModelConfigFile struct {
	Version        int                 `yaml:"version"`
	Services       ServiceEndpoints    `yaml:"services"`
	Cache          CacheTTLs           `yaml:"cache"`
	Features       FeatureFlags        `yaml:"features"`
	Retry          RetryDef            `yaml:"retry"`
	Router         RouterConfigDef     `yaml:"router"`
	DefaultPricing ModelPriceDef       `yaml:"default_pricing"`
	Models         map[string]ModelDef `yaml:"models"`
}

// RouterConfigDef configures the virtual `auto`/`zen-router` model. When
// disabled (the default) `auto` is not a routable model and behaves like any
// other unknown id. When enabled, a chat request for `auto` is classified and
// mapped to a concrete servable model before provider/pricing/billing
// resolution — so the request bills and reports the model that served it.
type RouterConfigDef struct {
	Enabled     bool                `yaml:"enabled"`
	Endpoint    string              `yaml:"endpoint"`     // zen-router base URL; "" = heuristic only
	Prefer      map[string][]string `yaml:"prefer"`       // task tag → ordered model ids ("default" catch-all)
	CostCeiling float64             `yaml:"cost_ceiling"` // advisory cost cap, USD per 1k tokens (per-1k), forwarded verbatim as the engine SLO
}

// ServiceEndpoints holds URLs for external pricing/model services.
type ServiceEndpoints struct {
	PricingURL string `yaml:"pricing_url"`
}

// CacheTTLs defines TTL durations for cached data.
type CacheTTLs struct {
	PricingTTL string `yaml:"pricing_ttl"`
}

// FeatureFlags controls runtime behavior.
type FeatureFlags struct {
	LiveMode    bool `yaml:"live_mode"`
	PremiumGate bool `yaml:"premium_gate"`
}

// RetryDef is how long we hold a request open for a provider that is merely
// busy, rather than handing its 429 to the client.
//
// It is configuration, not a constant, for the same reason the context windows
// are: the right number is a property of the providers we happen to be using
// today, and it changes without our code changing. Baking it in means a rebuild
// to tune a timeout — which is how the context table ended up lying for a year.
//
// attempts: total tries of ONE provider (1 disables same-provider retry).
// base/max: exponential backoff bounds, jittered, capped by the caller's deadline.
type RetryDef struct {
	Attempts    int    `yaml:"attempts,omitempty"`
	BaseBackoff string `yaml:"base_backoff,omitempty"` // e.g. "500ms"
	MaxBackoff  string `yaml:"max_backoff,omitempty"`  // e.g. "4s"
}

// ModelPriceDef holds per-million token economics: the customer price (Input/Output,
// with the *_per_million long form) plus the optional provider COGS
// (cost_in_per_million / cost_out_per_million). COGS unset ⇒ cost defaults to price
// (zero margin), so an entry that lists only a price is byte-identical to before.
type ModelPriceDef struct {
	InputPerMillion   float64 `yaml:"input_per_million,omitempty"`
	OutputPerMillion  float64 `yaml:"output_per_million,omitempty"`
	Input             float64 `yaml:"input,omitempty"`
	Output            float64 `yaml:"output,omitempty"`
	CostInPerMillion  float64 `yaml:"cost_in_per_million,omitempty"`
	CostOutPerMillion float64 `yaml:"cost_out_per_million,omitempty"`
}

// FallbackDef describes an alternate provider+upstream for failover.
type FallbackDef struct {
	Provider string `yaml:"provider"`
	Upstream string `yaml:"upstream"`
	// ContextWindow is the window THIS provider serves the model at. The same
	// model is served at different sizes by different providers — DO serves
	// glm-5.2 at 262144 while the model itself is a 1M-context model — so the
	// window belongs to the (provider, upstream) pair, not to the model. A
	// fallback that reaches a bigger window declares it here; 0 inherits the
	// primary route's window.
	ContextWindow int `yaml:"context_window,omitempty"`
}

// ModelDef describes a single model entry in the config.
type ModelDef struct {
	Provider     string         `yaml:"provider"`
	Upstream     string         `yaml:"upstream"`
	Fallbacks    []FallbackDef  `yaml:"fallbacks,omitempty"`
	Premium      bool           `yaml:"premium"`
	Hidden       bool           `yaml:"hidden"`
	OwnedBy      string         `yaml:"owned_by"`
	AliasOf      string         `yaml:"alias_of"`
	AliasPricing string         `yaml:"alias_pricing"`
	PricingOnly  bool           `yaml:"pricing_only"`
	Pricing      *ModelPriceDef `yaml:"pricing,omitempty"`
	// ContextWindow is the model's max input+output context in tokens. When
	// set (>0) it overrides the heuristic getContextLength table — models.yaml
	// is the per-model source of truth, configurable without a rebuild. This
	// is the single field that stops long prompts (and /compact) dead-ending
	// on a stale 16K/256K fallback for models the table predates.
	ContextWindow int `yaml:"context_window,omitempty"`
	// MaxOutputTokens is the model's max completion length (tokens), taken from
	// the upstream catalog. Surfaced in /v1/models so a client caps its output
	// request honestly instead of guessing.
	MaxOutputTokens int `yaml:"max_output_tokens,omitempty"`
	// Vision reports the model accepts image input (OpenAI image_url content
	// parts); Tools reports it supports function/tool calling. Both are additive
	// capability flags surfaced in /v1/models (omitempty ⇒ present only when
	// true). Set ONLY from a live capability probe of the serving provider —
	// never guessed — so absence means "not advertised", never a fabricated yes.
	Vision bool `yaml:"vision,omitempty"`
	Tools  bool `yaml:"tools,omitempty"`
}

// ── Singleton ───────────────────────────────────────────────────────────

var (
	globalModelConfig *ModelConfig
	configOnce        sync.Once
)

// ModelConfig is the runtime singleton that serves model routing and pricing
// from a parsed YAML config file.
type ModelConfig struct {
	mu       sync.RWMutex
	routes   map[string]modelRoute // lowercase key → route
	pricing  map[string]modelPrice // lowercase key → price
	features FeatureFlags
	retry    RetryDef
	router   RouterConfigDef
	defaults modelPrice

	// Live refresh state
	configPath    string
	pricingURL    string
	pricingTTL    time.Duration
	lastPricingAt time.Time
	stopCh        chan struct{}
}

// InitModelConfig loads the YAML config and optionally starts a background
// refresh goroutine (when live_mode is true). Returns an error if the file
// cannot be read or parsed. This is non-fatal — the caller can log and fall
// back to static maps.
func InitModelConfig(path string) error {
	mc := &ModelConfig{
		routes:  make(map[string]modelRoute),
		pricing: make(map[string]modelPrice),
		stopCh:  make(chan struct{}),
	}

	if err := mc.loadFromFile(path); err != nil {
		return err
	}

	mc.configPath = path
	globalModelConfig = mc

	if mc.features.LiveMode {
		go mc.backgroundRefresh()
	}

	return nil
}

// GetModelConfig returns the singleton. Returns nil if not initialized.
func GetModelConfig() *ModelConfig {
	return globalModelConfig
}

// ── Loading ─────────────────────────────────────────────────────────────

func (mc *ModelConfig) loadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("model config: read %s: %w", path, err)
	}

	var file ModelConfigFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("model config: parse %s: %w", path, err)
	}

	return mc.applyConfig(&file)
}

func (mc *ModelConfig) applyConfig(file *ModelConfigFile) error {
	routes := make(map[string]modelRoute, len(file.Models))
	pricing := make(map[string]modelPrice, len(file.Models))

	// Build alias pricing map for resolution
	aliasPricingMap := make(map[string]string)

	for name, def := range file.Models {
		key := strings.ToLower(name)

		// Build route (skip pricing-only entries)
		if !def.PricingOnly {
			r := modelRoute{
				providerName:  def.Provider,
				upstreamModel: def.Upstream,
				premium:       def.Premium,
				hidden:        def.Hidden,
				ownedBy:       def.OwnedBy,
				contextWindow: def.ContextWindow,
				maxOutput:     def.MaxOutputTokens,
				vision:        def.Vision,
				tools:         def.Tools,
			}
			for _, fb := range def.Fallbacks {
				r.fallbacks = append(r.fallbacks, modelRouteFallback{
					providerName:  fb.Provider,
					upstreamModel: fb.Upstream,
					contextWindow: fb.ContextWindow,
				})
			}
			routes[key] = r
		}

		// Build pricing
		if def.Pricing != nil {
			p := modelPrice{}
			// Support both {input, output} and {input_per_million, output_per_million}
			if def.Pricing.Input > 0 {
				p.InputPerMillion = def.Pricing.Input
			} else {
				p.InputPerMillion = def.Pricing.InputPerMillion
			}
			if def.Pricing.Output > 0 {
				p.OutputPerMillion = def.Pricing.Output
			} else {
				p.OutputPerMillion = def.Pricing.OutputPerMillion
			}
			// Provider COGS (optional). Unset ⇒ 0 ⇒ cost defaults to price at read
			// time (modelPrice.costInputPerMillion), so margin is 0 unless declared.
			p.CostInPerMillion = def.Pricing.CostInPerMillion
			p.CostOutPerMillion = def.Pricing.CostOutPerMillion
			pricing[key] = p
		}

		// Track alias pricing for second-pass resolution
		if def.AliasPricing != "" {
			aliasPricingMap[key] = strings.ToLower(def.AliasPricing)
		}
	}

	// Resolve alias pricing (second pass)
	for alias, base := range aliasPricingMap {
		if _, exists := pricing[alias]; !exists {
			if basePrice, ok := pricing[base]; ok {
				pricing[alias] = basePrice
			}
		}
	}

	// Parse service config
	pricingURL := file.Services.PricingURL
	if envURL := os.Getenv("PRICING_SERVICE_URL"); envURL != "" {
		pricingURL = envURL
	}

	// Router config. ROUTER_ENDPOINT stays an env override — it is infra wiring
	// (where the engine's /v1/route lives), mirroring the pricing-URL pattern so
	// ops can point at the engine without editing the ConfigMap.
	//
	// ROUTER_ENABLED is NO LONGER folded in here. The platform-wide routing
	// default is now the "*" GlobalDefaultOwner OrgSettings row (see
	// effectiveAutoRouting), edited from admin.hanzo.ai — one source of truth. The
	// env survives only as a DEPRECATED fallback consulted by effectiveAutoRouting
	// when that row is unset, so router.Enabled here reflects the conf file alone:
	// the last-resort default beneath both the row and the env.
	routerCfg := file.Router
	if envEndpoint := os.Getenv("ROUTER_ENDPOINT"); envEndpoint != "" {
		routerCfg.Endpoint = envEndpoint
	}

	pricingTTL := 6 * time.Hour
	if file.Cache.PricingTTL != "" {
		if d, err := time.ParseDuration(file.Cache.PricingTTL); err == nil {
			pricingTTL = d
		}
	}

	// Default pricing
	defaults := modelPrice{InputPerMillion: 1.00, OutputPerMillion: 4.00}
	if file.DefaultPricing.InputPerMillion > 0 {
		defaults.InputPerMillion = file.DefaultPricing.InputPerMillion
	}
	if file.DefaultPricing.OutputPerMillion > 0 {
		defaults.OutputPerMillion = file.DefaultPricing.OutputPerMillion
	}

	// Apply under write lock
	mc.mu.Lock()
	mc.routes = routes
	mc.pricing = pricing
	mc.features = file.Features
	mc.retry = file.Retry
	mc.router = routerCfg
	mc.defaults = defaults
	mc.pricingURL = pricingURL
	mc.pricingTTL = pricingTTL
	mc.mu.Unlock()

	log.Info("Model config loaded: %d routes, %d pricing entries",
		len(routes), len(pricing))

	return nil
}

// Reload re-reads the config file and triggers a live pricing fetch if enabled.
func (mc *ModelConfig) Reload() error {
	if err := mc.loadFromFile(mc.configPath); err != nil {
		return err
	}

	mc.mu.RLock()
	live := mc.features.LiveMode
	mc.mu.RUnlock()

	if live {
		go mc.fetchLivePricing()
	}

	return nil
}

// ── Lookups ─────────────────────────────────────────────────────────────

// ResolveRoute looks up a user-facing model name and returns its route.
// Returns nil if the model is not in the routing table.
func (mc *ModelConfig) ResolveRoute(model string) *modelRoute {
	key := strings.ToLower(model)
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if route, ok := mc.routes[key]; ok {
		return &route
	}
	return nil
}

// RetryConfig returns the configured retry policy (zero fields mean "use the
// default"). Read under the lock so a live config reload is safe.
func (mc *ModelConfig) RetryConfig() RetryDef {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.retry
}

// ContextWindow returns the max context (tokens) a model is served at, or 0
// when nothing declares it (the caller applies the floor).
//
// A window is a property of WHERE a model is served, not of the model alone:
// GLM-5.2 is a 1M-context model, but DigitalOcean serves it at 262144. So the
// value resolves in three steps, most specific first:
//
//	provider override  (this route's own context_window)
//	model default      (the upstream leaf's context_window, via the alias chain)
//	floor              (0 here; model.DefaultContextLength at the call site)
//
// The alias chain means `zen5` inherits its upstream's window without
// redeclaring it. Lookup is by normalized lowercase key.
func (mc *ModelConfig) ContextWindow(model string) int {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.contextWindowLocked(strings.ToLower(model))
}

func (mc *ModelConfig) contextWindowLocked(key string) int {
	route, ok := mc.routes[key]
	if !ok {
		return 0
	}
	if route.contextWindow > 0 {
		return route.contextWindow
	}
	// Follow the alias to the upstream leaf and read ITS declared window.
	if route.upstreamModel != "" && route.upstreamModel != key {
		if up, ok := mc.routes[strings.ToLower(route.upstreamModel)]; ok && up.contextWindow > 0 {
			return up.contextWindow
		}
	}
	return 0
}

// RouteForContext picks a route for `model` that can actually serve a prompt of
// `tokens`, returning the provider and upstream to call.
//
// The primary route is used whenever it fits. When it does not — the prompt is
// 400K and DO serves glm-5.2 at 262144 — the request is not refused: the
// fallback chain is searched for a provider that serves the SAME model with a
// window big enough, and that one is called instead. Fallbacks are declared in
// preference order, so the first that fits wins.
//
// This is why the window lives on the (provider, upstream) pair: it turns the
// fallback chain from failover-only into capability selection. If nothing in
// the chain fits, the primary is returned and the upstream rejects it — an
// honest error from the model rather than a guess from us.
//
// tokens <= 0 means "unknown size": take the primary.
func (mc *ModelConfig) RouteForContext(model string, tokens int) (provider, upstream string, window int, ok bool) {
	key := strings.ToLower(model)

	mc.mu.RLock()
	defer mc.mu.RUnlock()

	route, found := mc.routes[key]
	if !found {
		return "", "", 0, false
	}

	primary := mc.contextWindowLocked(key)
	if tokens <= 0 || primary == 0 || tokens <= primary {
		return route.providerName, route.upstreamModel, primary, true
	}

	// The primary cannot hold this prompt. Find a provider that can.
	for _, fb := range route.fallbacks {
		w := fb.contextWindow
		if w == 0 {
			w = mc.contextWindowLocked(strings.ToLower(fb.upstreamModel))
		}
		if w >= tokens {
			return fb.providerName, fb.upstreamModel, w, true
		}
	}

	// Nothing fits. Return the primary and let the upstream speak for itself.
	return route.providerName, route.upstreamModel, primary, true
}

// GetPrice returns pricing for a model name, with alias and default fallback.
func (mc *ModelConfig) GetPrice(model string) modelPrice {
	p, _ := mc.GetPriceOK(model)
	return p
}

// GetPriceOK is GetPrice plus whether a REAL per-model price was found (true) or the
// configured default was synthesized (false). The price value is identical to GetPrice;
// only unpriced-model detection reads the bool.
func (mc *ModelConfig) GetPriceOK(model string) (modelPrice, bool) {
	key := strings.ToLower(model)
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if price, ok := mc.pricing[key]; ok {
		return price, true
	}
	return mc.defaults, false
}

// ListModels returns visible models sorted by name (excludes hidden).
func (mc *ModelConfig) ListModels() []modelInfo {
	now := time.Now().Unix()
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	models := make([]modelInfo, 0, len(mc.routes))
	for name, route := range mc.routes {
		if route.hidden {
			continue
		}
		owner := route.ownedBy
		if owner == "" {
			owner = route.providerName
		}
		// mc.pricing is guarded by the same mc.mu held here; a present entry
		// means real per-model pricing was configured (never a default).
		price, hasPrice := mc.pricing[name]
		models = append(models, modelInfo{
			ID:              name,
			Object:          "model",
			Created:         now,
			OwnedBy:         owner,
			Premium:         route.premium,
			Provider:        publicProvider(route),
			ContextWindow:   route.contextWindow,
			MaxOutputTokens: route.maxOutput,
			SupportsVision:  route.vision,
			SupportsTools:   route.tools,
			Pricing:         pricingInfo(price, hasPrice),
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	return models
}

// ListModelsWithUpstream returns all models including upstream IDs (for ZAP).
func (mc *ModelConfig) ListModelsWithUpstream() []zapModelEntry {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	models := make([]zapModelEntry, 0, len(mc.routes))
	for name, route := range mc.routes {
		models = append(models, zapModelEntry{
			ID:       name,
			Object:   "model",
			OwnedBy:  route.providerName,
			Premium:  route.premium,
			Upstream: route.upstreamModel,
		})
	}

	return models
}

// PremiumGateEnabled returns whether the premium gate feature is active.
func (mc *ModelConfig) PremiumGateEnabled() bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.features.PremiumGate
}

// RouterEnabled reports whether the virtual `auto`/`zen-router` model is active.
func (mc *ModelConfig) RouterEnabled() bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.router.Enabled
}

// routerConfigPresent reports whether the config carries anything to route with
// — the global enable flag, a preference table, or an engine endpoint. A per-org
// "enabled" override only takes effect when this is true (nothing to route with
// otherwise). Caller must hold mc.mu.
func (mc *ModelConfig) routerConfigPresent() bool {
	return mc.router.Enabled || len(mc.router.Prefer) > 0 || mc.router.Endpoint != ""
}

// AutoRoutingActive decides whether the virtual `auto`/`zen-router` model routes
// for a request, given the EFFECTIVE preference already resolved by
// effectiveAutoRouting (org row > "*" GlobalDefaultOwner row > deprecated
// ROUTER_ENABLED env). Precedence on that resolved three-state preference:
//   - "disabled": never routes — `auto` falls through as a pre-routing unknown id
//   - "enabled":  routes whenever router config is present (per-org or global opt-in)
//   - "" (unset): every row and the env are unset — the conf router.enabled flag
//     is the last-resort default
func (mc *ModelConfig) AutoRoutingActive(orgPref string) bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	switch orgPref {
	case object.AutoRoutingDisabled:
		return false
	case object.AutoRoutingEnabled:
		return mc.routerConfigPresent()
	default:
		return mc.router.Enabled
	}
}

// ConfRouterPolicy returns the live conf router Prefer + CostCeiling under the
// read lock — the baseline that effectiveRouterPrefer /
// effectiveRouterCostCeiling fold the per-org OrgSettings over. The Prefer map
// is copied so callers never hold (or mutate) the live config map.
func (mc *ModelConfig) ConfRouterPolicy() (map[string][]string, float64) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	out := make(map[string][]string, len(mc.router.Prefer))
	for k, v := range mc.router.Prefer {
		out[k] = v
	}
	return out, mc.router.CostCeiling
}

// RouterClient assembles a router.Client from the config, regardless of the
// enabled flag — the enable decision is made separately by AutoRoutingActive.
// `known` restricts the choice to models the caller can serve for the current
// org (typically resolveModelRouteForOrg != nil). Cheap to build per request.
func (mc *ModelConfig) RouterClient(known func(string) bool) router.Client {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return router.Client{
		Endpoint: mc.router.Endpoint,
		Timeout:  router.DefaultTimeout,
		Policy: router.Policy{
			Prefer:      mc.router.Prefer,
			CostCeiling: mc.router.CostCeiling,
		},
		Known: known,
	}
}

// ── Admin endpoint ──────────────────────────────────────────────────────

// ReloadModelConfig reloads the model configuration from YAML and refreshes live
// pricing, so a catalogue change takes effect without a restart.
// @Title ReloadModelConfig
// @Tag Admin
// @Description Reload model configuration from YAML and refresh live pricing.
// @Success 200 {object} controllers.Response
// @router /admin/reload-model-config [post]
func (c *ApiController) ReloadModelConfig() {
	if !c.RequireSuperAdmin() {
		return
	}
	cfg := GetModelConfig()
	if cfg == nil {
		c.ResponseError("model config not initialized")
		return
	}

	if err := cfg.Reload(); err != nil {
		c.ResponseError(fmt.Sprintf("reload failed: %s", err.Error()))
		return
	}

	c.ResponseOk()
}

// RefreshModelPricing forces a live pricing refresh from the configured pricing
// service, so the catalogue's rates match the source without waiting for the cycle.
// @Title RefreshModelPricing
// @Tag Admin
// @Description Force a live pricing refresh from the configured pricing service.
// @Success 200 {object} controllers.Response
// @router /admin/refresh-model-pricing [post]
func (c *ApiController) RefreshModelPricing() {
	if !c.RequireSuperAdmin() {
		return
	}
	cfg := GetModelConfig()
	if cfg == nil {
		c.ResponseError("model config not initialized")
		return
	}

	cfg.fetchLivePricing()

	c.ResponseOk(struct {
		LastPricingRefresh time.Time `json:"lastPricingRefresh"`
	}{
		LastPricingRefresh: cfg.LastPricingRefresh().UTC(),
	})
}
