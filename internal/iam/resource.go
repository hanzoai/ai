// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

package iam

import (
	"encoding/json"
	"errors"
)

// Resource mirrors the IAM Resource JSON model (a stored file record). ai
// constructs one client-side to delete by tag, and reads Name/CreatedTime/
// FileSize/Url off the list response, so the full field set is kept.
//
// NOTE: these resource calls proxy file storage through IAM's /v1/iam/
// upload/list/delete endpoints — the same behavior ai had via the old SDK.
// Re-homing file storage to S3 directly is a possible future cleanup, but is
// out of scope for decoupling from the retired iam-v1 module: keeping the REST
// calls preserves behavior exactly.
type Resource struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	CreatedTime string `json:"createdTime"`
	User        string `json:"user"`
	Provider    string `json:"provider"`
	Application string `json:"application"`
	Tag         string `json:"tag"`
	Parent      string `json:"parent"`
	FileName    string `json:"fileName"`
	FileType    string `json:"fileType"`
	FileFormat  string `json:"fileFormat"`
	FileSize    int    `json:"fileSize"`
	Url         string `json:"url"`
	Description string `json:"description"`
}

// UploadResource uploads a file to IAM storage and returns (fileUrl, name).
func (c *Client) UploadResource(user, tag, parent, fullFilePath string, fileBytes []byte) (string, string, error) {
	queryMap := map[string]string{
		"owner":        c.OrganizationName,
		"user":         user,
		"application":  c.ApplicationName,
		"tag":          tag,
		"parent":       parent,
		"fullFilePath": fullFilePath,
	}
	resp, err := c.DoPost("upload-resource", queryMap, fileBytes, true, true)
	if err != nil {
		return "", "", err
	}
	fileUrl, ok := resp.Data.(string)
	if !ok {
		return "", "", errors.New("iam: upload-resource: unexpected data field")
	}
	name, _ := resp.Data2.(string)
	return fileUrl, name, nil
}

// GetResources lists stored resources matching the given filter.
func (c *Client) GetResources(owner, user, field, value, sortField, sortOrder string) ([]*Resource, error) {
	url := c.GetUrl("get-resources", map[string]string{
		"owner":     owner,
		"user":      user,
		"field":     field,
		"value":     value,
		"sortField": sortField,
		"sortOrder": sortOrder,
	})
	bytes, err := c.DoGetBytes(url)
	if err != nil {
		return nil, err
	}
	var resources []*Resource
	if err = json.Unmarshal(bytes, &resources); err != nil {
		return nil, err
	}
	return resources, nil
}

// DeleteResourceWithTag deletes a resource, defaulting its owner to the client's
// organization.
func (c *Client) DeleteResourceWithTag(resource *Resource, tag string) (bool, error) {
	if resource.Owner == "" {
		resource.Owner = c.OrganizationName
	}
	postBytes, err := json.Marshal(resource)
	if err != nil {
		return false, err
	}
	resp, err := c.DoPost("delete-resource", map[string]string{"tag": tag}, postBytes, false, false)
	if err != nil {
		return false, err
	}
	return resp.Data == "Affected", nil
}

// Package-level helpers.

func UploadResource(user, tag, parent, fullFilePath string, fileBytes []byte) (string, string, error) {
	return ensureClient().UploadResource(user, tag, parent, fullFilePath, fileBytes)
}

func GetResources(owner, user, field, value, sortField, sortOrder string) ([]*Resource, error) {
	return ensureClient().GetResources(owner, user, field, value, sortField, sortOrder)
}

func DeleteResourceWithTag(resource *Resource, tag string) (bool, error) {
	return ensureClient().DeleteResourceWithTag(resource, tag)
}
