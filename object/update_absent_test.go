// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// An update whose key names no row writes nothing. dbx's model update discards
// the result, so unless the row is read first the call cannot tell the caller
// that — and every caller reports what it returns to a client as success.
func TestUpdatingSomethingThatIsNotThereSaysSo(t *testing.T) {
	withStore(t)

	t.Run("provider", func(t *testing.T) {
		// A masked secret is what an edit form sends back for a field it did not
		// change, so the merge reads the stored row — which is not there.
		ok, err := UpdateProvider("admin/nope", &Provider{
			Owner: "admin", Name: "nope", Category: "Model", ClientSecret: SecretMask,
		})
		if err != nil {
			t.Fatalf("answered an error rather than absence: %v", err)
		}
		if ok {
			t.Error("reported that it updated a provider that is not there")
		}
	})

	t.Run("model route", func(t *testing.T) {
		ok, err := UpdateModelRoute("admin", "nope", &ModelRoute{Owner: "admin", ModelName: "nope"})
		if err != nil {
			t.Fatalf("answered an error rather than absence: %v", err)
		}
		if ok {
			t.Error("reported that it updated a route that is not there")
		}
	})

	// And a row that IS there still updates.
	t.Run("present", func(t *testing.T) {
		if _, err := AddModelRoute(&ModelRoute{Owner: "admin", ModelName: "real", Enabled: true}); err != nil {
			t.Fatal(err)
		}
		ok, err := UpdateModelRoute("admin", "real", &ModelRoute{Owner: "admin", ModelName: "real"})
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("would not update a route that is there")
		}
	})
}
