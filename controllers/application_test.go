// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"strings"
	"testing"

	iam "github.com/hanzoai/ai/internal/iam"
	"github.com/hanzoai/ai/object"
)

// An application's namespace is where its manifest is applied, and a manifest
// carries any kind. So the namespace is derived from the application's name and
// never taken from a request: one application, one namespace, decided here.
func TestAnApplicationsNamespaceIsNotTheCallersToChoose(t *testing.T) {
	withStore(t)
	if _, err := object.AddApplication(&object.Application{
		Owner: "acme", Name: "app", Template: "t",
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := object.GetApplication("acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Namespace != "hanzo-cloud-app" {
		t.Fatalf("created with namespace %q", stored.Namespace)
	}

	// An update naming another namespace does not move it.
	if _, err := object.UpdateApplication("acme/app", &object.Application{
		Owner: "acme", Name: "app", Template: "t", Namespace: "kube-system",
	}); err != nil {
		t.Fatal(err)
	}
	stored, err = object.GetApplication("acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Namespace != "hanzo-cloud-app" {
		t.Errorf("an update moved it to %q", stored.Namespace)
	}
}

// An update against a name nobody has wrote nothing and said it had.
func TestUpdatingAnApplicationThatIsNotThereSaysSo(t *testing.T) {
	withStore(t)
	ok, err := object.UpdateApplication("acme/nope", &object.Application{
		Owner: "acme", Name: "nope", Template: "t",
	})
	if err != nil {
		t.Fatalf("answered an error rather than absence: %v", err)
	}
	if ok {
		t.Error("reported that it updated an application that is not there")
	}
}

// Acting on an application is creating objects in a cluster, so it is one the
// caller's own organization owns.
func TestActingOnAnotherOrganizationsApplication(t *testing.T) {
	withStore(t)
	people := withIAM(t)
	if _, err := object.AddApplication(&object.Application{
		Owner: "other", Name: "theirs", Template: "t",
	}); err != nil {
		t.Fatal(err)
	}

	acme := people.signedIn(t, &iam.User{Owner: "acme", Name: "alice", IsAdmin: true})
	for _, call := range []struct {
		name string
		run  func(*ApiController)
	}{
		{"UpdateApplication", (*ApiController).UpdateApplication},
		{"DeployApplication", (*ApiController).DeployApplication},
		{"UndeployApplication", (*ApiController).UndeployApplication},
	} {
		c := as(visit("POST", "/v1/ai/x?id=other/theirs"), acme)
		c.Fiber().Request().SetBody([]byte(`{"owner":"other","name":"theirs","namespace":"kube-system"}`))
		call.run(c)
		if !strings.Contains(sent(c), "does not exist") && !strings.Contains(sent(c), "error") {
			t.Errorf("%s answered %s", call.name, sent(c))
		}
	}

	// And its own organization's it can reach.
	if _, err := object.AddApplication(&object.Application{
		Owner: "acme", Name: "mine", Template: "t",
	}); err != nil {
		t.Fatal(err)
	}
	c := as(visit("POST", "/v1/ai/x?id=acme/mine"), acme)
	c.Fiber().Request().SetBody([]byte(`{"owner":"acme","name":"mine"}`))
	c.UndeployApplication()
	if strings.Contains(sent(c), "does not exist") {
		t.Errorf("acme could not reach its own application: %s", sent(c))
	}
}
