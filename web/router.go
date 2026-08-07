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

package web

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/hanzoai/ai/log"
)

// errInvalidPosition is returned by InsertFilter for an out-of-range position.
var errInvalidPosition = errors.New("web: invalid filter position")

// Filter positions, ordered as they run around a request.
const (
	BeforeStatic = iota
	BeforeRouter
	BeforeExec
	AfterExec
	FinishRouter
)

// MaxBody bounds what one request body may occupy in this process, in bytes.
//
// It is the DECOMPRESSED size — the bytes a handler is handed — because that is
// the only reading that holds for a compressed body. It is exported so every
// decoder in the tree bounds itself by the SAME number: a body buys the same
// allowance whether it arrives gzipped, zstd-framed, or plain, and there is one
// limit to reason about instead of one per encoding.
//
// 64 MiB is the bound plain bodies have always had here, so nothing legitimate
// is newly refused: the largest real /v1 payloads are an audio upload (25 MiB,
// bounded again at MaxTranscribeUpload) and a max-context chat prompt (~4 MB of
// JSON). It is also far under any pod's memory, which is what a bound has to be
// to mean anything — the read happens above the filter chain, so whatever it
// admits, an unauthenticated caller can ask for.
const MaxBody int64 = 1 << 26

// FilterFunc runs against a request context at a filter position.
type FilterFunc func(*Context)

type filterEntry struct {
	pattern        string
	filter         FilterFunc
	returnOnOutput bool
}

// route holds a registered controller and its method mapping for a URL
// pattern split into segments (a ":name" segment is a route parameter).
type route struct {
	segments []string
	ctrlType reflect.Type
	methods  map[string]string // upper-case HTTP method -> handler method name; "*" matches any
}

// SessionManager starts the session store for a request. A provider that
// matches this shape (the session provider does) binds per-request sessions
// without the router depending on the provider.
type SessionManager interface {
	SessionStart(w http.ResponseWriter, r *http.Request) (Store, error)
}

// Router registers controllers and filters and serves requests: it builds a
// per-request Context, runs the filter chain around a reflection-dispatched
// controller method, and is itself an http.Handler.
type Router struct {
	routes    []*route
	filters   [FinishRouter + 1][]*filterEntry
	maxMemory int64
	sessions  SessionManager
}

// NewRouter returns an empty Router.
func NewRouter() *Router {
	return &Router{maxMemory: MaxBody}
}

// UseSessions binds a session manager. When set, ServeHTTP starts a session
// before the filter chain (so the auth filters and controllers read it) and
// releases it after the response.
func (p *Router) UseSessions(m SessionManager) {
	p.sessions = m
}

// Router registers a controller for a URL pattern with a method mapping such
// as "GET:List;POST:Create". The controller value supplies the type that is
// freshly instantiated per request.
func (p *Router) Router(pattern string, c ControllerInterface, mapping string) {
	p.routes = append(p.routes, &route{
		segments: splitPath(pattern),
		ctrlType: reflect.Indirect(reflect.ValueOf(c)).Type(),
		methods:  parseMapping(mapping),
	})
}

// InsertFilter adds a filter at a position for a URL pattern. The optional
// first bool is returnOnOutput (default true): a true filter that writes the
// response short-circuits the chain.
func (p *Router) InsertFilter(pattern string, pos int, filter FilterFunc, params ...bool) error {
	if pos < BeforeStatic || pos > FinishRouter {
		return errInvalidPosition
	}
	returnOnOutput := true
	if len(params) > 0 {
		returnOnOutput = params[0]
	}
	p.filters[pos] = append(p.filters[pos], &filterEntry{
		pattern:        pattern,
		filter:         filter,
		returnOnOutput: returnOnOutput,
	})
	return nil
}

// ServeHTTP builds the request context, caches the body, runs the filter
// chain around the matched controller method, and recovers a StopRun abort or
// a handler panic.
//
// A body over the bound is refused here, above the filter chain and above the
// router, because that is where the body was read: every handler below this
// line is entitled to assume the bytes it was handed are all the bytes that
// were sent. Answering 413 with the bound named is also the only answer that
// tells a caller what to do — a truncated body reaches the handler as a
// half-parsed form and is reported as whatever field went missing.
func (p *Router) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	ctx := NewContext()
	ctx.Reset(rw, r)
	ctx.Input.CopyBody(p.maxMemory)
	if ctx.Input.BodyTooLarge {
		ctx.Output.SetStatus(http.StatusRequestEntityTooLarge)
		ctx.Output.Header("Content-Type", "application/json; charset=utf-8")
		ctx.Output.Body([]byte(fmt.Sprintf(
			`{"status":"error","msg":"request body exceeds the %d MiB limit"}`, p.maxMemory>>20)))
		return
	}

	defer func() {
		if err := recover(); err != nil {
			if err == ErrAbort {
				return
			}
			log.Error("web: panic serving %s %s: %v\n%s", r.Method, r.URL.Path, err, debug.Stack())
			if !ctx.ResponseWriter.Started {
				http.Error(ctx.ResponseWriter, "Internal Server Error", http.StatusInternalServerError)
			}
		}
	}()

	// Bind the request session before the filter chain so the auth filters and
	// controllers read it; release it after the response (also on panic, since
	// this defer runs before the recover above).
	if p.sessions != nil {
		if store, err := p.sessions.SessionStart(ctx.ResponseWriter, r); err == nil && store != nil {
			ctx.Input.CruSession = store
			defer store.SessionRelease(ctx.ResponseWriter)
		}
	}

	urlPath := r.URL.Path

	if p.execFilter(ctx, urlPath, BeforeRouter) {
		return
	}

	// BeforeRouter filters (the /v1/cloud and /v1/iam rewrites) mutate
	// Request.URL.Path; route on the live post-filter path. The filter chain
	// keeps selecting on the entry path — the filters read the live path from
	// the context themselves.
	rt, params, found := p.match(r.URL.Path)
	if !found {
		http.NotFound(ctx.ResponseWriter, r)
		return
	}
	// A route parameter is readable through EVERY accessor, not just Input.Param.
	// Handlers overwhelmingly read identifiers as `c.Input().Get("id")`, which is
	// Request.Form — so a `:id` captured from the path must land there too, or a
	// REST route silently hands the handler an empty id while the same handler
	// works fine when the id arrives as ?id=. Merging here, in the ONE place route
	// params are bound, is what lets a resource live at /v1/iam/users/:id without
	// every handler learning a second way to read its own id.
	//
	// ParseForm first (it would otherwise overwrite this), and only fill keys the
	// request did not already carry, so an explicit query value still wins.
	if len(params) > 0 {
		if r.Form == nil {
			r.ParseForm()
		}
		for k, v := range params {
			ctx.Input.SetParam(k, v)
			if r.Form != nil && r.Form.Get(k) == "" {
				r.Form.Set(k, v)
			}
		}
		// A route that captures :owner and :name declares a COMPOSITE identity —
		// which is what every object in this system actually has (object.GetStore
		// and its siblings all begin by splitting an "owner/name" id). Compose it
		// once, here, so ~200 handlers keep reading their id the single way they
		// always have: c.Input().Get("id").
		//
		// This is why the member URL is /:owner/:name rather than /:id — a single
		// segment cannot carry a value containing a slash, since Go decodes %2F
		// back into URL.Path before the router ever sees it.
		if r.Form != nil && r.Form.Get("id") == "" {
			if owner, name := params["owner"], params["name"]; owner != "" && name != "" {
				r.Form.Set("id", owner+"/"+name)
				ctx.Input.SetParam("id", owner+"/"+name)
			}
		}
	}

	handler, ok := rt.methods[strings.ToUpper(r.Method)]
	if !ok {
		handler, ok = rt.methods["*"]
	}
	if !ok {
		http.Error(ctx.ResponseWriter, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if p.execFilter(ctx, urlPath, BeforeExec) {
		return
	}

	p.dispatch(ctx, rt, handler)

	p.execFilter(ctx, urlPath, AfterExec)
}

// execFilter runs the filters at a position in registration order. A
// returnOnOutput filter stops the chain once the response has started.
func (p *Router) execFilter(ctx *Context, urlPath string, pos int) bool {
	for _, f := range p.filters[pos] {
		if f.returnOnOutput && ctx.ResponseWriter.Started {
			return true
		}
		if matchFilterPattern(f.pattern, urlPath) {
			f.filter(ctx)
		}
		if f.returnOnOutput && ctx.ResponseWriter.Started {
			return true
		}
	}
	return false
}

// dispatch instantiates a fresh controller, runs Init, Prepare, the mapped
// method and Finish. A StopRun abort inside Prepare or the method unwinds to
// the recover here so Finish still runs; other panics propagate to ServeHTTP.
func (p *Router) dispatch(ctx *Context, rt *route, handler string) {
	vc := reflect.New(rt.ctrlType)
	execController, ok := vc.Interface().(ControllerInterface)
	if !ok {
		http.Error(ctx.ResponseWriter, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	execController.Init(ctx, rt.ctrlType.Name(), handler, vc.Interface())

	func() {
		defer func() {
			if err := recover(); err != nil && err != ErrAbort {
				panic(err)
			}
		}()
		execController.Prepare()
		if ctx.ResponseWriter.Started {
			return
		}
		if m := vc.MethodByName(handler); m.IsValid() {
			m.Call(nil)
		}
	}()

	execController.Finish()
}

// match finds the route that best matches the path and returns its captured
// route parameters.
//
// "Best" is the MOST SPECIFIC match — the one with the fewest parameter
// segments — not the first one registered. Both /v1/rag/stores/:id and
// /v1/rag/stores/names match "/v1/rag/stores/names"; the literal is what the
// author meant, and it wins here no matter which was registered first.
//
// This is deliberate. First-match-wins makes routing depend on registration
// ORDER, so adding a resource can silently swallow an existing sibling route
// with no error anywhere — the shadowed endpoint simply starts answering with
// the wrong handler, and its id parameter quietly becomes the literal segment.
// Specificity ordering makes that class of bug impossible to introduce, which
// matters most for the generated resource surface (see routers/resources.go),
// where routes are emitted by a loop and nobody is choosing their order.
func (p *Router) match(urlPath string) (*route, map[string]string, bool) {
	segs := splitPath(urlPath)
	var (
		best       *route
		bestParams map[string]string
		bestScore  = -1
	)
	for _, rt := range p.routes {
		if len(rt.segments) != len(segs) {
			continue
		}
		params := map[string]string{}
		ok := true
		// score = count of literal segments; higher is more specific.
		score := 0
		for i, seg := range rt.segments {
			if strings.HasPrefix(seg, ":") {
				params[seg[1:]] = segs[i]
			} else if seg != segs[i] {
				ok = false
				break
			} else {
				score++
			}
		}
		if ok && score > bestScore {
			best, bestParams, bestScore = rt, params, score
		}
	}
	if best == nil {
		return nil, nil, false
	}
	return best, bestParams, true
}

// splitPath cleans a URL path (resolving "", ".", ".." and empty segments the
// way a served path is normalized) and splits it into non-empty segments.
func splitPath(p string) []string {
	p = strings.Trim(path.Clean(p), "/")
	if p == "" || p == "." {
		return []string{}
	}
	return strings.Split(p, "/")
}

// parseMapping parses "GET:List;POST:Create" into an upper-case method to
// handler-name map. A "*" method matches any request method.
func parseMapping(mapping string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(mapping, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		verb, handler, found := strings.Cut(part, ":")
		if !found {
			continue
		}
		m[strings.ToUpper(strings.TrimSpace(verb))] = strings.TrimSpace(handler)
	}
	return m
}

// matchFilterPattern matches a filter pattern against a path: "*" and "/*"
// match everything, a trailing "/*" matches by prefix, otherwise the match is
// exact.
func matchFilterPattern(pattern, path string) bool {
	if pattern == "*" || pattern == "/*" {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == path
}

// Patterns returns every registered route pattern, in registration order, with
// its methods — e.g. "/v1/ai/stores" -> ["GET","POST"].
//
// Exported so a caller can assert things ABOUT its own route table that the table
// cannot see from the inside: most usefully, that a generated route never lands on
// a pattern a hand-written one already claimed. Two registrations of one pattern
// do not error here — the more specific, or the earlier, simply wins — so without
// a check like that the loser is silently unreachable.
func (p *Router) Patterns() map[string][]string {
	out := make(map[string][]string, len(p.routes))
	for _, rt := range p.routes {
		pat := "/" + strings.Join(rt.segments, "/")
		for m := range rt.methods {
			out[pat] = append(out[pat], m)
		}
	}
	return out
}
