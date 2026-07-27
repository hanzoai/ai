package iam

import (
	"encoding/json"
	"testing"
)

// The envelope must decode whichever shape the server sends. The numeric form is
// what shipped in iam v1.33.x and it took down authentication fleet-wide: the decode
// failed, the caller read that as "IAM unreachable", and the binary served 401 to
// every authenticated request while still passing its probes.
func TestResponseStatusAcceptsBothWireShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want Status
	}{
		{"string ok — the historical shape", `{"status":"ok","msg":""}`, "ok"},
		{"string error is preserved verbatim", `{"status":"error","msg":"nope"}`, "error"},
		{"numeric 200 is success", `{"status":200,"msg":""}`, "ok"},
		{"numeric 299 is still success", `{"status":299,"msg":""}`, "ok"},
		{"numeric 500 stays an error, not laundered into ok", `{"status":500,"msg":""}`, "500"},
		{"numeric 404 stays an error", `{"status":404,"msg":""}`, "404"},
		{"null decodes to empty, not a failure", `{"status":null,"msg":""}`, ""},
		{"absent field decodes to empty", `{"msg":""}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r Response
			if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
				t.Fatalf("decode %s: unexpected error %v", tc.body, err)
			}
			if r.Status != tc.want {
				t.Fatalf("decode %s: status = %q, want %q", tc.body, r.Status, tc.want)
			}
		})
	}
}

// Only "ok" may pass the success check the callers make (`response.Status != "ok"`).
// A 2xx number must reach it as success; anything else must not.
func TestStatusSuccessSemanticsMatchCallers(t *testing.T) {
	success := []string{`"ok"`, `200`, `201`, `204`}
	failure := []string{`"error"`, `400`, `401`, `500`, `null`}

	for _, body := range success {
		var s Status
		if err := json.Unmarshal([]byte(body), &s); err != nil {
			t.Fatalf("%s: unexpected error %v", body, err)
		}
		if s != "ok" {
			t.Fatalf("%s: got %q, want it to read as success", body, s)
		}
	}
	for _, body := range failure {
		var s Status
		if err := json.Unmarshal([]byte(body), &s); err != nil {
			t.Fatalf("%s: unexpected error %v", body, err)
		}
		if s == "ok" {
			t.Fatalf("%s: decoded to %q — an error must never read as success", body, s)
		}
	}
}

// A shape that is neither string nor number is a real error: guessing is what let the
// original drift go unnoticed.
func TestStatusRejectsUnknownShapes(t *testing.T) {
	for _, body := range []string{`{"a":1}`, `[1,2]`, `true`} {
		var s Status
		if err := json.Unmarshal([]byte(body), &s); err == nil {
			t.Fatalf("%s: decoded to %q, want an error", body, s)
		}
	}
}
