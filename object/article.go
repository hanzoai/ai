// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
// Portions Copyright 2024 The OpenAgent Authors. All Rights Reserved.
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

	"github.com/hanzoai/ai/util"
)

type Block struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	TextEn string `json:"textEn"`
	Prompt string `json:"prompt"`
	State  string `json:"state"`
}
type Article struct {
	Owner       string     `db:"pk" json:"owner"`
	Name        string     `db:"pk" json:"name"`
	CreatedTime string     `json:"createdTime"`
	DisplayName string     `json:"displayName"`
	Workflow    string     `json:"workflow"`
	Type        string     `json:"type"`
	Text        string     `json:"text"`
	Content     []*Block   `json:"content"`
	Glossary    StringList `json:"glossary"`
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
	return allRows[Article]("article")
}

func GetArticles(owner string) ([]*Article, error) {
	return rowsOf[Article]("article", owner)
}

func getArticle(owner, name string) (*Article, error) {
	return getRow[Article]("article", owner, name)
}

func GetArticle(id string) (*Article, error) {
	return rowAt[Article]("article", id)
}

func GetArticleCount(owner, field, value string) (int64, error) {
	return rowCount("article", owner, field, value)
}

func GetPaginationArticles(owner string, offset, limit int, field, value, sortField, sortOrder string) ([]*Article, error) {
	return rowsPage[Article]("article", owner, offset, limit, field, value, sortField, sortOrder)
}

func UpdateArticle(id string, article *Article) (bool, error) {
	owner, name, err := util.GetOwnerAndNameFromIdWithError(id)
	if err != nil {
		return false, err
	}
	if article == nil {
		return false, nil
	}
	article.Owner, article.Name = owner, name
	return updated(article)
}

func AddArticle(article *Article) (bool, error) {
	return addRow(article)
}

func DeleteArticle(article *Article) (bool, error) {
	return deleteRow("article", article.Owner, article.Name)
}

func (article *Article) GetId() string {
	return fmt.Sprintf("%s/%s", article.Owner, article.Name)
}
