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
package object
import (
	"fmt"
	"github.com/hanzoai/cloud/util"
	"github.com/hanzoai/dbx"
)
type Block struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	TextEn string `json:"textEn"`
	Prompt string `json:"prompt"`
	State  string `json:"state"`
}
type Article struct {
	Owner       string `db:"pk" json:"owner"`
	Name        string `db:"pk" json:"name"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`
	Workflow    string `json:"workflow"`
	Type        string `json:"type"`
	Text     string   `json:"text"`
	Content  []*Block `json:"content"`
	Glossary []string `json:"glossary"`
}
func GetMaskedArticle(article *Article, isMaskEnabled bool) *Article {
	if !isMaskEnabled {
		return article
	}
	if article == nil {
		return nil
	}
	return article
}
func GetMaskedArticles(articles []*Article, isMaskEnabled bool) []*Article {
	if !isMaskEnabled {
		return articles
	}
	for _, article := range articles {
		article = GetMaskedArticle(article, isMaskEnabled)
		article.Content = nil
	}
	return articles
}
func GetGlobalArticles() ([]*Article, error) {
	articles := []*Article{}
	err := findAll(adapter.db, "article", &articles, nil, "owner ASC", "created_time DESC")
	if err != nil {
		return articles, err
	}
	return articles, nil
}
func GetArticles(owner string) ([]*Article, error) {
	articles := []*Article{}
	err := findAll(adapter.db, "article", &articles, dbx.HashExp{"owner": owner}, "created_time DESC")
	if err != nil {
		return articles, err
	}
	return articles, nil
}
func getArticle(owner string, name string) (*Article, error) {
	article := Article{Owner: owner, Name: name}
	existed, err := getOne(adapter.db, "article", &article, pk2(article.Owner, article.Name))
	if err != nil {
		return &article, err
	}
	if existed {
		return &article, nil
	} else {
		return nil, nil
	}
}
func GetArticle(id string) (*Article, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return nil, err
	}
	return getArticle(owner, name)
}
func GetArticleCount(owner, field, value string) (int64, error) {
	session := GetDbQuery(owner, -1, -1, field, value, "", "")
	return queryCount(session, "article")
}
func GetPaginationArticles(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Article, error) {
	articles := []*Article{}
	session := GetDbQuery(owner, offset, limit, field, value, sortField, sortOrder)
	err := queryFind(session, "article", &articles)
	if err != nil {
		return articles, err
	}
	return articles, nil
}
func UpdateArticle(id string, article *Article) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	_, err = getArticle(owner, name)
	if err != nil {
		return false, err
	}
	if article == nil {
		return false, nil
	}
	article.Owner = owner
	article.Name = name
	err = adapter.db.Model(article).Update()
	if err != nil {
		return false, err
	}
	// return affected != 0
	return true, nil
}
func AddArticle(article *Article) (bool, error) {
	err := insertRow(adapter.db, article)
	affected := int64(1)
	if err != nil {
		affected = 0
	}
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func DeleteArticle(article *Article) (bool, error) {
	affected, err := deleteByPK(adapter.db, "article", pk2(article.Owner, article.Name))
	if err != nil {
		return false, err
	}
	return affected != 0, nil
}
func (article *Article) GetId() string {
	return fmt.Sprintf("%s/%s", article.Owner, article.Name)
}
