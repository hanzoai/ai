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

package plugin

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Scheme is the address scheme a plugin answers at: a family whose address is
// plugin://zen is the plugin registered as "zen", in this process. The address
// decides the transport, so the code that relays to a family is the same code
// whichever answers it.
const Scheme = "plugin"

// The request headers an Ask carries as typed fields, and the X-Hanzo-* names
// an answer's tells are given back under.
const (
	headerOrg          = "X-Org-Id"
	headerRequest      = "X-Request-Id"
	headerCapabilities = "X-Org-Capabilities"
	headerCapture      = "X-Capture"
	headerSpend        = "X-Hanzo-Spend"
	headerFronted      = "X-Hanzo-Fronted-By"

	headerServed   = "X-Hanzo-Served"
	headerArm      = "X-Hanzo-Arm"
	headerProvider = "X-Hanzo-Provider"
	headerFree     = "X-Hanzo-Free"
	headerFailover = "X-Hanzo-Failover"
	headerBound    = "X-Hanzo-Cost-Bound"
	headerCogs     = "X-Hanzo-Cogs"
	headerCost     = "X-Hanzo-Cost"
)

// unsent are request headers that never cross into a plugin: the credential the
// family's service would have checked, and the framing the request's body owns.
var unsent = map[string]bool{"Authorization": true, "Content-Length": true, "Host": true, "Te": true, "Connection": true}

var plugins sync.Map // name -> *Host

// Register makes p answer plugin://<p.Name>, in place of any it replaces. The
// instance starts on its first request.
func Register(p Plugin) {
	plugins.Store(strings.ToLower(p.Name), newHost(p))
}

// Lookup is the host of the plugin registered as name, or nil.
func Lookup(name string) *Host {
	if h, ok := plugins.Load(strings.ToLower(name)); ok {
		return h.(*Host)
	}
	return nil
}

// Transport answers plugin:// requests in process, as an http.RoundTripper: the
// request becomes an Ask, the plugin's answer an http.Response whose tells are
// the X-Hanzo-* headers a family service sends and whose End arrives as its
// trailers. Register it on an http.Transport:
//
//	t.RegisterProtocol(plugin.Scheme, plugin.Transport{})
type Transport struct{}

func (Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	h := Lookup(r.URL.Host)
	if h == nil {
		return nil, fmt.Errorf("plugin %q is not registered", r.URL.Host)
	}
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	ans, err := h.Do(r.Context(), ask(r), body)
	if err != nil {
		return nil, err
	}
	resp := &http.Response{
		Status:        fmt.Sprintf("%d %s", ans.Reply.Status, http.StatusText(ans.Reply.Status)),
		StatusCode:    ans.Reply.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		ContentLength: -1,
		Request:       r,
	}
	for _, kv := range ans.Reply.Headers {
		resp.Header.Add(kv[0], kv[1])
	}
	tell(resp.Header, ans.Reply.Tells)
	if b := ans.Reply.Bound; b != "" {
		resp.Header.Set(headerBound, b)
	}
	if len(ans.Reply.Trailers) > 0 {
		resp.Trailer = http.Header{}
		for _, k := range ans.Reply.Trailers {
			resp.Trailer[http.CanonicalHeaderKey(k)] = nil
		}
	}
	resp.Body = &trailed{ReadCloser: ans.Body, resp: resp, end: ans.End}
	return resp, nil
}

// ask reads r into the Ask a plugin answers.
func ask(r *http.Request) Ask {
	a := Ask{
		Method:    r.Method,
		Path:      r.URL.RequestURI(),
		Org:       r.Header.Get(headerOrg),
		RequestID: r.Header.Get(headerRequest),
		Spend:     r.Header.Get(headerSpend),
		Fronted:   r.Header.Get(headerFronted) != "",
	}
	if c := strings.TrimSpace(r.Header.Get(headerCapabilities)); c != "" {
		a.Capabilities = []byte(c)
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get(headerCapture))) {
	case "1", "true":
		a.Capture = true
	}
	for k, vs := range r.Header {
		switch k = http.CanonicalHeaderKey(k); k {
		case headerOrg, headerRequest, headerCapabilities, headerCapture, headerSpend, headerFronted:
			continue
		}
		if unsent[k] {
			continue
		}
		for _, v := range vs {
			a.Headers = append(a.Headers, [2]string{k, v})
		}
	}
	return a
}

// tell writes t as the X-Hanzo-* fields a family service sends.
func tell(h http.Header, t Tells) {
	if t.Served != "" {
		h.Set(headerServed, t.Served)
	}
	if t.Arm != "" {
		h.Set(headerArm, t.Arm)
	}
	if t.Provider != "" {
		h.Set(headerProvider, t.Provider)
	}
	if t.Free {
		h.Set(headerFree, "true")
	}
	if len(t.Failover) > 0 {
		h.Del(headerFailover)
		for _, f := range t.Failover {
			h.Add(headerFailover, f)
		}
	}
}

// trailed is an answer's body that states its End as the response's trailers
// once it has been read to its end, as an HTTP answer's trailers arrive.
type trailed struct {
	io.ReadCloser
	resp *http.Response
	end  func() End
	once sync.Once
}

func (t *trailed) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if err == io.EOF {
		t.once.Do(func() {
			e := t.end()
			if t.resp.Trailer == nil {
				t.resp.Trailer = http.Header{}
			}
			tell(t.resp.Trailer, e.Tells)
			if e.Cogs != "" {
				t.resp.Trailer.Set(headerCogs, e.Cogs)
			}
			if e.Cost != "" {
				t.resp.Trailer.Set(headerCost, e.Cost)
			}
		})
	}
	return n, err
}
