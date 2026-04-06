// Copyright 2024 The casbin Authors. All Rights Reserved.
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
package object
import (
	"fmt"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)
type Image struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`
	Provider string `json:"provider"`
	Category string `json:"category"`
	BootMode                string `json:"bootMode" xml:"bootMode"`
	ImageId                 string `json:"imageId" xml:"imageId"`
	ImageOwnerAlias         string `json:"ImageOwnerAlias" xml:"ImageOwnerAlias"`
	OSName                  string `json:"os" xml:"os"`
	OSNameEn                string `json:"OSNameEn" xml:"OSNameEn"`
	ImageFamily             string `json:"ImageFamily" xml:"ImageFamily"`
	Architecture            string `json:"systemArchitecture" xml:"systemArchitecture"`
	IsSupportIoOptimized    bool   `json:"IsSupportIoOptimized" xml:"IsSupportIoOptimized"`
	Size                    string `json:"size" xml:"size"`
	ResourceGroupId         string `json:"ResourceGroupId" xml:"ResourceGroupId"`
	SupplierName            string `json:"SupplierName" xml:"SupplierName"`
	Description             string `json:"description" xml:"description"`
	Usage                   string `json:"Usage" xml:"Usage"`
	IsCopied                bool   `json:"IsCopied" xml:"IsCopied"`
	LoginAsNonRootSupported bool   `json:"LoginAsNonRootSupported" xml:"LoginAsNonRootSupported"`
	ImageVersion            string `json:"ImageVersion" xml:"ImageVersion"`
	OSType                  string `json:"OSType" xml:"OSType"`
	IsSubscribed            bool   `json:"IsSubscribed" xml:"IsSubscribed"`
	IsSupportCloudinit      bool   `json:"IsSupportCloudinit" xml:"IsSupportCloudinit"`
	CreationTime            string `json:"creationTime" xml:"creationTime"`
	ProductCode             string `json:"ProductCode" xml:"ProductCode"`
	Progress                string `json:"progress" xml:"progress"`
	Platform                string `json:"platform" xml:"platform"`
	IsSelfShared            string `json:"IsSelfShared" xml:"IsSelfShared"`
	ImageName               string `json:"ImageName" xml:"ImageName"`
	Status                  string `json:"state" xml:"state"`
	ImageOwnerId            int64  `json:"ImageOwnerId" xml:"ImageOwnerId"`
	IsPublic                bool   `json:"IsPublic" xml:"IsPublic"`
	// DetectionOptions        DetectionOptions                            `json:"DetectionOptions" xml:"DetectionOptions"`
	// Features                Features                                    `json:"Features" xml:"Features"`
	// Tags                    TagsInDescribeImageFromFamily               `json:"Tags" xml:"Tags"`
	// DiskDeviceMappings      DiskDeviceMappingsInDescribeImageFromFamily `json:"DiskDeviceMappings" xml:"DiskDeviceMappings"`
	// DB info
	RemoteProtocol string `json:"remoteProtocol"`
	RemotePort     int    `json:"remotePort"`
	RemoteUsername string `json:"remoteUsername"`
	RemotePassword string `json:"remotePassword"`
}
func GetImageCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "image")
}
func GetImages(owner string) ([]*Image, error) {
	images := []*Image{}
	err := findAll(adapter.db, "image", &images, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return images, err
	}
	return images, nil
}
func GetPaginationImages(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Image, error) {
	images := []*Image{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "image", &images)
	if err != nil {
		return images, err
	}
	return images, nil
}
func getImage(owner string, name string) (*Image, error) {
	if owner == "" || name == "" {
		return nil, nil
	}
	image := Image{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "image", &image, pk2(image.Owner, image.Name))
	if err != nil {
		return &image, err
	}
	if existed {
		return &image, nil
	} else {
		return nil, nil
	}
}
func GetImage(id string) (*Image, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getImage(owner, name)
}
func GetMaskedImage(image *Image, errs ...error) (*Image, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	if image == nil {
		return nil, nil
	}
	//if image.ImageId != "" {
	//	image.ImageId = "***"
	//}
	return image, nil
}
func GetMaskedImages(images []*Image, errs ...error) ([]*Image, error) {
	if len(errs) > 0 && errs[0] != nil {
		return nil, errs[0]
	}
	var err error
	for _, image := range images {
		image, err = GetMaskedImage(image)
		if err != nil {
			return nil, err
		}
	}
	return images, nil
}
func UpdateImage(id string, image *Image) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	p, err := getImage(owner, name)
	if err != nil {
		return false, err
	} else if p == nil {
		return false, nil
	}
	//if image.RemotePassword == "***" {
	//	image.RemotePassword = p.RemotePassword
	//}
	//
	//_, err = updateImageCloud(p, image)
	//if err != nil {
	//	return false, err
	//}
	image.Owner = owner
	image.Name = name
	err = adapter.db.Model(image).Update()
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func AddImage(image *Image) (bool, error) {
	err := insertRow(adapter.db, image)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func addImages(images []*Image) (bool, error) {
	err := insertRow(adapter.db, images)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteImage(image *Image) (bool, error) {
	affected, err := deleteByPK(adapter.db, "image", pk2(image.Owner, image.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func deleteImages(owner string) (bool, error) {
	affected, err := deleteWhere(adapter.db, "image", dbx.HashExp{"owner": owner})
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func (image *Image) GetId() string {
	return fmt.Sprintf("%s/%s", image.Owner, image.Name)
}
