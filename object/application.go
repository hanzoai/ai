// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2025 The OpenAgent Authors. All Rights Reserved.
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
	"fmt"
	"strings"

	"github.com/hanzoai/ai/util"
)

// Application.Status values. The four middle ones are the pod phases a
// deployed application reports back.
const (
	StatusNotDeployed = "Not Deployed"
	StatusPending     = "Pending"
	StatusRunning     = "Running"
	StatusUnknown     = "Unknown"
	StatusFailed      = "Failed"
	StatusTerminating = "Terminating"
	NamespaceFormat   = "hanzo-cloud-%s"
)

type Application struct {
	Owner              string                    `db:"pk" json:"owner"`
	Name               string                    `db:"pk" json:"name"`
	CreatedTime        string                    `json:"createdTime"`
	UpdatedTime        string                    `json:"updatedTime"`
	DisplayName        string                    `json:"displayName"`
	Description        string                    `json:"description"`
	Template           string                    `json:"template"` // Reference to Template.Name
	Parameters         string                    `json:"parameters"`
	Manifest           string                    `json:"manifest"`  // Deployment manifest
	Status             string                    `json:"status"`    // Running, Pending, Failed, Not Deployed
	Namespace          string                    `json:"namespace"` // Kubernetes namespace (auto-generated)
	URL                string                    `json:"url"`       // Available service URL
	Details            *ApplicationView          `db:"-" json:"details,omitempty"`
	BasicConfigOptions []applicationConfigOption `json:"basicConfigOptions"`
}
type applicationConfigOption struct {
	Parameter string `json:"parameter"`
	Setting   string `json:"setting"`
}

func GetApplications(owner string) ([]*Application, error) {
	return rowsOf[Application]("application", owner)
}

func GetApplicationCount(owner, field, value string) (int64, error) {
	return rowCount("application", owner, field, value)
}

func GetPaginationApplications(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Application, error) {
	return rowsPage[Application]("application", owner, offset, limit, field, value, sortField, sortOrder)
}

func getApplication(owner, name string) (*Application, error) {
	return getRow[Application]("application", owner, name)
}

func GetApplication(id string) (*Application, error) {
	return rowAt[Application]("application", id)
}

// UpdateApplication writes the record. The Manifest field is rendered from the
// template by cluster.Manifest, which the caller applies before saving.
func UpdateApplication(id string, application *Application) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	if application == nil {
		return false, nil
	}
	application.UpdatedTime = util.GetCurrentTime()
	// getApplication answers (nil, nil) for a row that is not there, and this read
	// discarded the value — so an update against a name nobody has wrote nothing
	// and reported that it had.
	existing, err := getApplication(owner, name)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	application.Owner = owner
	application.Name = name
	// The namespace is decided here, from the name, and never taken from the
	// request. It is where this application's manifest is applied, and a manifest
	// carries any kind, so which namespace an application uses is this module's
	// answer rather than a field a request fills in. Add generated it and update
	// wrote whatever arrived — the same field answering to two different things.
	application.Namespace = namespaceFor(name)
	err = adapter.db.Model(application).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

// namespaceFor answers which namespace an application's manifest is applied in.
// One application, one namespace, derived from its name — so a caller names an
// application and never a namespace.
func namespaceFor(name string) string {
	return fmt.Sprintf(NamespaceFormat, strings.ReplaceAll(name, "_", "-"))
}

func AddApplication(application *Application) (bool, error) {
	if application.CreatedTime == "" {
		application.CreatedTime = util.GetCurrentTime()
	}
	if application.UpdatedTime == "" {
		application.UpdatedTime = util.GetCurrentTime()
	}
	application.Namespace = namespaceFor(application.Name)
	// Set initial status
	if application.Status == "" {
		application.Status = StatusNotDeployed
	}
	return addRow(application)
}

// DeleteApplication removes the record. Tearing down what the record deployed is
// cluster.Undeploy, which the caller runs first.
func DeleteApplication(application *Application) (bool, error) {
	return deleteRow("application", application.Owner, application.Name)
}
