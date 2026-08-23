// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package txt

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// These readers are handed whatever was uploaded, so what they do with a file
// that is not what it claims to be is a request's business. An error is an
// answer; a panic is the request taking the service with it.
func TestAFileThatIsNotWhatItClaims(t *testing.T) {
	dir := t.TempDir()

	// Three shapes of wrong: not a document at all, an empty file, and a valid
	// zip with none of the parts the format requires (docx, pptx and xlsx are
	// zips, so this is the one that gets furthest in before failing).
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("this is just some text, not a document"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	hollow := filepath.Join(dir, "hollow.zip")
	f, err := os.Create(hollow)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	part, err := w.Create("unrelated.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("nothing a document reader wants")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	missing := filepath.Join(dir, "not-there")

	readers := map[string]func(string) (string, error){
		"pdf":  getTextFromPdf,
		"pptx": getTextFromPptx,
		"xlsx": getTextFromXlsx,
		"docx": func(p string) (string, error) { return GetTextFromDocx(p, "en") },
	}
	for name, read := range readers {
		for _, path := range []string{garbage, empty, hollow, missing} {
			// Reaching the next iteration at all is the assertion: a panic here is
			// the test binary going down, which is the service going down.
			text, err := read(path)
			if err == nil && text != "" {
				t.Errorf("%s read %q out of %s", name, text, filepath.Base(path))
			}
		}
	}
}
