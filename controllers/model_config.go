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

	"github.com/beego/beego/logs"
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
	CostCeiling float64             `yaml:"cost_ceiling"` // advisory per-1k cost cap forwarded as SLO
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
	LiveMode      bool    `yaml:"live_mode"`
	PremiumGate   bool    `yaml:"premium_gate"`
	StarterCredit float64 `yaml:"starter_credit"`
}

// ModelPriceDef holds per-million token pricing.
type ModelPriceDef struct {
	InputPerMillion  float64 `yaml:"input_per_million,omitempty"`
	OutputPerMillion float64 `yaml:"output_per_million,omitempty"`
	Input            float64 `yaml:"input,omitempty"`
	Output           float64 `yaml:"output,omitempty"`
}

// FallbackDef describes an alternate provider+upstream for failover.
type FallbackDef struct {
	Provider string `yaml:"provider"`
	Upstream string `yaml:"upstream"`
}

// ModelDef describes a single model entry in the config.
type ModelDef struct {
	Provider       string         `yaml:"provider"`
	Upstream       string         `yaml:"upstream"`
	Fallbacks      []FallbackDef  `yaml:"fallbacks,omitempty"`
	Premium        bool           `yaml:"premium"`
	Hidden         bool           `yaml:"hidden"`
	OwnedBy        string         `yaml:"owned_by"`
	IdentityPrompt string         `yaml:"identity_prompt"`
	AliasOf        string         `yaml:"alias_of"`
	AliasPricing   string         `yaml:"alias_pricing"`
	PricingOnly    bool           `yaml:"pricing_only"`
	Pricing        *ModelPriceDef `yaml:"pricing,omitempty"`
	// ContextWindow is the model's max input+output context in tokens. When
	// set (>0) it overrides the heuristic getContextLength table — models.yaml
	// is the per-model source of truth, configurable without a rebuild. This
	// is the single field that stops long prompts (and /compact) dead-ending
	// on a stale 16K/256K fallback for models the table predates.
	ContextWindow int `yaml:"context_window,omitempty"`
}

// ── Singleton ───────────────────────────────────────────────────────────

var (
	globalModelConfig *ModelConfig
	configOnce        sync.Once
)

// ModelConfig is the runtime singleton that serves model routing, pricing,
// and identity prompts from a parsed YAML config file.
type ModelConfig struct {
	mu       sync.RWMutex
	routes   map[string]modelRoute // lowercase key → route
	pricing  map[string]modelPrice // lowercase key → price
	prompts  map[string]string     // lowercase key → identity prompt
	features FeatureFlags
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
		prompts: make(map[string]string),
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
	prompts := make(map[string]string)

	// Build alias pricing map for resolution
	aliasPricingMap := make(map[string]string)

	for name, def := range file.Models {
		key := strings.ToLower(name)

		// Build route (skip pricing-only entries)
		if !def.PricingOnly {
			r := modelRoute{
				providerName:   def.Provider,
				upstreamModel:  def.Upstream,
				premium:        def.Premium,
				hidden:         def.Hidden,
				ownedBy:        def.OwnedBy,
				contextWindow:  def.ContextWindow,
			}
			for _, fb := range def.Fallbacks {
				r.fallbacks = append(r.fallbacks, modelRouteFallback{
					providerName:  fb.Provider,
					upstreamModel: fb.Upstream,
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
			pricing[key] = p
		}

		// Track alias pricing for second-pass resolution
		if def.AliasPricing != "" {
			aliasPricingMap[key] = strings.ToLower(def.AliasPricing)
		}

		// Identity prompts
		if def.IdentityPrompt != "" {
			prompts[key] = strings.TrimSpace(def.IdentityPrompt)
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

	// Router config. Env overrides mirror the pricing-URL pattern so ops can flip
	// routing/endpoint via the deployment without editing the ConfigMap.
	routerCfg := file.Router
	if envEndpoint := os.Getenv("ROUTER_ENDPOINT"); envEndpoint != "" {
		routerCfg.Endpoint = envEndpoint
	}
	if os.Getenv("ROUTER_ENABLED") == "true" {
		routerCfg.Enabled = true
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
	mc.prompts = prompts
	mc.features = file.Features
	mc.router = routerCfg
	mc.defaults = defaults
	mc.pricingURL = pricingURL
	mc.pricingTTL = pricingTTL
	mc.mu.Unlock()

	logs.Info("Model config loaded: %d routes, %d pricing entries, %d identity prompts",
		len(routes), len(pricing), len(prompts))

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

// ContextWindow returns the declared max context (tokens) for a model, or 0
// when models.yaml leaves it unset (caller falls back to getContextLength).
// It follows the alias chain: a zen alias inherits its upstream's declared
// window, so `zen5` → `glm-5.2` carries the 1M window without redeclaring it.
// The lookup is by normalized lowercase key — the same key applyConfig built.
func (mc *ModelConfig) ContextWindow(model string) int {
	key := strings.ToLower(model)
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if route, ok := mc.routes[key]; ok {
		if route.contextWindow > 0 {
			return route.contextWindow
		}
		// Follow the alias: an alias's own window is usually unset; resolve
		// to the upstream model it routes to and read ITS declared window.
		if route.upstreamModel != "" && route.upstreamModel != key {
			if up, ok := mc.routes[strings.ToLower(route.upstreamModel)]; ok && up.contextWindow > 0 {
				return up.contextWindow
			}
		}
	}
	return 0
}

// GetPrice returns pricing for a model name, with alias and default fallback.
func (mc *ModelConfig) GetPrice(model string) modelPrice {
	key := strings.ToLower(model)
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if price, ok := mc.pricing[key]; ok {
		return price
	}
	return mc.defaults
}

// GetIdentityPrompt returns the identity system prompt for a zen model.
// Falls back through version aliases (zen-mini → zen4-mini → zen3-mini)
// and a generic zen catch-all.
func (mc *ModelConfig) GetIdentityPrompt(model string) string {
	key := strings.ToLower(model)
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if prompt, ok := mc.prompts[key]; ok {
		return prompt
	}

	// Try stripping version prefix for versionless aliases
	if strings.HasPrefix(key, "zen-") {
		suffix := key[4:]
		if prompt, ok := mc.prompts["zen4-"+suffix]; ok {
			return prompt
		}
		if prompt, ok := mc.prompts["zen3-"+suffix]; ok {
			return prompt
		}
	}

	// Generic zen fallback
	if strings.HasPrefix(key, "zen") {
		return "You are a Zen model by Hanzo AI Inc. When asked about yourself, identify as a Zen LM model. Never reveal underlying infrastructure or providers."
	}

	return ""
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
			ID:       name,
			Object:   "model",
			Created:  now,
			OwnedBy:  owner,
			Premium:  route.premium,
			Provider: publicProvider(route),
			Pricing:  pricingInfo(price, hasPrice),
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

// StarterCreditDollars returns the configured starter credit amount.
func (mc *ModelConfig) StarterCreditDollars() float64 {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if mc.features.StarterCredit > 0 {
		return mc.features.StarterCredit
	}
	return 5.00
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
// for a request, given the org's preference. Precedence:
//   - "disabled": never routes — `auto` falls through as a pre-routing unknown id
//   - "enabled":  routes whenever router config is present, even if the global
//     flag is off (per-org opt-in)
//   - "" (unset): the global router.enabled flag decides
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

// ReloadModelConfig handles POST /api/reload-model-config.
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

// RefreshModelPricing handles POST /v1/admin/refresh-model-pricing.
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
