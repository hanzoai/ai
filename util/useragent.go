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

package util

import (
	"fmt"
	"sync"

	"github.com/ua-parser/uap-go/uaparser"
)

// parser is built on FIRST USE, not on somebody remembering to build it.
//
// It used to be a package variable filled by an InitParser() called from
// bootstrap, which made every caller depend on an ordering it could not see: a
// path that ran before bootstrap got there — or any test — dereferenced nil and
// took a SIGSEGV. Adding a chat reads the user agent, so that is a 500 on
// add-chat with a stack trace instead of a description. A test had already worked
// around it by calling InitParser itself when it found the variable empty, which
// is the ordering problem stated out loud.
//
// Built once, by whoever asks first. The regex set costs a moment to compile and
// the answer never changes after that.
var parser = sync.OnceValue(func() *uaparser.Parser {
	p, err := uaparser.New()
	if err != nil {
		return nil
	}
	return p
})

// GetDescFromUserAgent renders a user agent as "browser | os | device".
//
// An agent it cannot read is not worth a panic on the request that carried it:
// the description is a nicety beside whatever the caller actually asked for, so
// an unbuildable parser yields no description and nothing else changes.
func GetDescFromUserAgent(userAgent string) string {
	p := parser()
	if p == nil {
		return ""
	}
	client := p.Parse(userAgent)
	return fmt.Sprintf("%s | %s | %s", client.UserAgent.ToString(), client.Os.ToString(), client.Device.ToString())
}
