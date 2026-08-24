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

package controllers

import (
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// A video row says whether it is public. The listing drew exactly the rows that
// say so — in the browser, once the whole table had crossed the network — so the
// rows it dropped were delivered first. The row's own answer decides the listing
// on this side of the network now, and the set it draws is the same one.
func TestThePublicListingCarriesOnlyPublicVideos(t *testing.T) {
	withStore(t)

	for _, v := range []*object.Video{
		{Owner: "alice", Name: "open", IsPublic: true},
		{Owner: "alice", Name: "shut"},
		{Owner: "bob", Name: "also-shut"},
	} {
		if _, err := object.AddVideo(v); err != nil {
			t.Fatal(err)
		}
	}

	shown, err := object.GetGlobalVideos()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range shown {
		if !v.IsPublic {
			t.Errorf("the public listing carried %s/%s, which does not say it is public", v.Owner, v.Name)
		}
	}
	if len(shown) != 1 {
		t.Errorf("the public listing carried %d rows, want the one that says it is public", len(shown))
	}
}

// The public listing links straight to the single read, so a public video answers
// anyone. Every other video answers the person whose uploads it is among, and
// tells anyone else what it tells someone asking after a video that is not there.
func TestOnlyAPublicVideoAnswersAReaderWithNoName(t *testing.T) {
	withStore(t)
	people := withIAM(t)

	for _, v := range []*object.Video{
		{Owner: "alice", Name: "open", DisplayName: "the-open-one", IsPublic: true},
		{Owner: "alice", Name: "shut", DisplayName: "the-shut-one"},
	} {
		if _, err := object.AddVideo(v); err != nil {
			t.Fatal(err)
		}
	}

	c := visit("GET", "/v1/ai/get-video?id=alice/open")
	c.GetVideo()
	if !strings.Contains(sent(c), "the-open-one") {
		t.Errorf("a public video answered %s", sent(c))
	}

	c = visit("GET", "/v1/ai/get-video?id=alice/shut")
	c.GetVideo()
	if strings.Contains(sent(c), "the-shut-one") {
		t.Errorf("a video that is not public answered a reader with no name: %s", sent(c))
	}

	bob := people.signedIn(t, &iam.User{Owner: "acme", Name: "bob"})
	c = as(visit("GET", "/v1/ai/get-video?id=alice/shut"), bob)
	c.GetVideo()
	if strings.Contains(sent(c), "the-shut-one") {
		t.Errorf("a video that is not public answered another person: %s", sent(c))
	}

	alice := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice"})
	c = as(visit("GET", "/v1/ai/get-video?id=alice/shut"), alice)
	c.GetVideo()
	if !strings.Contains(sent(c), "the-shut-one") {
		t.Errorf("the person whose upload it is was refused it: %s", sent(c))
	}
}

// The id names the row that gets written, and the store takes the row's owner
// from that id — so the id is what has to belong to the caller. Stamping Owner on
// the body reads like the check and is overwritten one call later.
func TestAVideoIsWrittenOnlyByWhoseUploadItIs(t *testing.T) {
	withStore(t)
	people := withIAM(t)

	if _, err := object.AddVideo(&object.Video{
		Owner: "alice", Name: "hers", DisplayName: "hers", State: "Draft",
	}); err != nil {
		t.Fatal(err)
	}

	bob := people.signedIn(t, &iam.User{Owner: "acme", Name: "bob"})
	c := as(visit("POST", "/v1/ai/update-video?id=alice/hers"), bob)
	c.Fiber().Request().SetBody([]byte(`{"owner":"alice","name":"hers","displayName":"his-now","state":"Draft"}`))
	c.UpdateVideo()
	if !strings.Contains(sent(c), "does not exist") {
		t.Errorf("writing another person's video answered %s", sent(c))
	}

	after, err := object.GetVideo("alice/hers")
	if err != nil {
		t.Fatal(err)
	}
	if after == nil {
		t.Fatal("the row is gone")
	}
	if after.DisplayName != "hers" {
		t.Errorf("the row now reads %q", after.DisplayName)
	}
}
