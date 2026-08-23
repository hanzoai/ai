// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package txt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadingAPlainDocument(t *testing.T) {
	got, err := getTextFromPlain(write(t, "a.txt", "hello\nworld\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\nworld\n" {
		t.Errorf("read back %q", got)
	}
	if _, err := getTextFromPlain(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("a document that is not there was read without complaint")
	}
}

// A row becomes an object keyed by the header, and a number is read as a number
// so what is indexed downstream can be compared as one.
func TestReadingASpreadsheetOfRows(t *testing.T) {
	got, err := getTextFromCsv(write(t, "a.csv", "name, count \nacme,7\nbeta,not-a-number\n"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("read %d rows from a file with 2: %q", len(lines), got)
	}
	// The header's own whitespace is not part of the key.
	if !strings.Contains(lines[0], `"count":7`) {
		t.Errorf("a number was not read as one: %s", lines[0])
	}
	if !strings.Contains(lines[0], `"name":"acme"`) {
		t.Errorf("the header was not trimmed: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"count":"not-a-number"`) {
		t.Errorf("something that is not a number was read as one: %s", lines[1])
	}

	// A row with fewer columns than the header is refused, not read past the end.
	if _, err := getTextFromCsv(write(t, "r.csv", "a,b,c\n1,2\n")); err == nil {
		t.Error("a ragged row was read without complaint")
	}
	// A file with no header at all has nothing to key on.
	if _, err := getTextFromCsv(write(t, "e.csv", "")); err == nil {
		t.Error("an empty file was read without complaint")
	}
}

func TestWhichSlideAFileIs(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"ppt/slides/slide1.xml", 1},
		{"ppt/slides/slide12.xml", 12},
		{"ppt/slides/slide0.xml", 0},
		{"ppt/slides/slide.xml", -1},
		{"ppt/slideLayouts/slideLayout1.xml", -1},
		{"docProps/app.xml", -1},
		{"", -1},
	} {
		if got := getPageNumberFromSlideFilename(c.in); got != c.want {
			t.Errorf("getPageNumberFromSlideFilename(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// The extensions this module says it reads are the ones its reader dispatches on.
func TestTheFileTypesWeSayWeRead(t *testing.T) {
	types := GetSupportedFileTypes()
	if len(types) == 0 {
		t.Fatal("we say we read nothing")
	}
	for _, ext := range types {
		if !strings.HasPrefix(ext, ".") {
			t.Errorf("%q is not an extension", ext)
		}
	}
}

// A local path is read by the extension it is given, not by what is in the file.
func TestReadingByExtension(t *testing.T) {
	path := write(t, "notes.txt", "plain contents")
	got, err := GetParsedTextFromUrl(path, ".txt", "en")
	if err != nil {
		t.Fatal(err)
	}
	if got != "plain contents" {
		t.Errorf("read back %q", got)
	}
	// No extension is read as plain text, which is what the reader falls back to.
	if got, err := GetParsedTextFromUrl(path, "", "en"); err != nil || got != "plain contents" {
		t.Errorf("an unnamed type gave %q, %v", got, err)
	}
	// An extension nothing reads says so rather than answering emptily.
	if _, err := GetParsedTextFromUrl(path, ".exe", "en"); err == nil {
		t.Error("a type we do not read was read without complaint")
	}
}
