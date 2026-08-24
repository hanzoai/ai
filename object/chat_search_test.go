package object

import (
	"testing"
)

// Chat search by message text (?field=messages) has to actually return rows.
// The DISTINCT belongs to the SELECT clause, not to a column name — passed as
// one it is quoted as an identifier, `DISTINCT chat`.*, and every call fails
// with "no such table: DISTINCT chat" before the join is ever evaluated.
func TestGetPaginationChatsByMessages_ReturnsMatchingChats(t *testing.T) {
	restore, err := UseMemoryDB("file:chatsearch?mode=memory&cache=shared", &Chat{}, &Message{})
	if err != nil {
		t.Fatalf("memory db: %v", err)
	}
	defer restore()

	for i, name := range []string{"alpha", "bravo", "charlie"} {
		if err := insertRow(adapter.db, &Chat{Owner: "acme", Name: name, CreatedTime: string(rune('a' + i))}); err != nil {
			t.Fatalf("seed chat %s: %v", name, err)
		}
	}
	// alpha matches twice, so DISTINCT is what keeps it from appearing twice.
	for _, m := range []Message{
		{Owner: "acme", Name: "m1", Chat: "alpha", Text: "the needle is here"},
		{Owner: "acme", Name: "m2", Chat: "alpha", Text: "needle again"},
		{Owner: "acme", Name: "m3", Chat: "bravo", Text: "needle too"},
		{Owner: "acme", Name: "m4", Chat: "charlie", Text: "nothing to see"},
	} {
		msg := m
		if err := insertRow(adapter.db, &msg); err != nil {
			t.Fatalf("seed message %s: %v", m.Name, err)
		}
	}

	chats, err := getPaginationChatsByMessages("acme", "", 0, 10, "needle", "name", "ascend", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	got := []string{}
	for _, c := range chats {
		got = append(got, c.Name)
	}
	if len(got) != 2 || got[0] != "alpha" || got[1] != "bravo" {
		t.Fatalf("search for %q returned %v, want [alpha bravo]", "needle", got)
	}

	// The count beside it feeds the paginator and must agree.
	count, err := getChatCountByMessages("acme", "", "needle", "")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}
