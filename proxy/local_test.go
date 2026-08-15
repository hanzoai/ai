// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package proxy

import (
	"net"
	"net/http"
	"testing"
)

// A client that accepts any certificate hands its API key to whoever presents
// one, and the call still succeeds — so nothing downstream can notice. These name
// the endpoints that carry a key and assert each takes the verifying default.
func TestAVendorEndpointIsVerified(t *testing.T) {
	for _, url := range []string{
		"https://inference.do-ai.run/v1",
		"https://api.fireworks.ai/inference/v1",
		"https://openrouter.ai/api/v1",
		"https://api.openai.com/v1",
		"https://example.com:8443/v1",
	} {
		if Local(url) != nil {
			t.Fatalf("%s would accept ANY certificate — the key goes to whoever presents one", url)
		}
	}
}

// The exemption exists because a model server on this machine has a self-signed
// certificate: nobody issues one for 127.0.0.1.
func TestThisMachineKeepsTheExemption(t *testing.T) {
	for _, url := range []string{
		"http://127.0.0.1:1234/v1",
		"https://localhost:8080/v1",
		"http://[::1]:1234/v1",
		"https://LOCALHOST:9000/v1",
	} {
		if Local(url) == nil {
			t.Fatalf("%s should keep the self-signed exemption", url)
		}
	}
}

// A host that merely reads like this machine is remote, and so is anything that
// cannot be parsed — the doubtful case verifies.
func TestALookalikeHostIsRemote(t *testing.T) {
	for _, url := range []string{
		"https://localhost.evil.com/v1",
		"https://127.0.0.1.evil.com/v1",
		"https://not-localhost/v1",
		"://broken",
		"",
	} {
		if Local(url) != nil {
			t.Fatalf("%q was treated as this machine — it would skip verification", url)
		}
	}
}

// The socks5 client carries the same API keys to the same vendors, just through a
// tunnel. A tunnel moves TCP; TLS still terminates at the real host, so it
// verifies like any other connection — and must, or the operator who runs the
// proxy reads every credential passing through it.
func TestTheSocks5ClientVerifies(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	t.Setenv("socks5Proxy", ln.Addr().String())

	tr, ok := getProxyHttpClient().Transport.(*http.Transport)
	if !ok {
		t.Fatal("the socks5 branch did not build a transport — this test stopped covering it")
	}
	if tr.Dial == nil {
		t.Fatal("no tunnel dialer — this test stopped covering the socks5 branch")
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("the tunnelled client accepts ANY certificate — the proxy operator reads every key through it")
	}
}
