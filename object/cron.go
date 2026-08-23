// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

import "github.com/robfig/cron/v3"

// newCron builds a scheduler whose jobs cannot end the process.
//
// The library runs each job in a goroutine of its own — startJob is `go j.Run()`
// — and installs no wrapper unless asked, so a panic in a job is a panic in a
// goroutine: nothing recovers it and the process exits. These jobs run every
// second, every five minutes and every hour against live rows, so anything a
// handler can trip over they can trip over too, only with no request to fail.
//
// Recover is the library's own answer and it is simply not on by default. This is
// the one place a scheduler is made here, so it is on for all of them.
func newCron() *cron.Cron {
	return cron.New(cron.WithChain(cron.Recover(cron.DefaultLogger)))
}
