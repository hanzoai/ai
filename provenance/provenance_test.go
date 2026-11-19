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

package provenance

import (
	"strings"
	"testing"
)

const hex64 = "4b227777d4dd1fc61c6f884f48641d02b4d121d3fd328cb08b5531fcacdabf8a"

// A digest is an immutable content hash or nothing: anything else a node reports
// is read as Unknown, so a trace never carries a tag, a path or a hostname where a
// digest belongs.
func TestADigestIsAContentHashOrUnknown(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"sha256:" + hex64, "sha256:" + hex64},
		{"  SHA256:" + strings.ToUpper(hex64) + " ", "sha256:" + hex64},
		// An image reference pinned by digest keeps the digest alone: the registry
		// and repository are not what identifies the bytes, and need not be shared.
		{"registry.internal:5000/hanzo/engine@sha256:" + hex64, "sha256:" + hex64},
		{"sha512:" + strings.Repeat("ab", 64), "sha512:" + strings.Repeat("ab", 64)},
		{"blake3:" + hex64, "blake3:" + hex64},
		{"", Unknown},
		{"unknown", Unknown},
		{"latest", Unknown},
		{"ghcr.io/hanzoai/engine:v1.7.108", Unknown}, // a tag moves; it is not a digest
		{"sha256:" + hex64[:63], Unknown},            // short
		{"sha256:" + hex64 + "00", Unknown},          // long
		{"sha256:" + strings.Replace(hex64, "4", "g", 1), Unknown},
		{"md5:" + hex64[:32], Unknown}, // not an algorithm we take as immutable
		{"sha256:" + hex64 + "\r\nX-Evil: 1", Unknown},
	} {
		if got := Digest(c.in); got != c.want {
			t.Errorf("Digest(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A connector is one name from a closed vocabulary. What a node says is mapped onto
// it; a URL keeps its scheme alone, so credentials, hosts and paths never reach a
// trace; anything unrecognised is Unknown.
func TestAConnectorIsANameFromTheVocabulary(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"in-memory", InMemory},
		{"GPU", InMemory},
		{"memory", InMemory},
		{"cpu-offload", CPUOffload},
		{"cpu_offload", CPUOffload},
		{"OffloadingConnector", CPUOffload},
		{"disk-offload", DiskOffload},
		{"SharedStorageConnector", DiskOffload},
		{"LMCacheConnectorV1", "external:lmcache"},
		{"NixlConnector", "external:nixl"},
		{"MooncakeStoreConnector", "external:mooncake"},
		{"P2pNcclConnector", "external:p2p-nccl"},
		{"external:lmcache", "external:lmcache"},
		{"external:kv-fabric", "external:kv-fabric"},
		{"redis://kv-1", "external:redis"},
		{"redis://default:hunter2@kv-1.internal:6379/0", "external:redis"},
		{"rediss://u:p@kv:6380", "external:redis"},
		{"s3://bucket/prefix?X-Amz-Signature=abc", "external:s3"},
		{"external:sk-live-abc123", ExternalOther}, // an external we cannot name is not echoed
		{"external", ExternalOther},
		{"ftp://u:p@host/x", ExternalOther},
		{"", Unknown},
		{"unknown", Unknown},
		{"whatever", Unknown},
		{"in-memory\r\nX-Evil: 1", Unknown},
	} {
		if got := Connector(c.in); got != c.want {
			t.Errorf("Connector(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Normalising twice is normalising once, so every reader of a record (the span, the
// warehouse row, a query filter) can apply the rule and agree.
func TestTheRulesAreIdempotent(t *testing.T) {
	for _, in := range []string{"", "x", "sha256:" + hex64, "a@sha256:" + hex64} {
		if once := Digest(in); Digest(once) != once {
			t.Errorf("Digest is not idempotent on %q", in)
		}
	}
	for _, in := range []string{"", "gpu", "redis://a:b@c", "external:foo", "LMCacheConnectorV1"} {
		if once := Connector(in); Connector(once) != once {
			t.Errorf("Connector is not idempotent on %q", in)
		}
	}
}

// Every name the vocabulary can produce is something a filter can be asked for:
// Connectors lists exactly the outputs, so a query surface can validate against it.
func TestConnectorsListsEveryOutput(t *testing.T) {
	known := map[string]bool{}
	for _, c := range Connectors() {
		known[c] = true
		if Connector(c) != c {
			t.Errorf("listed connector %q does not map to itself", c)
		}
	}
	for _, in := range []string{"gpu", "cpu", "disk", "LMCacheConnectorV1", "redis://x", "external:x", "nope"} {
		if !known[Connector(in)] {
			t.Errorf("Connector(%q) = %q is not listed by Connectors()", in, Connector(in))
		}
	}
}
