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

import "encoding/json"

type emailForm struct {
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Sender    string   `json:"sender"`
	Receivers []string `json:"receivers"`
}

// SendEmail sends an email via IAM's configured mail provider.
//
// IT HAS NO ROUTE. `/v1/iam/send-email` was part of the verb surface IAM
// retired, and mail is not an identity concern, so nothing replaced it under a
// noun — the whole router carries no mail address of any kind. Every call here
// reaches a 404, and it still speaks the {status, data} envelope because there
// is no native shape to speak instead.
//
// Re-homing this is a decision about where transactional mail belongs, not a
// spelling change, so it is deliberately not made here.
func (c *Client) SendEmail(title, content, sender string, receivers ...string) error {
	postBytes, err := json.Marshal(emailForm{
		Title:     title,
		Content:   content,
		Sender:    sender,
		Receivers: receivers,
	})
	if err != nil {
		return err
	}
	_, err = c.DoPost("send-email", nil, postBytes, false, false)
	return err
}

// SendEmail uses the configured (or env-derived) client.
func SendEmail(title, content, sender string, receivers ...string) error {
	return ensureClient().SendEmail(title, content, sender, receivers...)
}
