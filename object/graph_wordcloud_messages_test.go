// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// The word cloud reads the messages of the chats it is given, oldest first, and
// no others. The query was a bare "{:p0}" bound to the list of names: a WHERE
// naming no column, with a slice no driver binds, so it failed every time.
func TestWordCloudReadsTheMessagesOfItsChats(t *testing.T) {
	withStore(t)
	for _, m := range []*Message{
		{Owner: "admin", Name: "m-3", Chat: "c1", CreatedTime: "2026-01-01T00:00:03Z", Author: "alice", Text: "third"},
		{Owner: "admin", Name: "m-1", Chat: "c1", CreatedTime: "2026-01-01T00:00:01Z", Author: "alice", Text: "first"},
		{Owner: "admin", Name: "m-2", Chat: "c2", CreatedTime: "2026-01-01T00:00:02Z", Author: "bob", Text: "second"},
		{Owner: "admin", Name: "m-x", Chat: "c3", CreatedTime: "2026-01-01T00:00:00Z", Author: "eve", Text: "elsewhere"},
	} {
		if err := insertRow(adapter.db, m); err != nil {
			t.Fatalf("seed %s: %v", m.Name, err)
		}
	}

	got, err := GetMessagesForChats([]*Chat{{Owner: "admin", Name: "c1"}, {Owner: "admin", Name: "c2"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range got {
		names = append(names, m.Name)
	}
	want := []string{"m-1", "m-2", "m-3"}
	if len(names) != len(want) {
		t.Fatalf("messages = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("messages = %v, want %v (oldest first, c3's left out)", names, want)
		}
	}
}
