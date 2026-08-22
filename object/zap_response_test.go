// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package object

import (
	"bytes"
	"testing"
)

// A response is only correct if it READS BACK as what was put in. These build a
// wire message, so the assertion has to be a parse: a field written to the wrong
// slot, or dropped for being empty, is invisible to the builder and arrives as a
// zero at the other end — a 0 status, or an answer with no body, reported as a
// success.
func TestACloudResponseReadsBackAsWhatWasBuilt(t *testing.T) {
	for _, tc := range []struct {
		what   string
		status uint32
		body   []byte
		errMsg string
	}{
		{"an answer", 200, []byte(`{"ok":true}`), ""},
		{"a refusal, which carries words and no body", 402, nil, "insufficient credit"},
		{"a refusal that carries both", 500, []byte(`{"partial":1}`), "upstream failed"},
		{"an empty success", 204, nil, ""},
		{"a body with NUL bytes in it", 200, []byte{0x00, 0x01, 0x00}, ""},
	} {
		t.Run(tc.what, func(t *testing.T) {
			msg, err := BuildCloudResponse(tc.status, tc.body, tc.errMsg)
			if err != nil {
				t.Fatalf("BuildCloudResponse: %v", err)
			}
			root := msg.Root()
			if got := root.Uint32(CloudRespStatus); got != tc.status {
				t.Errorf("status = %d, want %d", got, tc.status)
			}
			if got := root.Bytes(CloudRespBody); !bytes.Equal(got, tc.body) {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
			if got := root.Text(CloudRespError); got != tc.errMsg {
				t.Errorf("error = %q, want %q", got, tc.errMsg)
			}
		})
	}
}

// The gateway answer uses the same three slots and the third is a DIFFERENT
// field: headers where the cloud answer keeps an error. Read back by its own
// name, so a day when either layout moves is a failure here rather than headers
// arriving where somebody reads a reason.
func TestAGatewayResponseReadsBackAsWhatWasBuilt(t *testing.T) {
	for _, tc := range []struct {
		what    string
		status  uint32
		body    []byte
		headers []byte
	}{
		{"an answer with headers", 200, []byte("hello"), []byte(`{"content-type":"text/plain"}`)},
		{"a redirect, all headers and no body", 302, nil, []byte(`{"location":"/elsewhere"}`)},
		{"a bare status", 204, nil, nil},
		{"a body and no headers", 200, []byte("hello"), nil},
	} {
		t.Run(tc.what, func(t *testing.T) {
			msg, err := BuildGatewayResponse(tc.status, tc.body, tc.headers)
			if err != nil {
				t.Fatalf("BuildGatewayResponse: %v", err)
			}
			root := msg.Root()
			if got := root.Uint32(GatewayRespStatus); got != tc.status {
				t.Errorf("status = %d, want %d", got, tc.status)
			}
			if got := root.Bytes(GatewayRespBody); !bytes.Equal(got, tc.body) {
				t.Errorf("body = %q, want %q", got, tc.body)
			}
			if got := root.Bytes(GatewayRespHeaders); !bytes.Equal(got, tc.headers) {
				t.Errorf("headers = %q, want %q", got, tc.headers)
			}
		})
	}
}

// With no node connected, neither capability claims to be there. They are read
// before every ZAP call, and a true here with nothing behind it is a call that
// dials a nil peer.
func TestNoNodeMeansNoZapAndNoDocdb(t *testing.T) {
	zapMu.Lock()
	savedReady, savedNode, savedDocdb := zapReady, zapNode, docdbPeerID
	zapReady, zapNode, docdbPeerID = false, nil, ""
	zapMu.Unlock()
	t.Cleanup(func() {
		zapMu.Lock()
		zapReady, zapNode, docdbPeerID = savedReady, savedNode, savedDocdb
		zapMu.Unlock()
	})

	if ZapEnabled() {
		t.Error("ZapEnabled() with no node behind it")
	}
	if DocdbEnabled() {
		t.Error("DocdbEnabled() with no peer behind it")
	}
}
