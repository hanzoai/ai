// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package util

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSnakeString pins the exact output of SnakeString.
//
// SnakeString derives SQL column names from Go field names (see the ORDER BY and
// LIKE call sites in object/), so its output is part of the on-disk schema
// contract: every change here is a column rename. hanzoai/ai and this repo
// carried two spellings of this function that disagreed on leading underscores
// and on non-ASCII input; the table below is the single agreed behaviour.
func TestSnakeString(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{

		// empty and separator-only
		{"", ""},
		{"_", "_"},
		{"__", "__"},
		{"a", "a"},
		{"A", "a"},
		{" ", ""},
		{"  ", ""},

		// leading underscore: ai emits no extra separator (vm used to double it)
		{"_Foo", "_foo"},
		{"__Foo", "__foo"},
		{"___Foo", "___foo"},
		{"_FOO", "_f_o_o"},
		{"_F", "_f"},
		{"_fooBar", "_foo_bar"},
		{"_Foo_Bar", "_foo__bar"},
		{"_Foo_", "_foo_"},
		{"__Foo__", "__foo__"},

		// trailing underscore
		{"Foo_", "foo_"},
		{"Foo__", "foo__"},

		// plain camel and pascal
		{"Foo", "foo"},
		{"FooBar", "foo_bar"},
		{"fooBar", "foo_bar"},
		{"FooBarBaz", "foo_bar_baz"},
		{"fooBarBaz", "foo_bar_baz"},

		// consecutive capitals stay naive, one separator per capital
		{"HTTPServer", "h_t_t_p_server"},
		{"HTTP", "h_t_t_p"},
		{"ID", "i_d"},
		{"UserID", "user_i_d"},
		{"userID", "user_i_d"},
		{"APIKey", "a_p_i_key"},
		{"URLPath", "u_r_l_path"},
		{"CPU", "c_p_u"},
		{"SHA", "s_h_a"},

		// digits
		{"Foo2Bar", "foo2_bar"},
		{"foo2Bar", "foo2_bar"},
		{"V2Model", "v2_model"},
		{"Model2", "model2"},
		{"S3Bucket", "s3_bucket"},
		{"2Foo", "2_foo"},
		{"RPM60m", "r_p_m60m"},

		// already snake
		{"foo_bar", "foo_bar"},
		{"foo_bar_baz", "foo_bar_baz"},
		{"Foo_Bar", "foo__bar"},
		{"FOO_BAR", "f_o_o__b_a_r"},
		{"foo__bar", "foo__bar"},

		// spaces are stripped
		{"Foo Bar", "foo_bar"},
		{"foo bar", "foobar"},
		{" Foo", "_foo"},
		{"Foo ", "foo"},
		{" Foo Bar ", "_foo_bar"},
		{"A B C", "a_b_c"},

		// punctuation passes through
		{"Foo-Bar", "foo-_bar"},
		{"Foo.Bar", "foo._bar"},
		{"Foo/Bar", "foo/_bar"},
		{"-Foo", "-_foo"},
		{".Foo", "._foo"},
		{"0Foo", "0_foo"},

		// non-ascii is lowercased rune-wise (vm used to leave it uppercase)
		{"Ünicode", "ünicode"},
		{"ünicode", "ünicode"},
		{"ÜNICODE", "ü_n_i_c_o_d_e"},
		{"Ünicode Test", "ünicode_test"},
		{"ΣigmaValue", "σigma_value"},
		{"Σigma", "σigma"},
		{"日本語", "日本語"},
		{"café", "café"},
		{"CAFÉ", "c_a_fé"},
		{"Café", "café"},
		{"ÀÉÎÕÜ", "àéîõü"},
		{"_Ünicode", "_ünicode"},
		{"_ÜFoo", "_ü_foo"},

		// real column names from the models in this repo
		{"Owner", "owner"},
		{"Name", "name"},
		{"CreatedTime", "created_time"},
		{"UpdatedTime", "updated_time"},
		{"DisplayName", "display_name"},
		{"ClientIp", "client_ip"},
		{"PublicIp", "public_ip"},
		{"CpuSize", "cpu_size"},
		{"Os", "os"},
		{"Id", "id"},
		{"User1", "user1"},
		{"IsDeleted", "is_deleted"},
		{"MessageCount", "message_count"},
	}
	for _, c := range cases {
		if got := SnakeString(c.in); got != c.want {
			t.Errorf("SnakeString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSnakeStringKeepsAcronymsNaive locks in behaviour that looks wrong and is not.
//
// An acronym-aware mapper (hanzoai/orm's names.SnakeMapper, for one) turns "ID"
// into "id" and "CPU" into "cpu". SnakeString splits on every capital instead, so
// it yields "i_d" and "c_p_u". The live tables were created with these names, so
// "fixing" the acronym handling renames 24 existing columns across the models in
// hanzoai/ai and this repo and breaks every query that uses them. If you want the
// smarter mapper, migrate the schema first; this test is the tripwire.
func TestSnakeStringKeepsAcronymsNaive(t *testing.T) {
	cases := map[string]string{
		"ID":         "i_d",
		"OrgID":      "org_i_d",
		"CPU":        "c_p_u",
		"URL":        "u_r_l",
		"ClusterIP":  "cluster_i_p",
		"WorkflowID": "workflow_i_d",
	}
	for in, want := range cases {
		if got := SnakeString(in); got != want {
			t.Errorf("SnakeString(%q) = %q, want %q (acronym splitting is deliberate: changing it renames a live column)", in, got, want)
		}
	}
}

// TestWriteBytesToPathReturnsError checks that a failed write is reported to the
// caller instead of taking the process down. This is a library function; the
// caller decides what a write failure means.
func TestWriteBytesToPathReturnsError(t *testing.T) {
	dir := t.TempDir()

	if err := WriteBytesToPath([]byte("hello"), filepath.Join(dir, "ok")); err != nil {
		t.Fatalf("WriteBytesToPath to a writable path: %v", err)
	}

	// A path whose parent is a regular file cannot be created.
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("WriteBytesToPath panicked instead of returning an error: %v", r)
		}
	}()
	if err := WriteBytesToPath([]byte("hello"), filepath.Join(notADir, "child")); err == nil {
		t.Error("WriteBytesToPath to an uncreatable path returned nil, want an error")
	}
}

func TestReadStringFromPathRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "round-trip")
	const want = "contents\n"
	WriteStringToPath(want, path)
	if got := ReadStringFromPath(path); got != want {
		t.Errorf("ReadStringFromPath = %q, want %q", got, want)
	}
}
