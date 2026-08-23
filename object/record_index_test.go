// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// A record whose action names the record API is dropped, so the valid slice is
// shorter than the input. The commit results come back one per VALID record, so
// the indices that place them have to address that slice — with a raw input index
// the results land on the wrong record, or past the end of the slice entirely,
// after the rows are already committed.
func TestCommitIndicesAddressTheValidRecords(t *testing.T) {
	withStore(t)
	if _, err := AddProvider(&Provider{
		Owner: "admin", Name: "chain", Category: "Blockchain", Type: "Tencent ChainMaker",
		State: "Active", ClientId: "x", ClientSecret: "y",
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	valid, idx, data, err := getValidAndNeedCommitRecords([]*Record{
		{Organization: "o", Method: "POST", Action: "add-record", NeedCommit: true},
		{Organization: "o", Method: "POST", Action: "chat", NeedCommit: true},
	})
	if err != nil {
		t.Skipf("no blockchain provider configured: %v", err)
	}
	if len(valid) != 1 {
		t.Fatalf("kept %d records, want the 1 that is not a record-API call", len(valid))
	}
	if len(data) != len(valid) {
		t.Fatalf("data has %d slots for %d valid records", len(data), len(valid))
	}
	for _, i := range idx {
		if i < 0 || i >= len(data) {
			t.Fatalf("commit index %d does not address the %d results", i, len(data))
		}
	}
}
