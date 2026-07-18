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
	"net/http"
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

// defaultMaxMemory bounds the request body CopyBody reads (64MB), matching
// the historical MaxMemory default.
const defaultMaxMemory int64 = 1 << 26

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

// Router registers controllers and filters and serves requests: it builds a
// per-request Context, runs the filter chain around a reflection-dispatched
// controller method, and is itself an http.Handler.
type Router struct {
	routes    []*route
	filters   [FinishRouter + 1][]*filterEntry
	maxMemory int64
}

// NewRouter returns an empty Router.
func NewRouter() *Router {
	return &Router{maxMemory: defaultMaxMemory}
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
func (p *Router) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	ctx := NewContext()
	ctx.Reset(rw, r)
	ctx.Input.CopyBody(p.maxMemory)

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

	urlPath := r.URL.Path

	if p.execFilter(ctx, urlPath, BeforeRouter) {
		return
	}

	rt, params, found := p.match(urlPath)
	if !found {
		http.NotFound(ctx.ResponseWriter, r)
		return
	}
	for k, v := range params {
		ctx.Input.SetParam(k, v)
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

// match finds the first route whose segments match the path and returns its
// captured route parameters.
func (p *Router) match(path string) (*route, map[string]string, bool) {
	segs := splitPath(path)
	for _, rt := range p.routes {
		if len(rt.segments) != len(segs) {
			continue
		}
		params := map[string]string{}
		ok := true
		for i, seg := range rt.segments {
			if strings.HasPrefix(seg, ":") {
				params[seg[1:]] = segs[i]
			} else if seg != segs[i] {
				ok = false
				break
			}
		}
		if ok {
			return rt, params, true
		}
	}
	return nil, nil, false
}

// splitPath splits a URL path into non-empty segments.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
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
