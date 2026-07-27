// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2023 The OpenAgent Authors. All Rights Reserved.
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

package video

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/alibabacloud-go/tea/tea"
	vod20170321 "github.com/alibabacloud-go/vod-20170321/v2/client"
	"github.com/hanzoai/ai/util"
)

// errNoVodClient is returned instead of dereferencing a nil VodClient.
//
// VodClient is package-level and stays nil until SetVodClient runs, so on any
// deployment where the Aliyun VOD provider is not configured, every function
// below called straight through nil and panicked. These run inside request
// handlers, so an unconfigured optional provider took the whole process down —
// and the trigger could be as ordinary as a video record with no id.
//
// One check, named once, used by all four entry points.
var errNoVodClient = errors.New("video: VOD client not configured (SetVodClient was never called)")

func GetVideoPlayAuth(videoId string) (string, error) {
	request := &vod20170321.GetVideoPlayAuthRequest{
		VideoId: tea.String(videoId),
	}

	if VodClient == nil {
		return "", errNoVodClient
	}

	resp, err := VodClient.GetVideoPlayAuth(request)
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Body == nil {
		return "", errNoVodClient
	}

	playAuth := tea.StringValue(resp.Body.PlayAuth)
	return playAuth, nil
}

type UploadAddress struct {
	Endpoint string `json:"Endpoint"`
	Bucket   string `json:"Bucket"`
	FileName string `json:"FileName"`
}

type UploadAuth struct {
	SecurityToken   string    `json:"SecurityToken"`
	AccessKeyId     string    `json:"AccessKeyId"`
	ExpireUTCTime   time.Time `json:"ExpireUTCTime"`
	AccessKeySecret string    `json:"AccessKeySecret"`
	Expiration      string    `json:"Expiration"`
	Region          string    `json:"Region"`
}

func UploadVideo(fileId string, filename string, fileBuffer *bytes.Buffer) (string, error) {
	// https://help.aliyun.com/document_detail/476208.html

	request := &vod20170321.CreateUploadVideoRequest{
		FileName: tea.String(filename),
		Title:    tea.String(fileId),
	}
	if VodClient == nil {
		return "", errNoVodClient
	}

	resp, err := VodClient.CreateUploadVideo(request)
	if err != nil {
		return "", nil
	}

	encodedUploadAddress := tea.StringValue(resp.Body.UploadAddress)
	videoId := tea.StringValue(resp.Body.VideoId)
	encodedUploadAuth := tea.StringValue(resp.Body.UploadAuth)

	uploadAddressStr := util.DecodeBase64(encodedUploadAddress)
	uploadAuthStr := util.DecodeBase64(encodedUploadAuth)

	uploadAddress := &UploadAddress{}
	err = util.JsonToStruct(uploadAddressStr, uploadAddress)
	if err != nil {
		return "", nil
	}

	uploadAuth := &UploadAuth{}
	err = util.JsonToStruct(uploadAuthStr, uploadAuth)
	if err != nil {
		return "", nil
	}

	ossClient, err := getOssClient(uploadAddress.Endpoint, uploadAuth.AccessKeyId, uploadAuth.AccessKeySecret, uploadAuth.SecurityToken)
	if err != nil {
		return "", nil
	}

	err = uploadLocalFile(ossClient, uploadAddress.Bucket, uploadAddress.FileName, fileBuffer)
	if err != nil {
		return "", nil
	}

	return videoId, nil
}

func GetVideoCoverUrl(videoId string) string {
	request := &vod20170321.GetVideoInfoRequest{
		VideoId: tea.String(videoId),
	}

	if VodClient == nil {
		return errNoVodClient.Error()
	}

	resp, err := VodClient.GetVideoInfo(request)
	if err != nil {
		fmt.Println(err)
		return err.Error()
	}
	// resp.Body.Video is three dereferences on a response we do not control;
	// a nil anywhere in that chain is a panic, not a missing cover image.
	if resp == nil || resp.Body == nil || resp.Body.Video == nil {
		return ""
	}

	return tea.StringValue(resp.Body.Video.CoverURL)
}

func GetVideoFileUrl(videoId string) string {
	request := &vod20170321.GetMezzanineInfoRequest{
		VideoId: tea.String(videoId),
	}

	if VodClient == nil {
		return errNoVodClient.Error()
	}

	resp, err := VodClient.GetMezzanineInfo(request)
	if err != nil {
		fmt.Println(err)
		return err.Error()
	}
	if resp == nil || resp.Body == nil || resp.Body.Mezzanine == nil {
		return ""
	}

	downloadUrl := tea.StringValue(resp.Body.Mezzanine.FileURL)
	if downloadUrl == "" {
		fmt.Println(err)
		return err.Error()
	}

	return downloadUrl
}
