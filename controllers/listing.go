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

package controllers

// listing.go — a vendor's model list, kept in the store.
//
// OpenRouter's /v1/models is synced into the store (object.Listing): when a process
// starts, unless another synced in the last listingStart, and once every
// listingEvery after, fleet-wide. A lease in the store lets exactly one process run
// each sync while the rest read its result. A sync keeps every entry exactly as
// listed, records each change it reads (object.ListingEvent: new, back, gone,
// repriced, turned free, turned paid), and puts them on the host's bus.
//
// The family reads its catalog from the store and never from the vendor, so every
// process lists, routes and bills from the same rows, and the free lane reads the
// same rows (GET /v1/models/vendors/openrouter?free=1). A model the vendor stops listing
// leaves the catalog at the next sync and is served no longer; its row and its
// history stay.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/ai/log"
	"github.com/hanzoai/ai/object"
	"github.com/hanzoai/decimal"
)

const (
	// listingEvery is how often the fleet syncs a vendor's list.
	listingEvery = time.Hour
	// listingStart is how recent a sync a starting process takes as its own.
	listingStart = 5 * time.Minute
	// servedKeep is how many hours of served counts the store keeps.
	servedKeep = 48 * time.Hour
)

// errUnlisted is a family whose list no sync has written yet.
var errUnlisted = errors.New("no sync has listed this vendor yet")

// listed is the family's catalog as the store holds it: {"data": [...]}, each entry
// the vendor's own, of every model it lists now. A process's first read syncs unless
// one synced within listingStart; a later read syncs once listingEvery has passed.
func (f *modelFamily) listed(ctx context.Context, p *object.Provider) ([]byte, error) {
	every := listingEvery
	if !f.started.Swap(true) {
		every = listingStart
	}
	if _, err := f.syncEvery(ctx, p, every, time.Now()); err != nil {
		log.Warning("%s listing: sync failed: %v", f.name, err)
	}
	rows, err := object.Listings(f.name)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(`{"data":[`)
	n := 0
	for _, r := range rows {
		if r.Gone != "" {
			continue
		}
		if n > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(r.Item)
		n++
	}
	buf.WriteString("]}")
	if n == 0 {
		return nil, errUnlisted
	}
	return buf.Bytes(), nil
}

// keepListed reads the family's catalog every TTL, so the fleet syncs on schedule
// whether or not anything asks for the family.
func (f *modelFamily) keepListed() {
	for range time.Tick(zenCatalogTTL) {
		f.fresh()
	}
}

// syncEvery syncs the family's list if no process has within every, and reports
// what it changed; nil when another process holds the interval.
func (f *modelFamily) syncEvery(ctx context.Context, p *object.Provider, every time.Duration, now time.Time) (*synced, error) {
	lease := "listing:" + f.name
	took, at, err := object.TakeLease(lease, every, now)
	if err != nil || !took {
		return nil, err
	}
	s, err := f.sync(ctx, p, now)
	if err != nil {
		if rerr := object.ReleaseLease(lease, at); rerr != nil {
			log.Warning("%s listing: lease not given back: %v", f.name, rerr)
		}
		return nil, err
	}
	log.Info("%s listing: synced %d models: %d new, %d back, %d gone, %d repriced", f.name, s.listed, s.count(object.ListingNew), s.count(object.ListingBack), s.count(object.ListingGone), s.repriced())
	return s, nil
}

// synced is what one sync read.
type synced struct {
	listed int
	events []object.ListingEvent
}

func (s *synced) count(kind string) int {
	n := 0
	for _, e := range s.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func (s *synced) repriced() int {
	return s.count(object.ListingPrice) + s.count(object.ListingFree) + s.count(object.ListingPaid)
}

// sync reads the vendor's list and writes it to the store: every entry as listed,
// the gone marked, each change recorded and put on the bus.
func (f *modelFamily) sync(ctx context.Context, p *object.Provider, now time.Time) (*synced, error) {
	ctx, cancel := context.WithTimeout(ctx, listWait)
	defer cancel()
	body, err := f.list(ctx, p)
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("%s /v1/models: %w", f.name, err)
	}
	// A list of nothing is a list gone wrong, and syncing it would mark every model
	// gone.
	if len(list.Data) == 0 {
		return nil, fmt.Errorf("%s /v1/models lists nothing", f.name)
	}
	rows, err := object.Listings(f.name)
	if err != nil {
		return nil, err
	}
	have := make(map[string]*object.Listing, len(rows))
	for i := range rows {
		have[rows[i].ID] = &rows[i]
	}
	stamp := now.UTC().Format(object.StampTime)
	s := &synced{}
	event := func(id, kind, was, is string) {
		s.events = append(s.events, object.ListingEvent{Vendor: f.name, ID: id, Time: stamp, Kind: kind, Was: was, Now: is})
	}
	seen := make(map[string]bool, len(list.Data))
	for _, raw := range list.Data {
		var w openrouterWireModel
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("%s /v1/models: %w", f.name, err)
		}
		id := strings.TrimSpace(w.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var item bytes.Buffer
		if err := json.Compact(&item, raw); err != nil {
			return nil, err
		}
		price, err := prices(w.Pricing.All)
		if err != nil {
			return nil, fmt.Errorf("%s %s pricing: %w", f.name, id, err)
		}
		free := w.free() && !w.variable()
		row, ok := have[id]
		if !ok {
			event(id, object.ListingNew, "", price)
			if err := object.AddListing(&object.Listing{Vendor: f.name, ID: id, Item: item.String(), Pricing: price, Free: free, Seen: stamp, Synced: stamp}); err != nil {
				return nil, err
			}
			continue
		}
		switch {
		case row.Gone != "":
			event(id, object.ListingBack, row.Pricing, price)
		case row.Pricing != price:
			kind := object.ListingPrice
			switch {
			case row.Free && !free:
				kind = object.ListingPaid
			case !row.Free && free:
				kind = object.ListingFree
			}
			event(id, kind, row.Pricing, price)
		}
		row.Item, row.Pricing, row.Free, row.Synced, row.Gone = item.String(), price, free, stamp, ""
		if err := object.UpdateListing(row); err != nil {
			return nil, err
		}
	}
	for i := range rows {
		r := &rows[i]
		if seen[r.ID] || r.Gone != "" {
			continue
		}
		event(r.ID, object.ListingGone, r.Pricing, "")
		r.Gone = stamp
		if err := object.UpdateListing(r); err != nil {
			return nil, err
		}
	}
	s.listed = len(seen)
	if err := object.AddListingEvents(s.events); err != nil {
		return nil, err
	}
	// A sync that seeds an empty store changed nothing anyone was told of: its rows
	// are recorded, and the bus hears from the next sync on.
	if len(rows) > 0 {
		object.PublishListing(ctx, s.events)
	}
	if err := object.DropServed(now.Add(-servedKeep)); err != nil {
		log.Warning("%s listing: served hours not dropped: %v", f.name, err)
	}
	return s, nil
}

// prices is a price list with its keys sorted and each rate written as its exact
// decimal, so it changes when a price does and not when a number's spelling does.
func prices(all map[string]json.RawMessage) (string, error) {
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				t[k] = walk(x)
			}
		case []any:
			for i, x := range t {
				t[i] = walk(x)
			}
		case string:
			if d, err := decimal.Parse(t); err == nil {
				return d.String()
			}
		case json.Number:
			if d, err := decimal.Parse(t.String()); err == nil {
				return json.Number(d.String())
			}
		}
		return v
	}
	out := make(map[string]any, len(all))
	for k, raw := range all {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return "", err
		}
		out[k] = walk(v)
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// listedFamily is the family whose list the store keeps under vendor, or nil.
func listedFamily(vendor string) *modelFamily {
	for _, f := range modelFamilies {
		if f.load != nil && strings.EqualFold(f.name, vendor) {
			return f
		}
	}
	return nil
}

// ListListing lists every model a vendor lists now, each entry exactly as the vendor
// listed it (prices, context, output bound, modalities, parameters, created,
// expiration), and when the list was last synced. ?free=1 keeps the models the
// vendor charges nothing for. Public, as the vendor's own list is: the free lane
// reads it in place of the vendor's.
func (c *ApiController) ListListing() {
	f := listedFamily(c.Param("vendor"))
	if f == nil {
		c.ResponseErrorWithStatus(http.StatusNotFound, "no listing for this vendor")
		return
	}
	rows, err := object.Listings(f.name)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, err.Error())
		return
	}
	free := c.Input().Get("free")
	only := free == "1" || strings.EqualFold(free, "true")
	out := struct {
		Vendor string            `json:"vendor"`
		Synced string            `json:"synced"`
		Data   []json.RawMessage `json:"data"`
	}{Vendor: f.name, Data: []json.RawMessage{}}
	for _, r := range rows {
		if r.Synced > out.Synced {
			out.Synced = r.Synced
		}
		if r.Gone != "" || (only && !r.Free) {
			continue
		}
		out.Data = append(out.Data, json.RawMessage(r.Item))
	}
	c.JSON(http.StatusOK, out)
}

// ListListingEvents lists the changes syncs read in a vendor's model list, newest
// first. Each is new, back, gone, price, free (was priced, now free) or paid (was
// free, now priced), with the price list before and after. ?id= keeps one model's;
// ?limit= bounds the rows (100, at most 1000).
func (c *ApiController) ListListingEvents() {
	f := listedFamily(c.Param("vendor"))
	if f == nil {
		c.ResponseErrorWithStatus(http.StatusNotFound, "no listing for this vendor")
		return
	}
	limit := 100
	if n, err := strconv.Atoi(c.Input().Get("limit")); err == nil && n > 0 {
		limit = min(n, 1000)
	}
	evs, err := object.ListingEvents(f.name, c.Input().Get("id"), limit)
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, err.Error())
		return
	}
	if evs == nil {
		evs = []object.ListingEvent{}
	}
	c.JSON(http.StatusOK, struct {
		Data []object.ListingEvent `json:"data"`
	}{evs})
}

// freeModel is one model of the free set and what the last day did with it.
type freeModel struct {
	ID       string        `json:"id"`
	Context  int           `json:"context"`
	Since    string        `json:"since"` // when a sync first listed it
	Answered int64         `json:"answered"`
	Failed   int64         `json:"failed"`
	Rate     *float64      `json:"rate"` // answered over asked; null when nothing asked
	Accounts []freeAccount `json:"accounts"`
}

// freeAccount is what one of our accounts did with a free model in the last day.
type freeAccount struct {
	Account  string `json:"account"` // its name, never its key; "" when the answer named none
	Answered int64  `json:"answered"`
	Failed   int64  `json:"failed"`
}

// AdminFree lists every model OpenRouter lists free now, each with what the last 24
// hours did with it: the requests answered and failed, the share answered, and which
// of our accounts served them. SuperAdmin only.
func (c *ApiController) AdminFree() {
	if !c.RequireSuperAdmin() {
		return
	}
	out, err := freeSet(openrouterFam.name, time.Now())
	if err != nil {
		c.ResponseErrorWithStatus(http.StatusServiceUnavailable, err.Error())
		return
	}
	c.JSON(http.StatusOK, struct {
		Data []freeModel `json:"data"`
	}{out})
}

// freeSet is vendor's free models listed now with the last day's counts.
func freeSet(vendor string, now time.Time) ([]freeModel, error) {
	rows, err := object.Listings(vendor)
	if err != nil {
		return nil, err
	}
	served, err := object.ServedSince(vendor, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	by := map[string]map[string]*freeAccount{}
	for _, s := range served {
		accts := by[s.Model]
		if accts == nil {
			accts = map[string]*freeAccount{}
			by[s.Model] = accts
		}
		a := accts[s.Account]
		if a == nil {
			a = &freeAccount{Account: s.Account}
			accts[s.Account] = a
		}
		a.Answered += s.Answered
		a.Failed += s.Failed
	}
	out := []freeModel{}
	for _, r := range rows {
		if r.Gone != "" || !r.Free {
			continue
		}
		var w openrouterWireModel
		_ = json.Unmarshal([]byte(r.Item), &w)
		m := freeModel{ID: r.ID, Context: w.ContextLength, Since: r.Seen, Accounts: []freeAccount{}}
		for _, a := range by[r.ID] {
			m.Answered += a.Answered
			m.Failed += a.Failed
			m.Accounts = append(m.Accounts, *a)
		}
		sort.Slice(m.Accounts, func(i, j int) bool { return m.Accounts[i].Account < m.Accounts[j].Account })
		if asked := m.Answered + m.Failed; asked > 0 {
			rate := float64(m.Answered) / float64(asked)
			m.Rate = &rate
		}
		out = append(out, m)
	}
	return out, nil
}

// served counts one request an account made for model in the store's served hours:
// answered when the vendor accepted it. Off the request's path.
func (f *modelFamily) served(model, account string, answered bool) {
	if f.load == nil || model == "" {
		return
	}
	go func() {
		if err := object.CountServed(f.name, model, account, time.Now(), answered); err != nil {
			log.Warning("%s listing: served not counted: %v", f.name, err)
		}
	}()
}
