// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// Package provenance is the one rule for what an inference trace records about the
// runtime that served a call: the immutable digest of its image or serving binary,
// and the KV cache connector it ran with (hanzo-inc/cloud#800).
//
// A serving node states both on its response, as RuntimeDigestHeader and
// CacheConnectorHeader; a router in front of it may state them for a node that
// cannot. Whatever arrives is read through Digest and Connector before it is
// written anywhere, so a trace carries a content hash or Unknown, and a connector
// name from a closed vocabulary or Unknown — never a tag, a host, a path or a
// credential. A node that says nothing reads Unknown, which is itself the answer a
// filter asks for when it looks for calls whose runtime nobody can name.
//
// The package is a leaf: no imports outside the standard library, so a host's query
// surface validates a filter with the same rule the writer applied.
package provenance

import "strings"

// The response headers a serving node (or the router in front of it) states them in.
const (
	RuntimeDigestHeader  = "X-Hanzo-Runtime-Digest"
	CacheConnectorHeader = "X-Hanzo-Cache-Connector"
)

// The gen_ai span attributes they are recorded under, beside the other gen_ai.hanzo.*
// attribution. Both are on every span ai emits, Unknown when no node said.
const (
	AttrRuntimeDigest  = "gen_ai.hanzo.runtime.digest"
	AttrCacheConnector = "gen_ai.hanzo.kv_cache.connector"
)

// Unknown is what a trace records when nothing trustworthy was stated.
const Unknown = "unknown"

// The connector vocabulary. InMemory is a KV cache held in the accelerator's (or
// host's) own memory and nowhere else; CPUOffload spills blocks to host RAM;
// DiskOffload to local or shared storage; external:<name> is a cache served by
// another system (LMCache, Mooncake, NIXL, Redis, …).
const (
	InMemory      = "in-memory"
	CPUOffload    = "cpu-offload"
	DiskOffload   = "disk-offload"
	ExternalOther = "external:other"
)

// externals are the external connectors a trace names. An external this list does
// not hold is ExternalOther: a free-form name is a channel for anything a node
// chooses to send, and the name of a system is all an audit needs.
var externals = []string{"kv-fabric", "lmcache", "mooncake", "nixl", "p2p-nccl", "redis", "s3", "valkey"}

// aliases maps what serving runtimes call their connectors onto the vocabulary:
// vLLM's KV connector class names, short spellings, and URL schemes.
var aliases = map[string]string{
	"in-memory": InMemory, "inmemory": InMemory, "memory": InMemory, "gpu": InMemory,
	"device": InMemory, "hbm": InMemory, "vram": InMemory,

	"cpu-offload": CPUOffload, "cpu_offload": CPUOffload, "cpu": CPUOffload,
	"offload": CPUOffload, "offloadingconnector": CPUOffload, "cpuoffloadingconnector": CPUOffload,

	"disk-offload": DiskOffload, "disk_offload": DiskOffload, "disk": DiskOffload,
	"sharedstorageconnector": DiskOffload,

	"lmcacheconnector": "external:lmcache", "lmcacheconnectorv1": "external:lmcache",
	"nixlconnector":     "external:nixl",
	"mooncakeconnector": "external:mooncake", "mooncakestoreconnector": "external:mooncake",
	"p2pncclconnector": "external:p2p-nccl",
	"hanzo-kv-fabric":  "external:kv-fabric",

	// URL schemes: the scheme names the system; the rest of a URL is never read.
	"rediss": "external:redis",
}

// digestHex is each algorithm a trace takes as immutable, with its hex length.
var digestHex = map[string]int{"sha256": 64, "sha384": 96, "sha512": 128, "blake3": 64}

// Digest reads a stated runtime digest: `<algorithm>:<hex>` for sha256, sha384,
// sha512 or blake3, lower-cased, or an image reference pinned by one
// (`repo@sha256:…`), of which only the digest is kept. Anything else is Unknown.
func Digest(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	algo, hex, ok := strings.Cut(s, ":")
	if !ok {
		return Unknown
	}
	n, ok := digestHex[algo]
	if !ok || len(hex) != n {
		return Unknown
	}
	for i := 0; i < len(hex); i++ {
		if c := hex[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return Unknown
		}
	}
	return algo + ":" + hex
}

// Connector reads a stated KV cache connector onto the vocabulary: a canonical name,
// a runtime's own name for one, `external:<name>`, or a URL, of which only the
// scheme is read. Anything else is Unknown.
func Connector(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == Unknown {
		return Unknown
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x21 || c > 0x7e {
			return Unknown
		}
	}
	if scheme, _, ok := strings.Cut(s, "://"); ok {
		return external(scheme)
	}
	if s == "external" {
		return ExternalOther
	}
	if name, ok := strings.CutPrefix(s, "external:"); ok {
		return external(name)
	}
	if c, ok := aliases[s]; ok {
		return c
	}
	return Unknown
}

// external names an external connector, ExternalOther when it is not one we list.
func external(name string) string {
	if c, ok := aliases[name]; ok && strings.HasPrefix(c, "external:") {
		return c
	}
	for _, e := range externals {
		if name == e {
			return "external:" + e
		}
	}
	return ExternalOther
}

// Connectors is every value Connector can return, Unknown included: what a query
// surface accepts as a connector filter.
func Connectors() []string {
	out := []string{InMemory, CPUOffload, DiskOffload}
	for _, e := range externals {
		out = append(out, "external:"+e)
	}
	return append(out, ExternalOther, Unknown)
}
