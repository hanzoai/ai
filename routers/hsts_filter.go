// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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

package routers

import (
	"github.com/zap-proto/zip"
)

// HstsFilter adds HTTP Strict Transport Security header to HTTPS responses
// This ensures browsers only access the website using HTTPS
func HstsFilter(c *zip.Ctx) error {
	// Only set HSTS header on HTTPS requests
	// Check both the direct TLS connection and X-Forwarded-Proto header (for reverse proxies)
	if c.Fiber().Scheme() == "https" || c.Header("X-Forwarded-Proto") == "https" {
		// Set HSTS header with:
		// - max-age=31536000 (1 year in seconds)
		// - includeSubDomains (apply to all subdomains)
		// - preload (allow inclusion in browser preload lists)
		c.SetHeader("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
	}
	return c.Continue()
}
