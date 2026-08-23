// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import "testing"

// An update is addressed by the id the caller was authorized for. The body says
// what to write, never which row — so a body naming a different message must not
// reach that message, on either branch.
func TestAnUpdateWritesTheRowItsIdNames(t *testing.T) {
	withStore(t)
	for _, name := range []string{"m-mine", "m-theirs"} {
		if _, err := AddMessage(&Message{
			Owner: "admin", Name: name, Organization: "acme", Chat: "c", User: "alice",
			Text: "original " + name,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The hit path, with a body naming someone else's message.
	body := &Message{Owner: "admin", Name: "m-theirs", Text: "rewritten"}
	if _, err := UpdateMessage("admin/m-mine", body, true); err != nil {
		t.Fatal(err)
	}
	theirs, err := GetMessage("admin/m-theirs")
	if err != nil {
		t.Fatal(err)
	}
	if theirs.Text != "original m-theirs" {
		t.Errorf("the other message was rewritten to %q", theirs.Text)
	}

	// And a hit changes the suggestions only — not the text of the row it does
	// address.
	mine, err := GetMessage("admin/m-mine")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Text != "original m-mine" {
		t.Errorf("a hit rewrote the addressed message's text to %q", mine.Text)
	}

	// The ordinary path still writes the row its id names.
	if _, err := UpdateMessage("admin/m-mine", &Message{Text: "answered"}, false); err != nil {
		t.Fatal(err)
	}
	if mine, err = GetMessage("admin/m-mine"); err != nil {
		t.Fatal(err)
	} else if mine.Text != "answered" {
		t.Errorf("the addressed message reads %q, want the update", mine.Text)
	}
}
