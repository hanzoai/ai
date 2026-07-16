// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package object

import (
	"time"

	"github.com/hanzoai/dbx"
)

// ModelAccess is one access grant/request for a gated model-family SKU — a SKU that
// is LISTED for discovery but callable only with a grant (enso is limited preview).
// One row per (org, user, model). A row with User="" is an ORG-WIDE grant; a row with
// a specific User (username OR email) grants just that user. Status is "requested"
// (waitlisted) or "granted". The storage pattern mirrors ModelRoute/OrgSettings.
type ModelAccess struct {
	Owner       string `db:"pk" json:"owner"` // org id
	User        string `db:"pk" json:"user"`  // username or email within the org; "" = org-wide
	Model       string `db:"pk" json:"model"` // canonical SKU id (e.g. "enso")
	Status      string `json:"status"`        // "requested" | "granted"
	Email       string `json:"email,omitempty"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
}

const (
	ModelAccessRequested = "requested"
	ModelAccessGranted   = "granted"
)

func modelAccessPK(owner, user, model string) dbx.HashExp {
	return dbx.HashExp{"owner": owner, "user": user, "model": model}
}

// GetModelAccess returns the row for (owner,user,model), or nil.
func GetModelAccess(owner, user, model string) (*ModelAccess, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	a := ModelAccess{Owner: owner, User: user, Model: model}
	existed, err := getOne(adapter.db, "model_access", &a, modelAccessPK(owner, user, model))
	if err != nil {
		return nil, err
	}
	if existed {
		return &a, nil
	}
	return nil, nil
}

// accessKeys are the identities a grant can be keyed to for this caller: the org-wide
// "" bucket, the username, and the email — so a grant seeded by email is honored even
// when the request principal is identified by username, and vice versa.
func accessKeys(name, email string) []string {
	keys := []string{""} // org-wide
	if name != "" {
		keys = append(keys, name)
	}
	if email != "" && email != name {
		keys = append(keys, email)
	}
	return keys
}

// IsModelAccessGranted reports whether the caller may use a gated model: an org-wide
// grant, or a grant to the caller's username or email. Fail-safe false on any error —
// a gated model is closed by default.
func IsModelAccessGranted(owner, name, email, model string) bool {
	if owner == "" {
		return false
	}
	for _, u := range accessKeys(name, email) {
		if a, _ := GetModelAccess(owner, u, model); a != nil && a.Status == ModelAccessGranted {
			return true
		}
	}
	return false
}

// ModelAccessStatus returns the caller's status for a model: "granted" if any grant
// applies, else "requested" if a request is pending, else "" (never asked).
func ModelAccessStatus(owner, name, email, model string) string {
	if owner == "" {
		return ""
	}
	pending := ""
	for _, u := range accessKeys(name, email) {
		a, _ := GetModelAccess(owner, u, model)
		if a == nil {
			continue
		}
		if a.Status == ModelAccessGranted {
			return ModelAccessGranted
		}
		pending = a.Status // "requested"
	}
	return pending
}

// UpsertModelAccess creates or updates a row, never DOWNGRADING a granted row to
// requested (a grant is sticky) — so a re-request by an already-granted user is a
// no-op. Idempotent.
func UpsertModelAccess(owner, user, email, model, status string) (*ModelAccess, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	existing, err := GetModelAccess(owner, user, model)
	if err != nil {
		return nil, err
	}
	now := time.Now().Format(time.RFC3339)
	if existing != nil {
		if existing.Status == ModelAccessGranted && status == ModelAccessRequested {
			return existing, nil // never downgrade a grant
		}
		existing.Status = status
		existing.UpdatedTime = now
		if email != "" {
			existing.Email = email
		}
		if err := adapter.db.Model(existing).Update(); err != nil {
			return nil, err
		}
		return existing, nil
	}
	a := &ModelAccess{Owner: owner, User: user, Model: model, Status: status, Email: email, CreatedTime: now, UpdatedTime: now}
	if err := insertRow(adapter.db, a); err != nil {
		return nil, err
	}
	return a, nil
}

// RequestModelAccess upserts a waitlist request for the caller (idempotent).
func RequestModelAccess(owner, user, email, model string) (*ModelAccess, error) {
	return UpsertModelAccess(owner, user, email, model, ModelAccessRequested)
}

// GrantModelAccess upserts a grant (idempotent). user "" grants the whole org.
func GrantModelAccess(owner, user, email, model string) (*ModelAccess, error) {
	return UpsertModelAccess(owner, user, email, model, ModelAccessGranted)
}

// ListModelAccess returns access rows, scoped to an org when owner != "", else all
// (the SuperAdmin view).
func ListModelAccess(owner string) ([]*ModelAccess, error) {
	if adapter == nil || adapter.db == nil {
		return nil, nil
	}
	rows := []*ModelAccess{}
	var where dbx.Expression
	if owner != "" {
		where = dbx.HashExp{"owner": owner}
	}
	if err := findAll(adapter.db, "model_access", &rows, where, "created_time DESC"); err != nil {
		return rows, err
	}
	return rows, nil
}
