// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package util

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GetFieldFromJsonString reads one field out of a document, whatever shape the
// value has. It is used on stored JSON, so a document that will not parse and a
// field that is not there are different answers: one is broken, the other is
// simply absent.
func TestReadingOneFieldOutOfADocument(t *testing.T) {
	doc := `{"name":"acme","count":7,"on":true,"tags":["a","b"],"nested":{"k":"v"},"nothing":null}`
	for _, c := range []struct{ field, want string }{
		{"name", "acme"},
		{"count", "7"},
		{"on", "true"},
		{"tags", `["a","b"]`},
		{"nested", `{"k":"v"}`},
		{"nothing", "null"},
		{"absent", ""},
	} {
		got, err := GetFieldFromJsonString(doc, c.field)
		if err != nil {
			t.Errorf("%s: %v", c.field, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.field, got, c.want)
		}
	}

	// An empty document has no fields and is not an error.
	if got, err := GetFieldFromJsonString("", "name"); err != nil || got != "" {
		t.Errorf("empty document gave %q, %v", got, err)
	}
	// One that will not parse is.
	if _, err := GetFieldFromJsonString("{not json", "name"); err == nil {
		t.Error("a document that will not parse was read without complaint")
	}
}

// FilterQuery drops named parameters and keeps the rest of the address.
func TestDroppingAQueryParameter(t *testing.T) {
	got := FilterQuery("/v1/ai/get-chats?store=main&accessToken=secret&p=1", []string{"accessToken"})
	if strings.Contains(got, "secret") || strings.Contains(got, "accessToken") {
		t.Errorf("the named parameter survived: %s", got)
	}
	for _, keep := range []string{"store=main", "p=1", "/v1/ai/get-chats"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s was dropped with it: %s", keep, got)
		}
	}
	// Nothing left to keep leaves the path alone, with no trailing question mark.
	if got := FilterQuery("/v1/x?accessToken=secret", []string{"accessToken"}); got != "/v1/x" {
		t.Errorf("a query emptied out gave %q", got)
	}
	// An address with no query is returned as it came.
	if got := FilterQuery("/v1/x", []string{"accessToken"}); got != "/v1/x" {
		t.Errorf("an address with no query gave %q", got)
	}
}

func TestRemovingAValueFromAList(t *testing.T) {
	for _, c := range []struct {
		in   []string
		val  string
		want string
	}{
		{[]string{"a", "b", "c"}, "b", "a,c"},
		{[]string{"a", "b", "b"}, "b", "a"},
		{[]string{"a"}, "z", "a"},
		{nil, "z", ""},
		{[]string{}, "z", ""},
	} {
		if got := strings.Join(DeleteVal(c.in, c.val), ","); got != c.want {
			t.Errorf("DeleteVal(%v, %q) = %q, want %q", c.in, c.val, got, c.want)
		}
	}
}

func TestReadingAWholeNumberStrictly(t *testing.T) {
	for _, s := range []string{"abc", "", "-1", "1.5"} {
		if _, err := ParseIntWithError(s); err == nil {
			t.Errorf("ParseIntWithError(%q) answered no error", s)
		}
	}
	got, err := ParseIntWithError("7")
	if err != nil || got != 7 {
		t.Errorf("ParseIntWithError(\"7\") = %d, %v", got, err)
	}
}

func TestReadingBase64(t *testing.T) {
	in := `{"endpoint":"oss.example"}`
	if got := DecodeBase64(base64.StdEncoding.EncodeToString([]byte(in))); got != in {
		t.Errorf("round trip gave %q", got)
	}
	// Callers hand the result to a reader that reports what it cannot read, so
	// something that is not base64 is the empty string rather than a panic.
	for _, s := range []string{"not base64!!", "==="} {
		if got := DecodeBase64(s); got != "" {
			t.Errorf("DecodeBase64(%q) = %q, want empty", s, got)
		}
	}
}

// An address on the public internet, as distinct from one inside a network.
func TestTellingAPublicAddress(t *testing.T) {
	for _, ip := range []string{"8.8.8.8", "203.0.113.7", "8.8.8.8:443", "2001:4860:4860::8888"} {
		if !IsInternetIp(ip) {
			t.Errorf("%s is a public address and was not called one", ip)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1",
		"::1", "0.0.0.0", "224.0.0.1", "not-an-address", ""} {
		if IsInternetIp(ip) {
			t.Errorf("%s is not a public address and was called one", ip)
		}
	}
}

func TestMakingSureAFolderIsThere(t *testing.T) {
	root := t.TempDir()

	deep := filepath.Join(root, "a", "b", "c")
	EnsureFolderExists(deep)
	if !FileExist(deep) {
		t.Errorf("%s was not created", deep)
	}
	EnsureFolderExists(deep) // again: it already exists, and that is not a failure

	file := filepath.Join(root, "x", "y", "report.pdf")
	EnsureFileFolderExists(file)
	if !FileExist(filepath.Dir(file)) {
		t.Errorf("the folder for %s was not created", file)
	}
	if FileExist(file) {
		t.Error("the file itself was created; only its folder should have been")
	}
}

func TestCopyingAFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dest := filepath.Join(root, "dest.txt")
	if err := os.WriteFile(src, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	CopyFile(dest, src)
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "contents" {
		t.Errorf("the copy reads %q", got)
	}
}
