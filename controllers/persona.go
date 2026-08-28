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

// Persona controller — the /v1/persona surface. List and get read the stock
// catalogue compiled into this binary together with the caller's own saved
// personas; save and delete touch only the caller's own. A caller who saves
// under a catalogue name shadows it for themselves alone, and deleting theirs
// uncovers the original, so there is one namespace and no copy-to-edit step.
//
// SECURITY: org + userId are resolved ONLY from the credential this process
// verified — never from a header, never from the request body. That pair decides
// whose personas a request touches, so nothing a caller can write may contribute
// to it.

package controllers

import (
	"encoding/json"
	"strings"

	"github.com/hanzoai/ai/object"
)

// personaRequest is the POST body for save and delete. It carries NO
// owner/userId field: identity is never accepted from the body.
type personaRequest struct {
	Name          string              `json:"name"`
	DisplayName   string              `json:"displayName"`
	Category      string              `json:"category"`
	Description   string              `json:"description"`
	Philosophy    string              `json:"philosophy"`
	Ocean         object.Ocean        `json:"ocean"`
	Tools         object.PersonaTools `json:"tools"`
	Contributions []string            `json:"contributions"`
	Quotes        []string            `json:"quotes"`
	Prompt        string              `json:"prompt"`
}

// requirePersonaIdentity returns the caller's own (org, userId) or writes an
// auth error and returns ok=false. Same resolution as every other per-user
// surface: the verified principal, never a client header.
func (c *ApiController) requirePersonaIdentity() (string, string, bool) {
	org := c.GetOrg()
	userID := c.memoryUserID()
	if org == "" || userID == "" {
		c.ResponseError(c.T("auth:Please sign in first"))
		return "", "", false
	}
	return org, userID, true
}

// PersonaList
// @Title PersonaList
// @Tag Persona API
// @Description list the persona catalogue together with the caller's own saved personas
// @Param   category    query    string  false  "only personas in this category"
// @Param   q           query    string  false  "match name, description or philosophy"
// @Success 200 {array} object.Persona The Response object
// @router /persona/list [get]
func (c *ApiController) PersonaList() {
	org, userID, ok := c.requirePersonaIdentity()
	if !ok {
		return
	}
	personas, err := object.GetPersonas(org, userID)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}

	category := strings.ToLower(strings.TrimSpace(c.Input().Get("category")))
	query := strings.ToLower(strings.TrimSpace(c.Input().Get("q")))
	if category == "" && query == "" {
		c.ResponseOk(personas)
		return
	}
	filtered := make([]*object.Persona, 0, len(personas))
	for _, p := range personas {
		if category != "" && strings.ToLower(p.Category) != category {
			continue
		}
		if query != "" && !personaMatches(p, query) {
			continue
		}
		filtered = append(filtered, p)
	}
	c.ResponseOk(filtered)
}

// personaMatches reports whether a lowercase query appears in the fields a
// person would search by. Name is included so an @mention completes from a slug.
func personaMatches(p *object.Persona, query string) bool {
	for _, field := range []string{p.Name, p.DisplayName, p.Category, p.Description, p.Philosophy} {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// PersonaGet
// @Title PersonaGet
// @Tag Persona API
// @Description get one persona by name, the caller's own taking precedence over the catalogue
// @Param   name    query    string  true  "persona name"
// @Success 200 {object} object.Persona The Response object
// @router /persona/get [get]
func (c *ApiController) PersonaGet() {
	org, userID, ok := c.requirePersonaIdentity()
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Input().Get("name"))
	if name == "" {
		c.ResponseError(c.T("general:Missing parameter"))
		return
	}
	persona, err := object.GetPersona(org, userID, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if persona == nil {
		c.ResponseError(c.T("general:The object does not exist"))
		return
	}
	c.ResponseOk(persona)
}

// PersonaSystem
// @Title PersonaSystem
// @Tag Persona API
// @Description get the system turn for a persona, ready to prepend to a conversation
// @Param   name    query    string  true  "persona name"
// @Success 200 {object} string The Response object
// @router /persona/system [get]
func (c *ApiController) PersonaSystem() {
	org, userID, ok := c.requirePersonaIdentity()
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Input().Get("name"))
	if name == "" {
		c.ResponseError(c.T("general:Missing parameter"))
		return
	}
	persona, err := object.GetPersona(org, userID, name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	if persona == nil {
		c.ResponseError(c.T("general:The object does not exist"))
		return
	}
	c.ResponseOk(persona.System())
}

// PersonaSave
// @Title PersonaSave
// @Tag Persona API
// @Description create or replace the caller's own persona
// @Param body body controllers.personaRequest true "the persona"
// @Success 200 {object} object.Persona The Response object
// @router /persona/save [post]
func (c *ApiController) PersonaSave() {
	org, userID, ok := c.requirePersonaIdentity()
	if !ok {
		return
	}
	var req personaRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	if !object.ValidPersonaName(strings.TrimSpace(req.Name)) {
		c.ResponseError(c.T("persona:The persona name should be lowercase letters, digits, hyphen or underscore"))
		return
	}

	persona := &object.Persona{
		// Identity is set here from the verified principal and nowhere else,
		// overwriting whatever the body may have carried.
		Owner:  org,
		UserId: userID,

		Name:          strings.TrimSpace(req.Name),
		DisplayName:   req.DisplayName,
		Category:      req.Category,
		Description:   req.Description,
		Philosophy:    req.Philosophy,
		Ocean:         req.Ocean,
		Tools:         req.Tools,
		Contributions: object.StringSlice(req.Contributions),
		Quotes:        object.StringSlice(req.Quotes),
		Prompt:        req.Prompt,
	}
	if err := object.SavePersona(persona); err != nil {
		c.ResponseError(err.Error())
		return
	}
	persona.Source = object.PersonaSourceUser
	c.ResponseOk(persona)
}

// PersonaDelete
// @Title PersonaDelete
// @Tag Persona API
// @Description delete the caller's own persona; a catalogue entry it shadowed becomes visible again
// @Param body body controllers.personaRequest true "name"
// @Success 200 {object} bool The Response object
// @router /persona/delete [post]
func (c *ApiController) PersonaDelete() {
	org, userID, ok := c.requirePersonaIdentity()
	if !ok {
		return
	}
	var req personaRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		c.ResponseError(err.Error())
		return
	}
	deleted, err := object.DeletePersona(org, userID, req.Name)
	if err != nil {
		c.ResponseError(err.Error())
		return
	}
	c.ResponseOk(deleted)
}
