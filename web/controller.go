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
	"mime/multipart"
	"net/url"
)

// ErrAbort is the panic value StopRun raises to unwind a handler; the router
// recovers it and stops running the request without treating it as a crash.
var ErrAbort = errors.New("web: stop run")

// ModeProd is the run mode under which ServeJSON omits indentation.
const ModeProd = "prod"

// RunMode selects response formatting: any value other than ModeProd
// pretty-prints ServeJSON output. It defaults to ModeProd (compact), matching
// the runtime default; the serving layer sets it from config when configured.
var RunMode = ModeProd

// ControllerInterface is the contract the router drives per request: build the
// controller with Init, run Prepare before the handler and Finish after.
type ControllerInterface interface {
	Init(ctx *Context, controllerName, actionName string, app interface{})
	Prepare()
	Finish()
}

// Controller is the base a request handler embeds. It holds the request
// Context, the response Data bag and the session store.
type Controller struct {
	Ctx        *Context
	Data       map[interface{}]interface{}
	CruSession Store

	// EnableRender is retained for handler compatibility. Handlers write their
	// own responses (ServeJSON, Output.Body), so the router renders nothing;
	// toggling this flag has no effect on the response.
	EnableRender bool

	controllerName string
	actionName     string
	AppController  interface{}
}

// Init binds the controller to a request. Data aliases the Input's per-request
// map, so values set through Ctx.Input.SetData and c.Data are the same map.
func (c *Controller) Init(ctx *Context, controllerName, actionName string, app interface{}) {
	c.Ctx = ctx
	c.controllerName = controllerName
	c.actionName = actionName
	c.AppController = app
	c.EnableRender = true
	c.Data = ctx.Input.Data()
}

// Prepare runs before the handler. The base does nothing; a controller may
// override it.
func (c *Controller) Prepare() {}

// Finish runs after the handler. The base does nothing; a controller may
// override it.
func (c *Controller) Finish() {}

// StopRun unwinds the current handler via a recovered panic.
func (c *Controller) StopRun() {
	panic(ErrAbort)
}

// Input parses and returns the request form values (query plus body form).
func (c *Controller) Input() url.Values {
	if c.Ctx.Request.Form == nil {
		c.Ctx.Request.ParseForm()
	}
	return c.Ctx.Request.Form
}

// GetFile returns the uploaded multipart file for a form key.
func (c *Controller) GetFile(key string) (multipart.File, *multipart.FileHeader, error) {
	return c.Ctx.Request.FormFile(key)
}

// GetString returns a route parameter or form value for key, or the optional
// default when unset.
func (c *Controller) GetString(key string, def ...string) string {
	if v := c.Ctx.Input.Query(key); v != "" {
		return v
	}
	if len(def) > 0 {
		return def[0]
	}
	return ""
}

// ServeJSON writes c.Data["json"] as the JSON response body, indented unless
// the run mode is prod. Passing true escapes non-ASCII runes.
func (c *Controller) ServeJSON(encoding ...bool) {
	hasIndent := RunMode != ModeProd
	hasEncoding := len(encoding) > 0 && encoding[0]
	c.Ctx.Output.JSON(c.Data["json"], hasIndent, hasEncoding)
}

// StartSession binds the request session store, taking it from the Input on
// first use, and returns it.
func (c *Controller) StartSession() Store {
	if c.CruSession == nil {
		c.CruSession = c.Ctx.Input.CruSession
	}
	return c.CruSession
}

// SetSession stores a session value.
func (c *Controller) SetSession(name interface{}, value interface{}) {
	if c.CruSession == nil {
		c.StartSession()
	}
	if c.CruSession == nil {
		return
	}
	c.CruSession.Set(name, value)
}

// GetSession returns a session value, or nil when no session is bound.
func (c *Controller) GetSession(name interface{}) interface{} {
	if c.CruSession == nil {
		c.StartSession()
	}
	if c.CruSession == nil {
		return nil
	}
	return c.CruSession.Get(name)
}

// DelSession deletes a session value.
func (c *Controller) DelSession(name interface{}) {
	if c.CruSession == nil {
		c.StartSession()
	}
	if c.CruSession == nil {
		return
	}
	c.CruSession.Delete(name)
}
