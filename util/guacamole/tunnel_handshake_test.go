// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package guacamole

import (
	"bufio"
	"net"
	"testing"
	"time"
)

// guacd answers "select" with the arguments it wants and then goes away. The
// handshake's next write fails, and that failure is the handshake's answer. It
// used to return nil, which NewTunnel took for success: it kept a dead
// connection and handed back a tunnel that was never opened.
func TestAHandshakeWriteThatFailsIsAnError(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() })

	go func() {
		defer server.Close()
		if _, err := bufio.NewReader(server).ReadString(Delimiter); err != nil {
			return // the select never arrived; the handshake fails on its own
		}
		server.Write([]byte(NewInstruction("args", "hostname").String()))
	}()

	tunnel := &Tunnel{conn: client, reader: bufio.NewReader(client), writer: bufio.NewWriter(client)}
	config := NewConfiguration()
	config.Protocol = "rdp"

	done := make(chan error, 1)
	go func() { done <- tunnel.Handshake(config) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the handshake reported success over a connection guacd had closed")
		}
		if tunnel.IsOpen {
			t.Error("a failed handshake left the tunnel marked open")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handshake did not return")
	}
}
