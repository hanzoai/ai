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
	"time"

	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/cloud/video"
	"github.com/hanzoai/dbx"
)

type Label struct {
	Id        string  `json:"id"`
	User      string  `json:"user"`
	Type      string  `json:"type"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
	Text      string  `json:"text"`
	Speaker   string  `json:"speaker"`
	Tag1      string  `json:"tag1"`
	Tag2      string  `json:"tag2"`
	Tag3      string  `json:"tag3"`
}
type Remark struct {
	Timestamp string `json:"timestamp"`
	User      string `json:"user"`
	Score     string `json:"score"`
	Text      string `json:"text"`
	IsPublic  bool   `json:"isPublic"`
}
type Video struct {
	Owner          string         `db:"pk" json:"owner"`
	Name           string         `db:"pk" json:"name"`
	CreatedTime    string         `json:"createdTime"`
	DisplayName    string         `json:"displayName"`
	Description    string         `json:"description"`
	Tag            string         `json:"tag"`
	Type           string         `json:"type"`
	VideoId        string         `json:"videoId"`
	VideoLength    string         `json:"videoLength"`
	CoverUrl       string         `json:"coverUrl"`
	DownloadUrl    string         `json:"downloadUrl"`
	AudioUrl       string         `json:"audioUrl"`
	EditMode       string         `json:"editMode"`
	Labels         []*Label       `json:"labels"`
	Segments       []*Label       `json:"segments"`
	LabelCount     int            `db:"-" json:"labelCount"`
	SegmentCount   int            `db:"-" json:"segmentCount"`
	WordCountMap   map[string]int `json:"wordCountMap"`
	DataUrls       []string       `json:"dataUrls"`
	DataUrl        string         `json:"dataUrl"`
	TagOnPause     bool           `json:"tagOnPause"`
	Remarks        []*Remark      `json:"remarks"`
	Remarks2       []*Remark      `json:"remarks2"`
	ExcellentCount int            `json:"excellentCount"`
	State          string         `json:"state"`
	ReviewState    string         `json:"reviewState"`
	IsPublic       bool           `json:"isPublic"`
	School         string         `json:"school"`
	Stage          string         `json:"stage"`
	Grade          string         `json:"grade"`
	Unit           string         `json:"unit"`
	Lesson         string         `json:"lesson"`
	Class          string         `json:"class"`
	Subject        string         `json:"subject"`
	Topic          string         `json:"topic"`
	Grade2         string         `json:"grade2"`
	Keywords       []string       `json:"keywords"`
	Template       string         `json:"template"`
	Task1          string         `json:"task1"`
	Task2          string         `json:"task2"`
	Task3          string         `json:"task3"`
	PlayAuth       string         `db:"-" json:"playAuth"`
}

func GetGlobalVideos() ([]*Video, error) {
	videos := []*Video{}
	err := findAll(adapter.db, "video", &videos, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return videos, err
	}
	return videos, nil
}

func GetVideos(owner string, lang string) ([]*Video, error) {
	videos := []*Video{}
	err := findAll(adapter.db, "video", &videos, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return videos, err
	}
	for _, v := range videos {
		err = v.refineVideoAndCoverUrl(lang)
		if err != nil {
			return videos, err
		}
	}
	return videos, nil
}

func getVideo(owner string, name string) (*Video, error) {
	v := Video{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "video", &v, pk2(v.Owner, v.Name))
	if err != nil {
		return &v, err
	}
	if existed {
		return &v, nil
	} else {
		return nil, nil
	}
}

func GetVideo(id string, lang string) (*Video, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	v, err := getVideo(owner, name)
	if err != nil {
		return nil, err
	}
	if v != nil && v.VideoId != "" {
		err = SetDefaultVodClient(lang)
		if err != nil {
			return nil, err
		}
		maxRetries := 30
		for i := 0; i < maxRetries; i++ {
			v.PlayAuth, err = video.GetVideoPlayAuth(v.VideoId)
			if err == nil {
				return v, nil
			}
			if !strings.Contains(err.Error(), "and AuditStatus is Init.") {
				return nil, err
			}
			fmt.Printf("GetVideoPlayAuth() error, video: %s, try time: %d, error: %v\n", name, i, err)
			time.Sleep(2 * time.Second)
		}
	}
	return v, nil
}

func UpdateVideo(id string, video *Video) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	_, err = getVideo(owner, name)
	if err != nil {
		return false, err
	}
	if video == nil {
		return false, nil
	}
	video.Owner = owner
	video.Name = name
	err = adapter.db.Model(video).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}

func AddVideo(video *Video) (bool, error) {
	err := insertRow(adapter.db, video)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func DeleteVideo(video *Video) (bool, error) {
	affected, err := deleteByPK(adapter.db, "video", pk2(video.Owner, video.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}

func (video *Video) GetId() string {
	return fmt.Sprintf("%s/%s", video.Owner, video.Name)
}

func (video *Video) Populate(lang string) error {
	// store, err := GetDefaultStore("admin")
	// if err != nil {
	//	return err
	// }
	// if store == nil {
	//	return nil
	// }
	//
	// dataUrls, err := store.GetVideoData()
	// if err != nil {
	//	return err
	// }
	// video.DataUrls = dataUrls
	err := video.PopulateWordCountMap(lang)
	if err != nil {
		return err
	}
	if video.EditMode == "" {
		if len(video.Segments) == 0 {
			video.EditMode = "Labeling"
		} else {
			video.EditMode = "Text Recognition"
		}
	}
	return nil
}

func (v *Video) refineVideoAndCoverUrl(lang string) error {
	excellentCount := 0
	for _, remark := range v.Remarks {
		if remark.Score == "Excellent" {
			excellentCount++
		}
	}
	v.ExcellentCount = excellentCount
	if v.VideoId == "" || (v.CoverUrl != "" && v.DownloadUrl != "") {
		return nil
	}
	err := SetDefaultVodClient(lang)
	if err != nil {
		return err
	}
	coverUrl := video.GetVideoCoverUrl(v.VideoId)
	v.CoverUrl = coverUrl
	downloadUrl := video.GetVideoFileUrl(v.VideoId)
	v.DownloadUrl = downloadUrl
	_, err = UpdateVideo(v.GetId(), v)
	if err != nil {
		return err
	}
	return nil
}

func GetVideoCount(owner string, field string, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "video")
}

func GetPaginationVideos(owner string, offset int, limit int, field string, value string, sortField string, sortOrder string, lang string) ([]*Video, error) {
	videos := []*Video{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "video", &videos)
	if err != nil {
		return videos, err
	}
	for _, v := range videos {
		err = v.refineVideoAndCoverUrl(lang)
		if err != nil {
			return videos, err
		}
	}
	return videos, nil
}
