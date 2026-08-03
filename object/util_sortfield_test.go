package object

import (
	"strings"
	"testing"
)

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

	// Each payload is lowercase on purpose: that is what survives SnakeString.
	for _, payload := range []string{
		"(select 1)",
		"case when (select 1)=1 then name else id end",
		"(select group_concat(name) from sqlite_master)",
		"name desc, (select 1)",
		"{{name}}",
		"[[name]]",
		"name; drop table article",
		"name--",
	} {
		q := GetDbQuery("", 0, 10, "", "", payload, "ascend")
		sql := q.Build().SQL()
		if !strings.Contains(sql, "created_time") {
			t.Errorf("sortField=%q did not fall back to the default; SQL = %s", payload, sql)
		}
		// The payload's own distinguishing text must appear nowhere in the SQL.
		for _, frag := range []string{"select 1", "sqlite_master", "drop table", "case when", "--"} {
			if strings.Contains(payload, frag) && strings.Contains(sql, frag) {
				t.Errorf("sortField=%q leaked %q into SQL: %s", payload, frag, sql)
			}
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
