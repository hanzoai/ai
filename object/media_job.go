// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

	"github.com/hanzoai/dbx"
)

// MediaJob is one generation a vendor runs for a caller (a video clip): who asked
// and pays, what was asked, the vendor's job, and what came of it. It is kept in
// the store so a job outlives the process that started it: a process that starts
// finishes, bills and answers every job one before it left unfinished.
type MediaJob struct {
	Owner string `db:"pk" json:"owner"` // the org whose ledger pays
	Name  string `db:"pk" json:"name"`  // the public id, video_<uuid>

	CreatedTime  string `json:"createdTime"`
	FinishedTime string `json:"finishedTime"`

	Model    string `json:"model"`    // the SKU asked for
	Vendor   string `json:"vendor"`   // the family whose vendor runs it ("openrouter")
	Upstream string `json:"upstream"` // the vendor's job id
	Account  string `json:"account"`  // the NAME of the vendor key the job runs on

	Subject        string `json:"subject"`        // the billing subject: who owns the job
	Organization   string `json:"organization"`   // the caller's home org
	User           string `json:"user"`           // the caller's name
	UserType       string `json:"userType"`       // the caller's IAM type
	BillingAccount string `json:"billingAccount"` // the caller's signed billing_account claim

	Status string `json:"status"` // queued | in_progress | completed | failed
	Error  string `json:"error"`

	Seconds    int    `json:"seconds"`
	Resolution string `json:"resolution"`
	Size       string `json:"size"`
	Aspect     string `json:"aspect"`
	Audio      bool   `json:"audio"`

	Quote   string `json:"quote"`   // USD reserved: the clip's ceiling, as billed
	Cost    string `json:"cost"`    // USD the vendor said it cost
	Billed  string `json:"billed"`  // USD billed
	Settled bool   `json:"settled"` // finished and billed (or released): nothing is left to do

	Asset    string  `json:"asset"` // the object's key in the host's store
	Bytes    int64   `json:"bytes"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Duration float64 `json:"duration"` // seconds, read from the file
}

const mediaJobTable = "media_job"

// AddMediaJob files a new job.
func AddMediaJob(j *MediaJob) error {
	if adapter == nil || adapter.db == nil {
		return errNoStore
	}
	return insertRow(adapter.db, j)
}

// UpdateMediaJob writes a job as it stands.
func UpdateMediaJob(j *MediaJob) error {
	if adapter == nil || adapter.db == nil {
		return errNoStore
	}
	n, err := updateByPK(adapter.db, mediaJobTable, dbx.HashExp{"owner": j.Owner, "name": j.Name}, dbx.Params{
		"finished_time": j.FinishedTime, "upstream": j.Upstream, "account": j.Account,
		"status": j.Status, "error": j.Error, "cost": j.Cost, "billed": j.Billed, "settled": j.Settled,
		"asset": j.Asset, "bytes": j.Bytes, "width": j.Width, "height": j.Height, "duration": j.Duration,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("media job %s/%s: %d rows updated", j.Owner, j.Name, n)
	}
	return nil
}

// MediaJobNamed is the job with public id name, nil when there is none.
func MediaJobNamed(name string) (*MediaJob, error) {
	if adapter == nil || adapter.db == nil {
		return nil, errNoStore
	}
	rows, err := rowsWhere[MediaJob](mediaJobTable, dbx.HashExp{"name": name})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// UnsettledMediaJobs are the jobs no process has finished and billed.
func UnsettledMediaJobs() ([]*MediaJob, error) {
	if adapter == nil || adapter.db == nil {
		return nil, errNoStore
	}
	return rowsWhere[MediaJob](mediaJobTable, dbx.HashExp{"settled": false})
}
