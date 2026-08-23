// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package i18n

import (
	"sync"
	"testing"
)

// Translate is called from the filter chain on every error carrying a
// translation key, so it is reached concurrently by definition.
//
// The language table is filled on first use. Filling it without a guard is not a
// data race that produces a wrong answer — a concurrent map write is a Go runtime
// FATAL ERROR, which recover() does not see and which ends the process. Run under
// -race this fails on the read; run without it, it ends the test binary outright.
func TestTranslatingInManyLanguagesAtOnce(t *testing.T) {
	langs := []string{"en", "zh", "fr", "de", "ja", "ko", "es", "ru", "pt", "it", "nl", "not-a-language"}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lang := langs[i%len(langs)]
			if got := Translate(lang, "auth:Please sign in first"); got == "" {
				t.Errorf("%s translated to nothing", lang)
			}
		}(i)
	}
	wg.Wait()
}

// A language the build does not carry falls back rather than failing, and English
// falls back to the key itself rather than recursing.
func TestAnUnknownLanguageFallsBack(t *testing.T) {
	if got := Translate("not-a-language", "auth:Please sign in first"); got == "" {
		t.Error("an unknown language translated to nothing")
	}
	if got := Translate("en", "auth:a key no locale carries"); got != "a key no locale carries" {
		t.Errorf("an unknown key = %q, want the key itself", got)
	}
	// Text with no key at all is reported rather than parsed.
	if got := Translate("en", "no colon here"); got == "" {
		t.Error("text with no key translated to nothing")
	}
}
