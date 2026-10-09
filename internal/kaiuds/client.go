// Package kaiuds implements Kai's CURRENT local decision socket protocol.
// It is framed JSON, not ZAP RPC. This client is deliberately opt-in and is not
// wired to the public decision endpoint until status/capture parity is proven.
package kaiuds

import (
    "context"
    "encoding/binary"
    "errors"
    "fmt"
    "io"
    "net"
    "time"
    "unicode"
    "unicode/utf8"
)

const (
    maxOrgRunes = 256
    maxOrgBytes = 4 * maxOrgRunes
    maxCapabilitiesBytes = 256 << 10
    maxBodyBytes = 16 << 20
    maxResponseBytes = 32 << 20
    defaultTimeout = 120 * time.Second
)

// Client addresses a trusted Kai service on the same host or pod. The socket
// must be inaccessible to untrusted peers; the wire does not authenticate.
type Client struct {
    Path string
    Timeout time.Duration
}

// Decide sends org, capabilities and the serialized Decision API request,
// followed by one framed reply. The output is raw JSON: the old UDS server
// does not transmit HTTP status codes, retry hints or capture headers.
func (c Client) Decide(ctx context.Context, org, capabilities string, body []byte) ([]byte, error) {
    if c.Path == "" {
        return nil, errors.New("kaiuds: socket path is required")
    }
    if !utf8.ValidString(org) || utf8.RuneCountInString(org) > maxOrgRunes || len(org) > maxOrgBytes {
        return nil, errors.New("kaiuds: invalid organization identity")
    }
    for _, r := range org {
        if unicode.IsControl(r) {
            return nil, errors.New("kaiuds: organization contains a control character")
        }
    }
    if !utf8.ValidString(capabilities) || len(capabilities) > maxCapabilitiesBytes {
        return nil, errors.New("kaiuds: invalid or oversized capabilities")
    }
    if len(body) > maxBodyBytes {
        return nil, errors.New("kaiuds: request body exceeds Kai's 16 MiB limit")
    }

    timeout := c.Timeout
    if timeout <= 0 {
        timeout = defaultTimeout
    }
    bounded, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    conn, err := (&net.Dialer{}).DialContext(bounded, "unix", c.Path)
    if err != nil {
        return nil, fmt.Errorf("kaiuds: connect: %w", err)
    }
    defer conn.Close()
    // A deadline also bounds reads when the peer stops responding; closing on
    // cancellation ensures cancellation is prompt even without a deadline.
    if deadline, ok := bounded.Deadline(); ok {
        if err := conn.SetDeadline(deadline); err != nil {
            return nil, fmt.Errorf("kaiuds: deadline: %w", err)
        }
    }
    stop := context.AfterFunc(bounded, func() { _ = conn.Close() })
    defer stop()

    for _, frame := range [][]byte{[]byte(org), []byte(capabilities), body} {
        if err := writeFrame(conn, frame); err != nil {
            if bounded.Err() != nil {
                return nil, bounded.Err()
            }
            return nil, fmt.Errorf("kaiuds: send: %w", err)
        }
    }
    out, err := readFrame(conn, maxResponseBytes)
    if err != nil {
        if bounded.Err() != nil {
            return nil, bounded.Err()
        }
        return nil, fmt.Errorf("kaiuds: receive: %w", err)
    }
    return out, nil
}

func writeFrame(w io.Writer, payload []byte) error {
    if uint64(len(payload)) > uint64(^uint32(0)) {
        return errors.New("frame cannot fit u32 length")
    }
    var head [4]byte
    binary.LittleEndian.PutUint32(head[:], uint32(len(payload)))
    if err := writeAll(w, head[:]); err != nil {
        return err
    }
    return writeAll(w, payload)
}

// io.Writer may return a short write without an error.
func writeAll(w io.Writer, p []byte) error {
    for len(p) > 0 {
        n, err := w.Write(p)
        if err != nil { return err }
        if n == 0 { return io.ErrShortWrite }
        p = p[n:]
    }
    return nil
}

func readFrame(r io.Reader, max uint32) ([]byte, error) {
    var head [4]byte
    if _, err := io.ReadFull(r, head[:]); err != nil {
        return nil, err
    }
    n := binary.LittleEndian.Uint32(head[:])
    if n > max {
        return nil, fmt.Errorf("response frame length %d exceeds limit %d", n, max)
    }
    out := make([]byte, n)
    _, err := io.ReadFull(r, out)
    return out, err
}
