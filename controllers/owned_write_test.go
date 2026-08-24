// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"context"

	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
	"github.com/luxfi/zap"
)

// The listings for these tables are scoped and the writes were not, so a row
// could be written outside what its own listing would ever show. A write now
// lands where that listing looks for it.
func TestARowIsWrittenWhereItsListingLooks(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	acme := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})

	// A body naming another organization writes into the caller's own.
	c := as(visit("POST", "/v1/ai/add-template"), acme)
	c.Fiber().Request().SetBody([]byte(`{"owner":"other","name":"t1","manifest":"kind: Pod"}`))
	c.AddTemplate()
	if strings.Contains(sent(c), "error") {
		t.Fatalf("adding a template answered %s", sent(c))
	}
	if stored, err := object.GetTemplate("other/t1"); err != nil {
		t.Fatal(err)
	} else if stored != nil {
		t.Error("it was written into the organization the body named")
	}
	stored, err := object.GetTemplate("acme/t1")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatal("it was not written into the caller's own organization")
	}

	// And a table that belongs to people is written under the person.
	c = as(visit("POST", "/v1/ai/add-task"), acme)
	c.Fiber().Request().SetBody([]byte(`{"owner":"someone-else","name":"k1"}`))
	c.AddTask()
	if task, err := object.GetTask("alice/k1"); err != nil {
		t.Fatal(err)
	} else if task == nil {
		t.Error("a task was not written under the person who filed it")
	}
}

// And a replace reaches only what the caller's own listing would show.
func TestAReplaceReachesOnlyYourOwn(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	if _, err := object.AddTemplate(&object.Template{
		Owner: "other", Name: "theirs", Manifest: "kind: ConfigMap",
	}); err != nil {
		t.Fatal(err)
	}

	acme := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})
	c := as(visit("POST", "/v1/ai/update-template?id=other/theirs"), acme)
	c.Fiber().Request().SetBody([]byte(`{"owner":"other","name":"theirs","manifest":"kind: Pod"}`))
	c.UpdateTemplate()
	if !strings.Contains(sent(c), "does not exist") {
		t.Errorf("reaching another organization's template answered %s", sent(c))
	}
	after, err := object.GetTemplate("other/theirs")
	if err != nil {
		t.Fatal(err)
	}
	if after.Manifest != "kind: ConfigMap" {
		t.Errorf("it was replaced with %q", after.Manifest)
	}
}

// A table with no owner to write is refused rather than stored unscoped —
// silence is how a table nobody decided the ownership of would go unnoticed.
func TestATableWithNoOwnerIsRefused(t *testing.T) {
	type ownerless struct{ Name string }
	if err := ownedBy(&ownerless{}, "acme"); err == nil {
		t.Error("a row with no owner was stored anyway")
	}
	type owned struct{ Owner, Name string }
	row := &owned{Owner: "other"}
	if err := ownedBy(row, "acme"); err != nil {
		t.Fatal(err)
	}
	if row.Owner != "acme" {
		t.Errorf("the owner came out %q", row.Owner)
	}
	if err := ownedBy(nil, "acme"); err == nil {
		t.Error("nothing at all was stored anyway")
	}
}

// The ZAP surface files a row the same way the HTTP one does. Two of the four
// tables that reach it are the scan surface, so a scan filed either way lands in
// the organization that filed it.
func TestTheZapSurfaceWritesWhereTheListingLooksToo(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	key := people.asUser(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})

	if _, err := zapAddScanHandler(context.Background(), key,
		[]byte(`{"owner":"other","name":"s1","provider":"nmap","state":"Pending"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := zapAddAssetHandler(context.Background(), key,
		[]byte(`{"owner":"other","name":"a1"}`)); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"other/s1", "other/a1"} {
		var landed bool
		if strings.HasSuffix(id, "s1") {
			row, err := object.GetScan(id)
			if err != nil {
				t.Fatal(err)
			}
			landed = row != nil
		} else {
			row, err := object.GetAsset(id)
			if err != nil {
				t.Fatal(err)
			}
			landed = row != nil
		}
		if landed {
			t.Errorf("%s was written into the organization the body named", id)
		}
	}

	// And into the caller's own, which is where its listing looks.
	if row, err := object.GetScan("acme/s1"); err != nil {
		t.Fatal(err)
	} else if row == nil {
		t.Error("the scan was not written into the caller's own organization")
	}
}

// The application and scan surfaces answer the same on both surfaces. An
// application is a manifest and a namespace to apply it in; a scan is a command
// line handed to a security tool. Neither reaches another organization's.
func TestBothSurfacesAgreeOnWhoseApplicationAndScan(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	key := people.asUser(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})

	for _, seed := range []func() error{
		func() error {
			_, err := object.AddTemplate(&object.Template{Owner: "other", Name: "t", Manifest: "kind: ConfigMap"})
			return err
		},
		func() error {
			_, err := object.AddApplication(&object.Application{Owner: "other", Name: "theirs", Template: "t", DisplayName: "theirs"})
			return err
		},
		func() error {
			_, err := object.AddScan(&object.Scan{Owner: "other", Name: "theirs", Provider: "nmap"})
			return err
		},
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}

	// Reaching another organization's application by id.
	for _, run := range []func(context.Context, string, []byte) (*zap.Message, error){
		zapUpdateApplicationHandler,
		zapDeployApplicationHandler,
	} {
		if _, err := run(context.Background(), key,
			[]byte(`{"id":"other/theirs","owner":"other","name":"theirs","template":"t","displayName":"taken over"}`)); err != nil {
			t.Fatal(err)
		}
	}
	app, err := object.GetApplication("other/theirs")
	if err != nil {
		t.Fatal(err)
	}
	if app.DisplayName != "theirs" {
		t.Errorf("another organization's application was rewritten to %q", app.DisplayName)
	}
	// The namespace is derived either way, which is the other rule, not this one.
	if app.Namespace != "hanzo-cloud-theirs" {
		t.Errorf("its namespace became %q", app.Namespace)
	}

	// And its scan.
	if _, err := zapUpdateScanHandler(context.Background(), key,
		[]byte(`{"id":"other/theirs","scan":{"owner":"other","name":"theirs","command":"-oN /tmp/x %s"}}`)); err != nil {
		t.Fatal(err)
	}
	scan, err := object.GetScan("other/theirs")
	if err != nil {
		t.Fatal(err)
	}
	if scan.Command != "" {
		t.Errorf("another organization's scan command became %q", scan.Command)
	}
}

// The remaining tables answer the same on both surfaces too. Each row is written
// where its own listing looks, whichever surface filed it.
func TestTheZapTwinsWriteWhereTheListingLooks(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	key := people.asUser(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})

	for _, c := range []struct {
		what  string
		run   func(context.Context, string, []byte) (*zap.Message, error)
		body  string
		there func() (bool, error)
	}{
		{"a form", zapAddFormHandler, `{"owner":"other","name":"f1"}`,
			func() (bool, error) { r, err := object.GetForm("other/f1"); return r != nil, err }},
		{"an article", zapAddArticleHandler, `{"owner":"other","name":"a1"}`,
			func() (bool, error) { r, err := object.GetArticle("other/a1"); return r != nil, err }},
		{"a graph", zapAddGraphHandler, `{"owner":"other","name":"g1"}`,
			func() (bool, error) { r, err := object.GetGraph("other/g1"); return r != nil, err }},
		{"a vector", zapAddVectorHandler, `{"owner":"other","name":"v1"}`,
			func() (bool, error) { r, err := object.GetVector("other/v1"); return r != nil, err }},
	} {
		if _, err := c.run(context.Background(), key, []byte(c.body)); err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		landed, err := c.there()
		if err != nil {
			t.Fatal(err)
		}
		if landed {
			t.Errorf("%s was written into the organization the body named", c.what)
		}
	}

	// And into the caller's own.
	if r, err := object.GetForm("acme/f1"); err != nil {
		t.Fatal(err)
	} else if r == nil {
		t.Error("the form was not written into the caller's own organization")
	}
}
