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

package storage

import (
	"bytes"
	"fmt"

	iamsdk "github.com/hanzoid/go-sdk/casdoorsdk"
	"github.com/hanzoai/cloud/conf"
	"github.com/hanzoai/cloud/i18n"
)

type IamProvider struct {
	providerName string
}

func NewIamProvider(providerName string, lang string) (*IamProvider, error) {
	if providerName == "" {
		return nil, fmt.Errorf(i18n.Translate(lang, "storage:storage provider name: [%s] doesn't exist"), providerName)
	}

	return &IamProvider{providerName: providerName}, nil
}

func (p *IamProvider) ListObjects(prefix string) ([]*Object, error) {
	iamOrganization := conf.GetConfigString("iamOrganization")
	iamApplication := conf.GetConfigString("iamApplication")
	resources, err := iamsdk.GetResources(iamOrganization, iamApplication, "provider", p.providerName, "Direct", prefix)
	if err != nil {
		return nil, err
	}

	res := []*Object{}
	for _, resource := range resources {
		res = append(res, &Object{
			Key:          resource.Name,
			LastModified: resource.CreatedTime,
			Size:         int64(resource.FileSize),
			Url:          resource.Url,
		})
	}
	return res, nil
}

func (p *IamProvider) PutObject(user string, parent string, key string, fileBuffer *bytes.Buffer) (string, error) {
	fileUrl, _, err := iamsdk.UploadResource(user, "HanzoCloud", parent, fmt.Sprintf("Direct/%s/%s", p.providerName, key), fileBuffer.Bytes())
	if err != nil {
		return "", err
	}
	return fileUrl, nil
}

func (p *IamProvider) DeleteObject(key string) error {
	resource := iamsdk.Resource{
		Name: key,
	}

	_, err := iamsdk.DeleteResourceWithTag(&resource, "Direct")
	if err != nil {
		return err
	}
	return nil
}
