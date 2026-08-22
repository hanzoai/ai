// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package controllers

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// `down` is the whole of "may this request be moved at all", and it is a
// statement about the VENDOR rather than about the request.
//
// Every case here is a status a real upstream answers with, and the split is the
// point: an account we cannot pay with and a vendor that fell over are facts that
// outlast the request, so it may be carried elsewhere; a body we malformed, a
// model they never had, content they refused and a queue that is briefly full are
// facts about THIS request, so moving it would hand the caller a smaller model
// instead of the error they need to read.
func TestDownIsAFactAboutTheVendor(t *testing.T) {
	for _, tc := range []struct {
		what string
		err  error
		want bool
	}{
		{"our account with them is spent", &apiError{status: http.StatusPaymentRequired, msg: "Insufficient credits."}, true},
		{"they fell over", &apiError{status: http.StatusInternalServerError, msg: "internal server error"}, true},
		{"a gateway in front of them fell over", &apiError{status: http.StatusBadGateway, msg: "bad gateway"}, true},
		{"they are unavailable", &apiError{status: http.StatusServiceUnavailable, msg: "service unavailable"}, true},
		{"a gateway in front of them timed out", &apiError{status: http.StatusGatewayTimeout, msg: "gateway timeout"}, true},

		{"busy, and it says so", &apiError{status: http.StatusTooManyRequests, msg: "rate limit exceeded"}, false},
		{"busy, but the reason is money", &apiError{status: http.StatusTooManyRequests, msg: "insufficient credit"}, true},

		{"no status to read, and the reason is money", errors.New("insufficient_quota"), true},
		{"no status to read, and the wire simply broke", errors.New("connection reset by peer"), false},

		{"we malformed the body", &apiError{status: http.StatusBadRequest, msg: "malformed request"}, false},
		{"they have not got that model", &apiError{status: http.StatusNotFound, msg: "no such model"}, false},
		{"they refused the content", &apiError{status: http.StatusUnprocessableEntity, msg: "content policy"}, false},
		{"they would not take our credential", &apiError{status: http.StatusUnauthorized, msg: "unauthorized"}, false},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if got := down(tc.err, strings.ToLower(tc.err.Error())); got != tc.want {
				t.Errorf("down(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// fallback answers WHERE a refused request may go, and the empty answer — the
// refusal standing as itself — is as much a result as a list of routes.
//
// It is asserted directly because it is a decision and not a walk: no vendor is
// stood up here, nothing is dialled, and every branch is reachable by stating the
// three facts it reads.
func TestFallbackNamesWhereARefusalMayGo(t *testing.T) {
	// A pool with one borrowed route in it, and our own compute switched off, so
	// "borrowed" and "our own" are told apart by which list comes back rather than
	// by both happening to be empty.
	stage := func(t *testing.T, ourOwn bool) {
		t.Helper()
		fam := freeFamily()
		restore(t, fam)
		restore(t, engineFam)
		fam.spares = []string{"v/borrowed:free"}
		fam.loaded, fam.fetchedAt = true, time.Now()
		if ourOwn {
			t.Setenv("TEST_ENGINE_URL", "http://engine.invalid")
			engineFam.urlKey = "TEST_ENGINE_URL"
		} else {
			engineFam.urlKey = "TEST_ENGINE_URL_UNSET"
		}
		engineFam.providerFn = nil
	}

	spent := &apiError{status: http.StatusPaymentRequired, msg: "Insufficient credits."}

	t.Run("a vendor that cannot serve moves onto the pool", func(t *testing.T) {
		stage(t, true)
		fam := otherFamily(t, "http://vendor.invalid") // states no terms
		got := fallback(fam, "enso", spent, nil)
		if len(got) != 2 {
			t.Fatalf("routes=%v, want the borrowed route and our own", ids(got))
		}
	})

	t.Run("a deny route moves onto our own compute and nothing borrowed", func(t *testing.T) {
		stage(t, true)
		fam := spareFamily(t, "http://vendor.invalid", "v/borrowed:free", "v/paid")
		if word, stated := fam.collection("v/paid"); !stated || word != collectionDeny {
			t.Fatalf("v/paid is bought under (%q, %v), want %q", word, stated, collectionDeny)
		}
		got := fallback(fam, "v/paid", spent, nil)
		if len(got) != 1 || got[0].fam != engineFam {
			t.Fatalf("routes=%v, want our own compute alone", ids(got))
		}
	})

	t.Run("a deny route with no compute of our own stands", func(t *testing.T) {
		stage(t, false)
		fam := spareFamily(t, "http://vendor.invalid", "v/borrowed:free", "v/paid")
		if got := fallback(fam, "v/paid", spent, nil); len(got) != 0 {
			t.Errorf("routes=%v, want none — there is nowhere it may go that keeps its terms", ids(got))
		}
	})

	t.Run("a refusal that is not the vendor's stands", func(t *testing.T) {
		stage(t, true)
		fam := otherFamily(t, "http://vendor.invalid")
		missing := &apiError{status: http.StatusNotFound, msg: "no such model"}
		if got := fallback(fam, "enso", missing, nil); len(got) != 0 {
			t.Errorf("routes=%v, want none — a model they have not got is not an outage", ids(got))
		}
	})

	t.Run("our own spend gate reaches the customer", func(t *testing.T) {
		stage(t, true)
		fam := otherFamily(t, "http://vendor.invalid")
		// The same 402, carrying OUR code: the customer owes money, and answering
		// that with a free model would say their payment problem had fixed itself.
		body := []byte(`{"error":{"code":"insufficient_balance","message":"add funds"}}`)
		if got := fallback(fam, "enso", spent, body); len(got) != 0 {
			t.Errorf("routes=%v, want none — this bill is the customer's to read", ids(got))
		}
	})

	t.Run("no refusal is nothing to move", func(t *testing.T) {
		stage(t, true)
		fam := otherFamily(t, "http://vendor.invalid")
		if got := fallback(fam, "enso", nil, nil); len(got) != 0 {
			t.Errorf("routes=%v, want none", ids(got))
		}
	})
}

// ids renders a route list for a failure message.
func ids(routes []spare) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.id)
	}
	return out
}
