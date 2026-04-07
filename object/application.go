// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
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

	"github.com/hanzoai/cloud/i18n"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
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
	applications := []*Application{}
	err := findAll(adapter.db, "application", &applications, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return applications, err
	}
	return applications, nil
}

func GetApplicationCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "application")
}

func GetPaginationApplications(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Application, error) {
	applications := []*Application{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "application", &applications)
	if err != nil {
		return applications, err
	}
	return applications, nil
}

func getApplication(owner, name string) (*Application, error) {
	application := Application{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "application", &application, pk2(application.Owner, application.Name))
	if err != nil {
		return &application, err
	}
	if existed {
		return &application, nil
	} else {
		return nil, nil
	}
}

func GetApplication(id string) (*Application, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getApplication(owner, name)
}

func UpdateApplication(id string, application *Application, lang string) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	application.UpdatedTime = util.GetCurrentTime()
	_, err = getApplication(owner, name)
	if err != nil {
		return false, err
	}
	if application == nil {
		return false, nil
	}
	template, err := getTemplate(application.Owner, application.Template)
	if err != nil {
		return false, err
	}
	if template.EnableBasicConfig {
		// Initialize the manifest using the basic configuration options
		application.Manifest, err = application.generateManifestWithBasicConfig(template)
		if err != nil {
			return false, err
		}
	} else {
		// Initialize the manifest using the template
		application.Manifest = template.Manifest
	}
	// Apply Kustomize overlays
	application.Manifest, err = generateManifestWithKustomize(application.Manifest, application.Parameters, lang)
	if err != nil {
		return false, fmt.Errorf("%s", fmt.Sprintf(i18n.Translate(lang, "object:failed to generate manifest: %v"), err))
	}
	application.Owner = owner
	application.Name = name
	err = adapter.db.Model(application).Update()
	if err != nil {
		return false, err
	}
	return true, nil
}

func AddApplication(application *Application) (bool, error) {
	if application.CreatedTime == "" {
		application.CreatedTime = util.GetCurrentTime()
	}
	if application.UpdatedTime == "" {
		application.UpdatedTime = util.GetCurrentTime()
	}
	// Generate namespace name based on application owner and name
	application.Namespace = fmt.Sprintf(NamespaceFormat, strings.ReplaceAll(application.Name, "_", "-"))
	// Set initial status
	if application.Status == "" {
		application.Status = StatusNotDeployed
	}
	err := insertRow(adapter.db, application)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteApplication(application *Application, lang string) (bool, error) {
	owner, name, namespace := application.Owner, application.Name, application.Namespace
	// First, delete the deployment if it exists
	go func() {
		_, err := UndeployApplication(owner, name, namespace, lang)
		if err != nil {
			return
		}
	}()
	// Then delete the application record
	affected, err := deleteByPK(adapter.db, "application", pk2(owner, name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

// generateManifestWithBasicConfig generates the manifest from the basic configuration options using the provided template.
func (a *Application) generateManifestWithBasicConfig(template *Template) (string, error) {
	app := map[string]interface{}{
		"name":      toK8sMetadataName(a.Name),
		"namespace": a.Namespace,
	}
	options := make(map[string]interface{}, len(a.BasicConfigOptions))
	for _, option := range a.BasicConfigOptions {
		options[option.Parameter] = option.Setting
	}
	data := map[string]interface{}{
		"application": app,
		"options":     options,
	}
	return template.Render(data)
}
