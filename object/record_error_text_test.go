// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"fmt"
	"testing"
)

// Why a commit failed is stored on the record's own row, however the record is
// named: by id, or by owner and name when it has no id. The two branches were
// inverted, so a record with no id was updated WHERE id = 0 and the error was
// lost.
func TestACommitErrorLandsOnTheRecordsRow(t *testing.T) {
	withStore(t)
	if err := insertRow(adapter.db, &Record{Owner: "admin", Name: "r-1", Organization: "acme"}); err != nil {
		t.Fatal(err)
	}
	stored, err := GetRecord("admin/r-1", "en")
	if err != nil || stored == nil || stored.Id == 0 {
		t.Fatalf("seeded record = %+v, %v; want it back with an id", stored, err)
	}
	byId := fmt.Sprintf("admin/%d", stored.Id)

	for _, c := range []struct {
		how    string
		record *Record
	}{
		{"by owner and name, no id", &Record{Owner: "admin", Name: "r-1"}},
		{"by id, no name", &Record{Owner: "admin", Id: stored.Id}},
	} {
		text := "failed " + c.how
		updated, err := c.record.updateErrorText(text, "en")
		if err != nil {
			t.Fatalf("%s: %v", c.how, err)
		}
		if !updated {
			t.Errorf("%s: no row was updated", c.how)
		}
		got, err := GetRecord(byId, "en")
		if err != nil || got == nil {
			t.Fatalf("%s: read back %+v, %v", c.how, got, err)
		}
		if got.ErrorText != text {
			t.Errorf("%s: error text = %q, want %q", c.how, got.ErrorText, text)
		}
	}
}
