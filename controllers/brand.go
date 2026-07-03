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
	"strings"
	"sync"

	"github.com/hanzoai/ai/conf"
	iam "github.com/hanzoai/iam"
	"golang.org/x/oauth2"
)

// White-label IAM resolution (HIP-0111).
//
// ONE cloud binary serves every brand's API host and the console's `/v1/signin`
// OAuth code exchange. Each brand authenticates against its OWN IAM app + issuer
// (hanzo->hanzo.id/hanzo-cloud, lux->lux.id/lux-cloud, zoo->zoolabs.id/zoo-cloud,
// pars->pars.id/pars-cloud). The brand is selected at RUNTIME from the request
// Host, mirroring the console's brandFromHost (src/config/index.ts) so the two
// agree host-for-host.
//
// This is the ONE brand->IAM map in the ai module. `Signin` resolves the brand
// from the request Host and exchanges the code as that brand's confidential
// client; the default is hanzo, so a hanzo host resolves to EXACTLY the values
// InitAuthConfig baked from IAM_CLIENT_ID/IAM_APP_NAME/IAM_ORG/IAM_URL -- hanzo
// behavior is byte-unchanged.
//
// PUBLIC facts (issuer host, client id, org) live in code; the only SECRET --
// each brand's client_secret -- is read from env (KMS-synced), never hard-coded,
// exactly as the existing IAM_CLIENT_SECRET.

// BrandIAM is a brand's resolved IAM identity for the OAuth code exchange and
// token validation. ClientID doubles as the OAuth `aud`/client_id; Issuer is the
// canonical OIDC `iss` a brand's tokens carry.
type BrandIAM struct {
	// Brand is the canonical brand key (hanzo|lux|zoo|pars).
	Brand string
	// Endpoint is the IAM OIDC base the code exchange + userinfo target. In
	// cluster the exchange rides the internal IAM service (IAM_URL); the public
	// issuer host is the value tokens carry (Issuer).
	Endpoint string
	// Issuer is the JWT `iss` a brand's tokens carry (e.g. https://lux.id).
	Issuer string
	// ClientID is the brand's OAuth client_id / IAM application name AND the `aud`
	// its tokens carry (client_id == app == aud, HIP-0111).
	ClientID string
	// ClientSecret is the brand's confidential-client secret (from env/KMS). Empty
	// for a brand whose secret is not provisioned -- the exchange then fails closed
	// with a clear error, never a fabricated token.
	ClientSecret string
	// Org is the brand's IAM organization slug (the tenant owner).
	Org string
}

// brandDef is the PUBLIC per-brand definition (no secret). The secret is resolved
// separately from env so this table can live in code.
type brandDef struct {
	issuer   string
	clientID string
	org      string
	// secretEnv is the env var carrying this brand's client_secret (KMS-synced).
	// hanzo reuses the existing IAM_CLIENT_SECRET so hanzo is byte-unchanged.
	secretEnv string
}

// brandDefs is the ONE brand->IAM registry. Keys are canonical brand IDs. Issuer
// hosts + client ids mirror the console BRANDS map and the verified IAM apps:
// lux-cloud (org lux, cert-lux), zoo-cloud (org zoo, cert-zoo). pars is defined
// for forward-compat; its exchange fails closed until pars-cloud + its secret
// exist (no fabricated login).
var brandDefs = map[string]brandDef{
	"hanzo": {issuer: "https://hanzo.id", clientID: "hanzo-cloud", org: "hanzo", secretEnv: "IAM_CLIENT_SECRET"},
	"lux":   {issuer: "https://lux.id", clientID: "lux-cloud", org: "lux", secretEnv: "LUX_CLOUD_CLIENT_SECRET"},
	"zoo":   {issuer: "https://zoolabs.id", clientID: "zoo-cloud", org: "zoo", secretEnv: "ZOO_CLOUD_CLIENT_SECRET"},
	"pars":  {issuer: "https://pars.id", clientID: "pars-cloud", org: "pars", secretEnv: "PARS_CLOUD_CLIENT_SECRET"},
}

// defaultBrand is the fallback for an unknown/empty Host -- hanzo, so the default
// path is unchanged.
const defaultBrand = "hanzo"

// hostBrand maps a Host suffix to a brand. First match wins; order mirrors the
// console HOST_BRANDS list so the backend and SPA resolve identically.
var hostBrand = []struct {
	suffix string
	brand  string
}{
	{"hanzo.ai", "hanzo"},
	{"lux.cloud", "lux"},
	{"lux.network", "lux"},
	{"lux.id", "lux"},
	{"zoo.cloud", "zoo"},
	{"zoo.ngo", "zoo"},
	{"zoo.network", "zoo"},
	{"zoolabs.id", "zoo"},
	{"hanzo.id", "hanzo"},
	{"pars.cloud", "pars"},
	{"pars.network", "pars"},
	{"pars.id", "pars"},
}

// brandFromHost resolves the brand id from a request Host (port/case-insensitive).
// Unknown/empty -> the default brand (hanzo). Mirrors console brandFromHost so a
// host resolves to the same brand on both ends.
func brandFromHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i] // strip :port
	}
	if h != "" {
		for _, e := range hostBrand {
			if h == e.suffix || strings.HasSuffix(h, "."+e.suffix) {
				return e.brand
			}
		}
	}
	return defaultBrand
}

// iamEndpoint is the IAM base the code exchange + userinfo target. In cluster the
// deployment sets IAM_URL to the internal service (http://iam.hanzo.svc), a single
// Casdoor that serves every brand host -- so ONE endpoint validates/exchanges all
// brands (the token still carries the per-brand public issuer). Falls back to the
// brand's public issuer host when IAM_URL is unset (dev / direct).
func iamEndpoint(issuer string) string {
	if v := strings.TrimSpace(conf.GetConfigString("IAM_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return strings.TrimRight(issuer, "/")
}

// resolveBrandIAM resolves the full BrandIAM for a request Host. The default
// (hanzo) returns EXACTLY the values InitAuthConfig baked (IAM_CLIENT_ID /
// IAM_APP_NAME / IAM_ORG / IAM_CLIENT_SECRET), so the hanzo signin path is
// unchanged; only a lux/zoo/pars host resolves to its own client + secret + issuer.
func resolveBrandIAM(host string) BrandIAM {
	brand := brandFromHost(host)
	d := brandDefs[brand]

	// hanzo defaults to the baked config values (byte-unchanged). This also lets a
	// single-tenant deployment PIN the identity via env without a brand: if
	// IAM_CLIENT_ID/IAM_APP_NAME/IAM_ORG are set they win for the hanzo brand,
	// exactly as InitAuthConfig read them.
	clientID := d.clientID
	org := d.org
	if brand == defaultBrand {
		if v := strings.TrimSpace(conf.GetConfigString("IAM_CLIENT_ID")); v != "" {
			clientID = v
		} else if v := strings.TrimSpace(conf.GetConfigString("IAM_APP_NAME")); v != "" {
			clientID = v
		}
		if v := strings.TrimSpace(conf.GetConfigString("IAM_ORG")); v != "" {
			org = v
		}
	}

	return BrandIAM{
		Brand:        brand,
		Endpoint:     iamEndpoint(d.issuer),
		Issuer:       d.issuer,
		ClientID:     clientID,
		ClientSecret: strings.TrimSpace(os.Getenv(d.secretEnv)),
		Org:          org,
	}
}

// brandIssuers returns the set of trusted OIDC issuers across every configured
// brand. The request-auth issuer policy (object.ValidateJWTIssAud) accepts a token
// whose `iss` is ANY of these, so a lux token (iss=https://lux.id) validates on the
// same binary as a hanzo token -- the multi-brand equivalent of the single
// expectedJWTIssuer(). One source of truth: derived from the same brandDefs.
func brandIssuers() []string {
	out := make([]string, 0, len(brandDefs))
	for _, d := range brandDefs {
		out = append(out, d.issuer)
	}
	return out
}

// brandClientCache caches one *iam.Client per brand so Signin does not rebuild an
// SDK client (and its JWKS cache) on every login. Keyed by brand id.
var (
	brandClientCache   = map[string]*iam.Client{}
	brandClientCacheMu sync.RWMutex
)

// authClient is the minimal IAM confidential-client surface Signin needs: the
// OAuth code exchange + JWT parse. Both *iam.Client (per-brand) and the SDK's
// package-global functions satisfy it, so the hanzo default can reuse the
// already-initialized global verbatim while other brands use their own client.
type authClient interface {
	GetOAuthToken(code, state string, opts ...iam.OAuthOption) (*oauth2.Token, error)
	ParseJwtToken(token string) (*iam.Claims, error)
}

// globalAuthClient adapts the SDK package-global (the client InitAuthConfig built
// from IAM_CLIENT_ID/IAM_APP_NAME/IAM_ORG/IAM_CLIENT_SECRET) to authClient, so the
// hanzo default path is EXACTLY today's behavior -- byte-unchanged.
type globalAuthClient struct{}

func (globalAuthClient) GetOAuthToken(code, state string, opts ...iam.OAuthOption) (*oauth2.Token, error) {
	return iam.GetOAuthToken(code, state, opts...)
}
func (globalAuthClient) ParseJwtToken(token string) (*iam.Claims, error) {
	return iam.ParseJwtToken(token)
}

// brandAuthClient returns the confidential IAM client for a resolved brand. For
// the hanzo default it returns the SDK's package-global (the exact client
// InitAuthConfig set), so the hanzo exchange is byte-unchanged. Every other brand
// gets its own cached *iam.Client (its endpoint + client_id + client_secret). A
// brand whose client_secret is not provisioned fails closed with a clear error --
// NEVER a silent fallback to another brand's client (exchanging a code as the
// wrong application is the very bug this closes).
func brandAuthClient(b BrandIAM) (authClient, error) {
	if b.Brand == defaultBrand {
		// Reuse the package-global verbatim (byte-unchanged hanzo path). It is set
		// once at InitAuthConfig; if that failed (IAM_URL unset), the exchange
		// surfaces the SDK's own nil-client error, exactly as before this change.
		return globalAuthClient{}, nil
	}
	if b.ClientID == "" {
		return nil, fmt.Errorf("iam: no client configured for brand %q", b.Brand)
	}
	if b.ClientSecret == "" {
		return nil, fmt.Errorf("iam: %s sign-in is not configured on this deployment (missing client secret for %s)", b.Brand, b.ClientID)
	}

	brandClientCacheMu.RLock()
	if cl, ok := brandClientCache[b.Brand]; ok {
		brandClientCacheMu.RUnlock()
		return cl, nil
	}
	brandClientCacheMu.RUnlock()

	// Build the per-brand client. Certificate is left empty: ParseJwtToken prefers
	// the published JWKS at <Endpoint>/v1/iam/.well-known/jwks (keyed by the token
	// kid), and the in-cluster IAM serves EVERY brand's signing cert in one JWKS
	// (cert-hanzo/cert-lux/cert-zoo/...), so a brand token verifies by kid without a
	// configured PEM. This matches the SDK's own JWKS-first design.
	cl := iam.NewClient(b.Endpoint, b.ClientID, b.ClientSecret, "", b.Org, b.ClientID)

	brandClientCacheMu.Lock()
	brandClientCache[b.Brand] = cl
	brandClientCacheMu.Unlock()
	return cl, nil
}
