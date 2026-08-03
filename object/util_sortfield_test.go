package object

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// sortPayloads are the values a caller can send as ?sortField=. Each is
// lowercase on purpose — that is what survives SnakeString — and each uses
// "/**/" where a space would go, because SnakeString strips spaces.
var sortPayloads = []string{
	"(select 1)",
	"case when (select 1)=1 then name else id end",
	"(select group_concat(name) from sqlite_master)",
	"name desc, (select 1)",
	"{{name}}",
	"[[name]]",
	"name; drop table article",
	"name--",
	"(select/**/group_concat(name)/**/from/**/sqlite_master)",
	"iif((select/**/count(*)/**/from/**/chat)>2,name,owner)",
	"name,(select/**/group_concat(name)/**/from/**/sqlite_master)",
}

// sortPayloadFragments are the distinguishing substrings that must never appear
// in emitted SQL. A payload leaks if it carries a fragment and the SQL has it.
var sortPayloadFragments = []string{"select 1", "sqlite_master", "drop table", "case when", "--", "iif(", "group_concat"}

// assertNoPayloadLeak fails when a payload's own text reached the SQL, and when
// the query did not fall back to the default column.
func assertNoPayloadLeak(t *testing.T, payload, sql string) {
	t.Helper()
	if !strings.Contains(sql, "created_time") {
		t.Errorf("sortField=%q did not fall back to the default; SQL = %s", payload, sql)
	}
	for _, frag := range sortPayloadFragments {
		if strings.Contains(payload, frag) && strings.Contains(sql, frag) {
			t.Errorf("sortField=%q leaked %q into SQL: %s", payload, frag, sql)
		}
	}
}

// captureSQL records every statement the adapter sends to the driver while fn
// runs. Some call sites build and execute the query internally, so the emitted
// SQL is only observable from the driver side.
func captureSQL(t *testing.T, fn func()) []string {
	t.Helper()
	var got []string
	adapter.db.QueryLogFunc = func(ctx context.Context, d time.Duration, s string, rows *sql.Rows, err error) {
		got = append(got, s)
	}
	defer func() { adapter.db.QueryLogFunc = nil }()
	fn()
	return got
}

// The sort column is caller-supplied and lands in an ORDER BY identifier
// position, where a bound parameter cannot reach. Two layers that look like
// they protect it do not:
//
//   - dbx.QuoteColumnName returns its argument UNQUOTED when it contains "(",
//     "{{" or "[[", so a value carrying a parenthesis is concatenated verbatim.
//   - util.SnakeString only lowercases and inserts "_" before capitals, so an
//     all-lowercase payload passes through it byte for byte.
//
// These assert on the SQL the real builder emits against the real driver, so
// they fail if either layer's behaviour changes underneath the whitelist.
func TestGetDbQuery_SortFieldCannotReachOrderBy(t *testing.T) {
	restore, err := UseMemoryDB("file:sortfield?mode=memory&cache=shared", &Article{})
	if err != nil {
		t.Fatalf("memory db: %v", err)
	}
	defer restore()

	for _, payload := range sortPayloads {
		q := GetDbQuery("", 0, 10, "", "", payload, "ascend")
		assertNoPayloadLeak(t, payload, q.Build().SQL())
	}
}

// The chat list has a second sort site: ?field=messages searches message text
// through a join, and that branch builds its own ORDER BY with a "chat." table
// prefix instead of going through GetDbQuery. The prefix is not a defence — a
// comma starts a second ORDER BY term, so "name,(select …)" lands whole.
func TestGetPaginationChatsByMessages_SortFieldCannotReachOrderBy(t *testing.T) {
	restore, err := UseMemoryDB("file:sortfieldchat?mode=memory&cache=shared", &Chat{}, &Message{})
	if err != nil {
		t.Fatalf("memory db: %v", err)
	}
	defer restore()

	for _, payload := range sortPayloads {
		for _, stmt := range captureSQL(t, func() {
			_, _ = getPaginationChatsByMessages("", 0, 10, "needle", payload, "ascend", "")
		}) {
			assertNoPayloadLeak(t, payload, stmt)
		}
	}
}

// The provider list has a third sort site: when a separate provider database is
// configured, the remote half of the result set is sorted by a hand-built ORDER
// BY rather than by GetDbQuery. That half reads a DIFFERENT database, so a
// payload here walks rows the caller's own database does not even hold.
func TestGetPaginationProviders_SortFieldCannotReachOrderBy(t *testing.T) {
	restore, err := UseMemoryDB("file:sortfieldprov?mode=memory&cache=shared", &Provider{})
	if err != nil {
		t.Fatalf("memory db: %v", err)
	}
	defer restore()

	prev := providerAdapter
	providerAdapter = adapter
	defer func() { providerAdapter = prev }()

	for _, payload := range sortPayloads {
		for _, stmt := range captureSQL(t, func() {
			_, _ = GetPaginationProviders("acme", "", -1, -1, "", "", payload, "ascend")
		}) {
			assertNoPayloadLeak(t, payload, stmt)
		}
	}
}

// The whitelist must not break real sorting. The UI sends Ant Design dataIndex
// names, which are alphanumeric; each must survive to its snake_case column.
func TestGetDbQuery_LegitimateSortFieldsStillSort(t *testing.T) {
	restore, err := UseMemoryDB("file:sortfield2?mode=memory&cache=shared", &Article{})
	if err != nil {
		t.Fatalf("memory db: %v", err)
	}
	defer restore()

	for field, want := range map[string]string{
		"createdTime": "created_time",
		"name":        "name",
		"displayName": "display_name",
		"owner":       "owner",
	} {
		q := GetDbQuery("", 0, 10, "", "", field, "descend")
		sql := q.Build().SQL()
		if !strings.Contains(sql, want) {
			t.Errorf("sortField=%q should sort by %q, SQL = %s", field, want, sql)
		}
		if !strings.Contains(sql, "DESC") {
			t.Errorf("sortField=%q lost its DESC direction: %s", field, sql)
		}
	}
}

// Same requirement for the two sites that build their own ORDER BY: a real
// column must still reach it, prefixed and directed as before.
func TestOtherSortSites_LegitimateSortFieldsStillSort(t *testing.T) {
	restore, err := UseMemoryDB("file:sortfield3?mode=memory&cache=shared", &Chat{}, &Message{}, &Provider{})
	if err != nil {
		t.Fatalf("memory db: %v", err)
	}
	defer restore()

	prev := providerAdapter
	providerAdapter = adapter
	defer func() { providerAdapter = prev }()

	for field, want := range map[string]string{
		"createdTime": "created_time",
		"name":        "name",
		"displayName": "display_name",
	} {
		chatSQL := captureSQL(t, func() {
			_, _ = getPaginationChatsByMessages("", 0, 10, "needle", field, "descend", "")
		})
		if len(chatSQL) == 0 {
			t.Fatalf("sortField=%q: chat query emitted no SQL", field)
		}
		for _, stmt := range chatSQL {
			if !strings.Contains(stmt, "`chat`.`"+want+"` DESC") {
				t.Errorf("chat sortField=%q should sort by chat.%s DESC, SQL = %s", field, want, stmt)
			}
		}

		provSQL := captureSQL(t, func() {
			_, _ = GetPaginationProviders("acme", "", -1, -1, "", "", field, "ascend")
		})
		if len(provSQL) == 0 {
			t.Fatalf("sortField=%q: provider query emitted no SQL", field)
		}
		for _, stmt := range provSQL {
			if !strings.Contains(stmt, "`"+want+"` ASC") {
				t.Errorf("provider sortField=%q should sort by %s ASC, SQL = %s", field, want, stmt)
			}
		}
	}
}
