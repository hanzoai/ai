// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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
	"crypto/tls"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/hanzoai/ai/conf"
	"github.com/hanzoai/ai/log"
	"golang.org/x/net/proxy"
)

var (
	DefaultHttpClient *http.Client
	ProxyHttpClient   *http.Client
)

func InitHttpClient() {
	// not use proxy
	DefaultHttpClient = http.DefaultClient

	// use proxy
	ProxyHttpClient = getProxyHttpClient()
}

func isAddressOpen(address string) bool {
	timeout := time.Millisecond * 100
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		// cannot connect to address, proxy is not active
		return false
	}

	if conn != nil {
		defer conn.Close()
		log.Info("Socks5 proxy enabled: %s", address)
		return true
	}

	return false
}

func getProxyHttpClient() *http.Client {
	socks5Proxy := conf.GetConfigString("socks5Proxy")
	if socks5Proxy == "" {
		return &http.Client{}
	}

	if !isAddressOpen(socks5Proxy) {
		return &http.Client{}
	}

	// https://stackoverflow.com/questions/33585587/creating-a-go-socks5-client
	dialer, err := proxy.SOCKS5("tcp", socks5Proxy, nil, proxy.Direct)
	if err != nil {
		panic(err)
	}

	tr := &http.Transport{Dial: dialer.Dial}
	return &http.Client{
		Transport: tr,
	}
}

// GetHttpClient answers the client a URL should be fetched with, and always
// answers one.
//
// The two package clients are filled by InitHttpClient at boot, so anything
// running before that line got nil and dereferenced it — a panic rather than a
// request. Three tests already worked around it by calling InitHttpClient
// themselves, which is the ordering problem stated out loud.
//
// The fallback is http.DefaultClient, which is what the non-proxy path resolves
// to anyway; a deployment that configured a proxy still gets it, because by then
// InitHttpClient has run.
func GetHttpClient(url string) *http.Client {
	if strings.Contains(url, "githubusercontent.com") || strings.Contains(url, "googleusercontent.com") || strings.Contains(url, "github.com") {
		if ProxyHttpClient != nil {
			return ProxyHttpClient
		}
		return http.DefaultClient
	}
	if DefaultHttpClient != nil {
		return DefaultHttpClient
	}
	return http.DefaultClient
}

// Local returns the client to use for a base URL, or nil to take the verifying
// default. A non-nil result accepts any certificate and is returned ONLY for this
// machine, where a model server's certificate is self-signed because nobody issues
// one for 127.0.0.1. Anything that does not clearly name this machine — a
// look-alike host, an unparseable URL — is remote and verifies.
func Local(url string) *http.Client {
	if !loopback(url) {
		return nil
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func loopback(raw string) bool {
	u, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
