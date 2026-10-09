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
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/hanzoai/dbx"
)

// Listing is one model a vendor lists, as the last sync read it (controllers'
// listing.go). Item is the vendor's own entry, verbatim, so every field it states —
// each price, the context, the output bound, the modalities, the parameters, when it
// was created and when it expires — is kept exactly as stated, including fields
// added after this was written. A model the vendor stops listing keeps its row with
// Gone set, and is served no longer.
type Listing struct {
	Vendor string `db:"pk" json:"vendor"` // "openrouter"
	ID     string `db:"pk" json:"id"`     // the vendor's id
	Item   string `json:"item"`           // the vendor's entry, JSON as listed
	// Pricing is the entry's pricing in canonical form (keys sorted, each number
	// written as its exact decimal), so a change of price is a change of this string
	// and a change of how the vendor spells a number is not.
	Pricing string `json:"pricing"`
	Free    bool   `json:"free"`
	Seen    string `json:"seen"`   // when a sync first listed it (StampTime)
	Synced  string `json:"synced"` // the last sync that listed it
	Gone    string `json:"gone"`   // when a sync stopped listing it; "" while listed
}

// The kinds of change a sync reads in a listing. A change of price that crosses zero
// is named for the side it lands on, because free and paid are what callers and the
// free lane act on.
const (
	ListingNew   = "new"   // first listed
	ListingBack  = "back"  // listed again after it was gone
	ListingGone  = "gone"  // no longer listed
	ListingPrice = "price" // priced differently, on the same side of zero
	ListingFree  = "free"  // was priced, now free
	ListingPaid  = "paid"  // was free, now priced
)

// ListingEvent is one change a sync read in a vendor's listing: the history of a
// model's prices and of its presence, and what the bus is told.
type ListingEvent struct {
	Vendor string `db:"pk" json:"vendor"`
	ID     string `db:"pk" json:"id"`
	Time   string `db:"pk" json:"time"` // the sync's time (StampTime)
	Kind   string `json:"kind"`         // one of the Listing* kinds
	Was    string `json:"was"`          // the pricing before (Listing.Pricing); "" when new
	Now    string `json:"now"`          // the pricing after; "" when gone
}

// Listings is every row of vendor's listing, gone ones included.
func Listings(vendor string) ([]Listing, error) {
	if adapter == nil || adapter.db == nil {
		return nil, errNoStore
	}
	var rows []Listing
	err := adapter.db.Select().From("listing").Where(dbx.HashExp{"vendor": vendor}).OrderBy("id").All(&rows)
	return rows, err
}

// AddListing inserts a row a sync read for the first time.
func AddListing(l *Listing) error {
	if adapter == nil || adapter.db == nil {
		return errNoStore
	}
	return adapter.db.Model(l).Insert()
}

// UpdateListing writes a row a sync read again.
func UpdateListing(l *Listing) error {
	if adapter == nil || adapter.db == nil {
		return errNoStore
	}
	return adapter.db.Model(l).Update()
}

// AddListingEvents records what a sync read.
func AddListingEvents(evs []ListingEvent) error {
	if adapter == nil || adapter.db == nil {
		return errNoStore
	}
	for i := range evs {
		if err := adapter.db.Model(&evs[i]).Insert(); err != nil {
			return err
		}
	}
	return nil
}

// ListingEvents is vendor's history, newest first: every model's, or id's alone when
// id is set, at most limit rows.
func ListingEvents(vendor, id string, limit int) ([]ListingEvent, error) {
	if adapter == nil || adapter.db == nil {
		return nil, errNoStore
	}
	where := dbx.HashExp{"vendor": vendor}
	if id != "" {
		where["id"] = id
	}
	var rows []ListingEvent
	err := adapter.db.Select().From("listing_event").Where(where).OrderBy("time DESC", "id").Limit(int64(limit)).All(&rows)
	return rows, err
}

var errNoStore = errors.New("no store")

// listingPublisher puts a sync's changes on the host's bus; nil publishes nothing.
var listingPublisher atomic.Pointer[func(context.Context, []ListingEvent)]

// SetListingPublisher installs what puts a sync's changes on the host's bus (nil
// clears it). It is called once a sync's changes are recorded, never before.
func SetListingPublisher(f func(context.Context, []ListingEvent)) {
	if f == nil {
		listingPublisher.Store(nil)
		return
	}
	listingPublisher.Store(&f)
}

// PublishListing hands a sync's changes to the host's bus.
func PublishListing(ctx context.Context, evs []ListingEvent) {
	if f := listingPublisher.Load(); f != nil && len(evs) > 0 {
		(*f)(ctx, evs)
	}
}

// Lease is a job the fleet runs once an interval, whichever process runs it: the
// time it was last taken.
type Lease struct {
	Name string `db:"pk" json:"name"`
	Time string `json:"time"` // StampTime; "" never taken
}

// StampTime writes a time as RFC 3339 at a fixed width, so that times compare as
// their strings do: a listing's times and a lease's.
const StampTime = "2006-01-02T15:04:05.000000000Z"

// TakeLease takes name for this process when it was last taken before now-every, and
// reports whether it did; of processes asking at once, one takes it. A job that
// fails gives it back (Release) so the next asks again.
func TakeLease(name string, every time.Duration, now time.Time) (taken bool, at string, err error) {
	if adapter == nil || adapter.db == nil {
		return false, "", errNoStore
	}
	insert := "INSERT INTO {{lease}} ([[name]], [[time]]) VALUES ({:name}, '') ON CONFLICT ([[name]]) DO NOTHING"
	if adapter.driverName == "mysql" {
		insert = "INSERT IGNORE INTO {{lease}} ([[name]], [[time]]) VALUES ({:name}, '')"
	}
	if _, err := adapter.db.NewQuery(insert).Bind(dbx.Params{"name": name}).Execute(); err != nil {
		return false, "", err
	}
	at = now.UTC().Format(StampTime)
	cut := now.Add(-every).UTC().Format(StampTime)
	res, err := adapter.db.NewQuery("UPDATE {{lease}} SET [[time]] = {:at} WHERE [[name]] = {:name} AND [[time]] < {:cut}").
		Bind(dbx.Params{"name": name, "at": at, "cut": cut}).Execute()
	if err != nil {
		return false, "", err
	}
	n, err := res.RowsAffected()
	return n == 1, at, err
}

// ReleaseLease gives back a lease taken at at, so the next process to ask takes it.
func ReleaseLease(name, at string) error {
	if adapter == nil || adapter.db == nil {
		return errNoStore
	}
	_, err := adapter.db.NewQuery("UPDATE {{lease}} SET [[time]] = '' WHERE [[name]] = {:name} AND [[time]] = {:at}").
		Bind(dbx.Params{"name": name, "at": at}).Execute()
	return err
}

// Served is what one vendor account did with one model in one UTC hour: the requests
// it answered and the ones it failed. The free view reads the last day of it.
type Served struct {
	Vendor   string `db:"pk" json:"vendor"`
	Model    string `db:"pk" json:"model"`
	Account  string `db:"pk" json:"account"` // the account's name (OPENROUTER_API_KEY_3), never its key; "" when unstated
	Hour     string `db:"pk" json:"hour"`    // 2006-01-02T15, UTC
	Answered int64  `json:"answered"`
	Failed   int64  `json:"failed"`
}

const servedHour = "2006-01-02T15"

// CountServed counts one request account made for model at at, in one statement:
// the hour's row is inserted, or its count raised, whoever else writes it.
func CountServed(vendor, model, account string, at time.Time, answered bool) error {
	if adapter == nil || adapter.db == nil {
		return nil
	}
	a, f := int64(0), int64(1)
	if answered {
		a, f = 1, 0
	}
	upsert := "INSERT INTO {{served}} ([[vendor]], [[model]], [[account]], [[hour]], [[answered]], [[failed]]) VALUES ({:v}, {:m}, {:k}, {:h}, {:a}, {:f}) " +
		"ON CONFLICT ([[vendor]], [[model]], [[account]], [[hour]]) DO UPDATE SET [[answered]] = {{served}}.[[answered]] + excluded.[[answered]], [[failed]] = {{served}}.[[failed]] + excluded.[[failed]]"
	if adapter.driverName == "mysql" {
		upsert = "INSERT INTO {{served}} ([[vendor]], [[model]], [[account]], [[hour]], [[answered]], [[failed]]) VALUES ({:v}, {:m}, {:k}, {:h}, {:a}, {:f}) " +
			"ON DUPLICATE KEY UPDATE [[answered]] = [[answered]] + VALUES([[answered]]), [[failed]] = [[failed]] + VALUES([[failed]])"
	}
	_, err := adapter.db.NewQuery(upsert).Bind(dbx.Params{
		"v": vendor, "m": model, "k": account, "h": at.UTC().Format(servedHour), "a": a, "f": f,
	}).Execute()
	return err
}

// ServedSince is every hour of vendor's counts from since's hour on.
func ServedSince(vendor string, since time.Time) ([]Served, error) {
	if adapter == nil || adapter.db == nil {
		return nil, errNoStore
	}
	var rows []Served
	err := adapter.db.Select().From("served").
		Where(dbx.And(dbx.HashExp{"vendor": vendor}, dbx.NewExp("[[hour]] >= {:h}", dbx.Params{"h": since.UTC().Format(servedHour)}))).
		All(&rows)
	return rows, err
}

// DropServed deletes every hour before before's.
func DropServed(before time.Time) error {
	if adapter == nil || adapter.db == nil {
		return nil
	}
	_, err := adapter.db.NewQuery("DELETE FROM {{served}} WHERE [[hour]] < {:h}").Bind(dbx.Params{"h": before.UTC().Format(servedHour)}).Execute()
	return err
}
