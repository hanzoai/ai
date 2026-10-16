// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package controllers

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// useKaiURLs boots KAI_URLS as spec, and restores what was there.
func useKaiURLs(t *testing.T, spec string) {
	t.Helper()
	prev := kaiURLs.Load()
	t.Cleanup(func() { kaiURLs.Store(prev) })
	t.Setenv("KAI_URLS", spec)
	if err := InitKaiURLs(); err != nil {
		t.Fatalf("KAI_URLS=%q: %v", spec, err)
	}
}

// versionService is a decision service of its own, as a Kai version's deployment is.
func versionService(t *testing.T, answer string) (*fakeDecisions, string) {
	t.Helper()
	f := &fakeDecisions{status: http.StatusOK, answer: answer}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// askFor is decisionBody asking for model.
func askFor(model string) string {
	return strings.Replace(decisionBody, `"model":"kai"`, `"model":"`+model+`"`, 1)
}

// A version KAI_URLS names is answered by its own service, asked there for kai with
// the org, the request id and the capabilities the default path sends, and billed as
// kai at kai's price. Its answer names the version. kai, and a version KAI_URLS does
// not name, still go to KAI_URL.
func TestKaiURLsSendAVersionToItsService(t *testing.T) {
	fake, events := setupDecisions(t)
	fake.answer = heldAnswer
	v14, at := versionService(t, heldAnswer)
	useKaiURLs(t, "kai-1.4="+at+"/")

	// Its members in the order the service is sent them, so the body kai-1.4's
	// service is asked is byte for byte the body kai's is.
	const kaiBody = `{"model":"kai","questions":{"is_bug":{"type":"noul","instructions":"Is this a bug?"}},"state":{"message":"Payment failed twice"}}`
	v14Body := strings.Replace(kaiBody, `"model":"kai"`, `"model":"kai-1.4"`, 1)

	status, body := driveDecisions(t, "Bearer "+decisionsKey, v14Body)
	if status != http.StatusOK {
		t.Fatalf("kai-1.4 => %d %s", status, body)
	}
	if !sameJSON([]byte(body), []byte(strings.Replace(heldAnswer, `"model":"kai"`, `"model":"kai-1.4"`, 1))) {
		t.Fatalf("answer %s, want kai's answer naming kai-1.4", body)
	}
	calls, path, sent := v14.seen()
	if calls != 1 || path != decisionsPath || string(sent) != kaiBody {
		t.Fatalf("kai-1.4's service saw %d call(s) at %q with %s; want 1 at %s asking for kai", calls, path, sent, decisionsPath)
	}
	v14.mu.Lock()
	org, rid := v14.org, v14.rid
	v14.mu.Unlock()
	if org != decisionsOrg || rid == "" {
		t.Fatalf("kai-1.4's service was told org %q, request id %q", org, rid)
	}
	if n, _, _ := fake.seen(); n != 0 {
		t.Fatalf("KAI_URL was asked %d time(s) for a version with a service of its own", n)
	}
	e := (*events)[len(*events)-1]
	if e.Model != "kai" || e.Provider != "kai" || e.USD != nanoToUSD(42*21) {
		t.Fatalf("debit %s/%s $%s, want kai/kai at 42 tokens × $0.021/M", e.Provider, e.Model, e.USD)
	}

	// kai, asked the same body by the same org, is KAI_URL's: kai-1.4's held answer
	// is never given to it.
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, kaiBody); status != http.StatusOK || body != heldAnswer {
		t.Fatalf("kai => %d %s", status, body)
	}
	if n, _, sent := fake.seen(); n != 1 || string(sent) != kaiBody {
		t.Fatalf("KAI_URL was asked %d time(s) for kai, want 1 (%s)", n, sent)
	}
	// And each is held apart: asked again, each is answered from its own.
	driveDecisions(t, "Bearer "+decisionsKey, v14Body)
	driveDecisions(t, "Bearer "+decisionsKey, kaiBody)
	if a, _, _ := v14.seen(); a != 1 {
		t.Fatalf("kai-1.4's service was asked %d time(s) for a held answer", a)
	}
	if b, _, _ := fake.seen(); b != 1 {
		t.Fatalf("KAI_URL was asked %d time(s) for a held answer", b)
	}

	// A version no pair names goes to KAI_URL, asked as written.
	if status, body := driveDecisions(t, "Bearer "+decisionsKey, askFor("kai-1.5")); status != http.StatusOK {
		t.Fatalf("kai-1.5 => %d %s", status, body)
	}
	if n, _, sent := fake.seen(); n != 2 || string(sent) != askFor("kai-1.5") {
		t.Fatalf("KAI_URL saw %d call(s), last %s; want kai-1.5 as asked", n, sent)
	}
}

// With KAI_URLS unset or blank, every decision is today's: kai and every version go
// to KAI_URL, the body as written, the answer as given, one debit at kai's price.
func TestKaiURLsBlankChangesNothing(t *testing.T) {
	for _, spec := range []string{"", "  "} {
		fake, events := setupDecisions(t)
		useKaiURLs(t, spec)
		if kaiURLs.Load() != nil {
			t.Fatalf("KAI_URLS=%q names versions", spec)
		}
		for _, model := range []string{"kai", "kai-1.4", "kai-0834a74f2d14"} {
			asked := askFor(model)
			status, body := driveDecisions(t, "Bearer "+decisionsKey, asked)
			if status != http.StatusOK || body != decisionAnswer {
				t.Fatalf("%s => %d %s", model, status, body)
			}
			if _, path, sent := fake.seen(); path != decisionsPath || string(sent) != asked {
				t.Fatalf("%s reached KAI_URL at %q as %s", model, path, sent)
			}
			e := (*events)[len(*events)-1]
			if e.Model != "kai" || e.Provider != "kai" || e.USD != nanoToUSD(42*21) {
				t.Fatalf("%s: debit %s/%s $%s", model, e.Provider, e.Model, e.USD)
			}
		}
		if n, _, _ := fake.seen(); n != 3 || len(*events) != 3 {
			t.Fatalf("%d calls, %d debits; want 3 of each", n, len(*events))
		}
	}
}

// A Kai version is kai- and the weights' first 12 hex, or a release number of two
// or three parts, each a number without a leading zero.
func TestKaiVersion(t *testing.T) {
	for id, want := range map[string]bool{
		"kai-0834a74f2d14": true, "kai-1.4": true, "kai-1.4.2": true, "kai-0.1": true, "kai-10.20.30": true, "kai-2.0.0": true,
		"kai": false, "kai-": false, "kai-1": false, "kai-1.": false, "kai-.4": false, "kai-1.4.": false, "kai-1..4": false,
		"kai-1.4.2.1": false, "kai-01.4": false, "kai-1.04": false, "kai-1.4-rc1": false, "kai-v1.4": false, "KAI-1.4": false,
		"kai-1.a": false, "kai-0834A74F2D14": false, "kai-0834a74f2d1": false, "zen-1.4": false, " kai-1.4": false,
	} {
		if got := kaiVersion(id); got != want {
			t.Errorf("kaiVersion(%q) = %v, want %v", id, got, want)
		}
	}
	for model, want := range map[string]string{"kai-1.4": "kai", "kai-0834a74f2d14": "kai", "kai": "kai", "kai-01.4": "kai-01.4", "zenlm/zen6": "zenlm/zen6", "gpt-4o": "gpt-4o"} {
		if got := DecisionAs(model); got != want {
			t.Errorf("DecisionAs(%q) = %q, want %q", model, got, want)
		}
	}
}

// /v1/models lists every version KAI_URLS names as Kai's own row under its id: owned
// by hanzo, at kai's price, class and family. Blank, it lists none.
func TestKaiURLsAreListed(t *testing.T) {
	setupDecisions(t)
	useKaiURLs(t, "")
	if lists(t, "kai-1.4") {
		t.Fatal("kai-1.4 is listed with KAI_URLS blank")
	}
	useKaiURLs(t, "kai-1.4=http://kai-1-4.hanzo.svc:8080,kai-1.5.1=http://kai-1-5.hanzo.svc:8080")
	byID := map[string]modelInfo{}
	var order []string
	for _, m := range listAvailableModels() {
		byID[m.ID] = m
		order = append(order, m.ID)
	}
	kai, ok := byID["kai"]
	if !ok {
		t.Fatal("kai is not listed")
	}
	for _, id := range []string{"kai-1.4", "kai-1.5.1"} {
		m, ok := byID[id]
		if !ok {
			t.Fatalf("%s is not listed", id)
		}
		if m.OwnedBy != "hanzo" || m.Object != "model" || m.Class != kai.Class || m.Family != "kai" || m.CanonicalSlug != "hanzo/"+id {
			t.Fatalf("%s listed as %+v; kai is %+v", id, m, kai)
		}
		if m.Pricing == nil || !reflect.DeepEqual(m.Pricing, kai.Pricing) || m.Free != kai.Free {
			t.Fatalf("%s priced %+v, kai %+v", id, m.Pricing, kai.Pricing)
		}
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] >= order[i] {
			t.Fatalf("the listing is out of order at %q, %q", order[i-1], order[i])
		}
	}
}

// A malformed KAI_URLS refuses the boot, saying which pair and why.
func TestKaiURLsMalformedRefusesBoot(t *testing.T) {
	prev := kaiURLs.Load()
	t.Cleanup(func() { kaiURLs.Store(prev) })
	for spec, says := range map[string]string{
		"kai-1.4":                                     "is not id=url",
		"http://kai-1-4.hanzo.svc:8080":               "is not id=url",
		"kai-1.4=http://a:8080,,":                     "entry 2",
		"kai=http://kai.hanzo.svc:8080":               "kai itself is KAI_URL's",
		"zenlm/zen6=http://zen":                       "is no Kai version",
		"kai-01.4=http://a":                           "is no Kai version",
		"=http://a":                                   "is no Kai version",
		"kai-1.4=":                                    "is not an http or https address",
		"kai-1.4=kai-1-4.hanzo.svc:8080":              "is not an http or https address",
		"kai-1.4=ftp://a":                             "is not an http or https address",
		"kai-1.4=http://":                             "is not an http or https address",
		"kai-1.4=http://a?x=1":                        "is not an http or https address",
		"kai-1.4=http://u:p@a":                        "carries a credential",
		"kai-1.4=http://a,kai-1.4=http://b":           "kai-1.4 is named twice",
		"kai-1.4=http://a, kai-1.5=http://b, bogus=x": "entry 3",
	} {
		t.Setenv("KAI_URLS", spec)
		err := InitKaiURLs()
		if err == nil || !strings.HasPrefix(err.Error(), "KAI_URLS: ") || !strings.Contains(err.Error(), says) {
			t.Errorf("KAI_URLS=%q => %v, want a refusal saying %q", spec, err, says)
		}
		if strings.Contains(spec, "u:p") && err != nil && strings.Contains(err.Error(), "u:p") {
			t.Errorf("the refusal repeats the credential: %v", err)
		}
	}
	t.Setenv("KAI_URLS", " kai-1.4 = http://kai-1-4.hanzo.svc:8080/ , kai-0834a74f2d14=https://kai-a7.hanzo.svc ")
	if err := InitKaiURLs(); err != nil {
		t.Fatal(err)
	}
	if u, ok := kaiURL("kai-1.4"); !ok || u != "http://kai-1-4.hanzo.svc:8080" {
		t.Fatalf("kai-1.4 at %q, %v", u, ok)
	}
	if u, ok := kaiURL("kai-0834a74f2d14"); !ok || u != "https://kai-a7.hanzo.svc" {
		t.Fatalf("kai-0834a74f2d14 at %q, %v", u, ok)
	}
	if _, ok := kaiURL("kai"); ok {
		t.Fatal("kai has an address of its own")
	}
}
