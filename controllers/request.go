package controllers

import (
	"mime/multipart"
	"net/url"
)

// How a handler reads what it was sent.
//
// The embedded zip context already answers most of it — Body, Param, Query,
// Header. These three are the readings that do not have a one-call equivalent
// there: the merged form+query set that ~320 handlers read by name, and the two
// multipart accessors.

// Input is every value the request carries by name, query and form merged, with
// the form winning — the same order net/http's ParseForm produces. Handlers read
// an identifier as Input().Get("id") whether it arrived as ?id=, as a form field,
// or as a route parameter the router bound.
func (c *ApiController) Input() url.Values {
	f := c.Fiber()
	out := url.Values{}
	for k, v := range f.Queries() {
		out.Set(k, v)
	}
	if form, err := f.MultipartForm(); err == nil && form != nil {
		for k, vs := range form.Value {
			if len(vs) > 0 {
				out.Set(k, vs[0])
			}
		}
	}
	// A route parameter is readable by name too, so /v1/ai/chats/:owner/:name
	// hands its handler an id without the handler learning a second way to ask.
	for _, p := range c.Fiber().Route().Params {
		if v := c.Param(p); v != "" {
			out.Set(p, v)
		}
	}
	return out
}

// GetString is Input().Get with a fallback, for the reads that have a sensible
// default rather than an error.
func (c *ApiController) GetString(key string, def ...string) string {
	if v := c.Input().Get(key); v != "" {
		return v
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// GetFile returns one uploaded part by name.
func (c *ApiController) GetFile(key string) (multipart.File, *multipart.FileHeader, error) {
	h, err := c.Fiber().FormFile(key)
	if err != nil {
		return nil, nil, err
	}
	f, err := h.Open()
	if err != nil {
		return nil, nil, err
	}
	return f, h, nil
}
