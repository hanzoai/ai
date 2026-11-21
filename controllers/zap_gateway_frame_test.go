package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luxfi/zap"

	"github.com/hanzoai/ai/object"
)

func frameOf(msg *zap.Message) uint16 { return msg.Flags() >> 8 }

// A request that arrives as a gateway frame (MsgType 200) is answered with a gateway
// frame on the fast paths too; the same handler answers a native cloud request
// (MsgType 100) with a cloud frame.
func TestGatewayFastPathsReplyWithGatewayFrames(t *testing.T) {
	paths := []struct {
		name, path, method string
		cloud              string
	}{
		{"chat", "/v1/chat/completions", http.MethodPost, "chat.completions"},
		{"balance", "/v1/balance", http.MethodGet, "balance"},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			msg, err := gateway(nil)(context.Background(), "gw", gatewayGet(t, p.path, "", map[string]string{}, "{}"))
			if err != nil {
				t.Fatal(err)
			}
			if got := frameOf(msg); got != object.MsgTypeHTTPRequest {
				t.Fatalf("gateway %s answered MsgType %d, want %d", p.path, got, object.MsgTypeHTTPRequest)
			}
			status, body := antDecodeGateway(t, msg)
			if status != 401 {
				t.Fatalf("gateway %s status %d, want 401", p.path, status)
			}
			var refusal struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &refusal); err != nil || refusal.Error == "" {
				t.Fatalf("gateway %s body %q is not an {\"error\"} refusal: %v", p.path, body, err)
			}

			native, err := handleCloudService(context.Background(), "peer", cloudCall(t, p.cloud, "", "{}"))
			if err != nil {
				t.Fatal(err)
			}
			if got := frameOf(native); got != object.MsgTypeCloud {
				t.Fatalf("native %s answered MsgType %d, want %d", p.cloud, got, object.MsgTypeCloud)
			}
		})
	}
}

// asGateway keeps a success body and the status as they are.
func TestAsGatewayCarriesStatusAndBody(t *testing.T) {
	cloud, err := object.BuildCloudResponse(200, []byte(`{"ok":true}`), "")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := asGateway(cloud, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := frameOf(msg); got != object.MsgTypeHTTPRequest {
		t.Fatalf("MsgType %d, want %d", got, object.MsgTypeHTTPRequest)
	}
	status, body := antDecodeGateway(t, msg)
	if status != 200 || string(body) != `{"ok":true}` {
		t.Fatalf("got %d %q", status, body)
	}
}
